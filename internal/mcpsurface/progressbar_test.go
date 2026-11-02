package mcpsurface

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
	"github.com/carlosFreak/gyroflow-mcp/internal/jobengine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestProgressBar(t *testing.T) {
	cases := []struct {
		name string
		job  jobengine.Job
		want string
	}{
		{
			"running with frames",
			jobengine.Job{State: jobengine.StateRunning, Progress: backend.Progress{Percent: 25, Frame: 1147, Total: 4589}},
			"[██████░░░░░░░░░░░░░░░░░░] 25.0% · 1147/4589 frames",
		},
		{
			"queued",
			jobengine.Job{State: jobengine.StateQueued},
			"[░░░░░░░░░░░░░░░░░░░░░░░░] 0.0% · queued",
		},
		{
			"completed is always full",
			jobengine.Job{State: jobengine.StateCompleted, Progress: backend.Progress{Percent: 99.2}},
			"[████████████████████████] 100.0% · completed",
		},
		{
			"failed keeps real progress",
			jobengine.Job{State: jobengine.StateFailed, Progress: backend.Progress{Percent: 50}},
			"[████████████░░░░░░░░░░░░] 50.0% · failed",
		},
		{
			"out of range is clamped",
			jobengine.Job{State: jobengine.StateRunning, Progress: backend.Progress{Percent: 140}},
			"[████████████████████████] 100.0%",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := progressBar(c.job); got != c.want {
				t.Fatalf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestProgressBarManyFiles(t *testing.T) {
	j := jobengine.Job{State: jobengine.StateRunning, Progress: backend.Progress{
		Percent: 31, Frame: 121, Total: 390,
		Files: []backend.FileProgress{
			{Name: "b.mp4", Percent: 44.8, Frame: 121, Total: 270, State: backend.FileRendering},
			{Name: "a.mp4", Total: 120, State: backend.FilePending},
		},
	}}
	want := "[██████████░░░░░░░░░░░░░░] 44.8% · 121/270 frames · file 1/2: b.mp4\n" +
		"[███████░░░░░░░░░░░░░░░░░] 31.0% · 121/390 frames · total"
	if got := progressBar(j); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

// blockingBackend reports 50% and stays running until release is closed.
type blockingBackend struct {
	okBackend
	release chan struct{}
}

func (b blockingBackend) Stabilize(ctx context.Context, _ backend.StabilizeRequest, p backend.ProgressFunc) (*backend.Result, error) {
	p(backend.Progress{Percent: 50, Frame: 5, Total: 10, Stage: "render"})
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &backend.Result{OutputPaths: []string{"out.mp4"}}, nil
}

func waitTestClient(t *testing.T, be backend.Backend, onProgress func(*mcp.ProgressNotificationParams)) (*mcp.ClientSession, *jobengine.Engine) {
	t.Helper()
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	eng := jobengine.New(be, 1)
	RegisterTools(srv, eng, func(context.Context) DoctorResult { return DoctorResult{} }, nil)
	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, r *mcp.ProgressNotificationClientRequest) {
			onProgress(r.Params)
		},
	})
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, t1, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := c.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs, eng
}

// TestRenderWaitStreamsBarUntilCompleted verifies the live path: while the
// render_wait request is open, notifications carry the bar, and the call
// returns only once the job is terminal.
func TestRenderWaitStreamsBarUntilCompleted(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	var messages []string
	cs, eng := waitTestClient(t, blockingBackend{release: release}, func(p *mcp.ProgressNotificationParams) {
		mu.Lock()
		defer mu.Unlock()
		messages = append(messages, p.Message)
		if strings.Contains(p.Message, "50.0%") {
			select {
			case <-release:
			default:
				close(release)
			}
		}
	})

	id := eng.StartStabilize(backend.StabilizeRequest{}, nil)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "render_wait",
		Arguments: map[string]any{"job_id": id, "timeout_s": 10},
		Meta:      mcp.Meta{"progressToken": "w1"},
	})
	if err != nil || res.IsError {
		t.Fatalf("render_wait failed: %v %+v", err, res)
	}
	st := res.StructuredContent.(map[string]any)
	if st["state"] != "completed" {
		t.Fatalf("state = %v, want completed", st["state"])
	}
	if !strings.Contains(st["progress_bar"].(string), "100.0%") {
		t.Fatalf("progress_bar = %v", st["progress_bar"])
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		last := ""
		if len(messages) > 0 {
			last = messages[len(messages)-1]
		}
		mu.Unlock()
		if strings.Contains(last, "100.0%") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no final 100%% notification; got %q", messages)
}

// TestRenderWaitTimeoutLeavesJobRunning verifies a wait that times out reports
// the current state and does not disturb the render.
func TestRenderWaitTimeoutLeavesJobRunning(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	cs, eng := waitTestClient(t, blockingBackend{release: release}, func(*mcp.ProgressNotificationParams) {})

	id := eng.StartStabilize(backend.StabilizeRequest{}, nil)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "render_wait",
		Arguments: map[string]any{"job_id": id, "timeout_s": 1},
	})
	if err != nil || res.IsError {
		t.Fatalf("render_wait failed: %v %+v", err, res)
	}
	if st := res.StructuredContent.(map[string]any); st["state"] != "running" {
		t.Fatalf("state = %v, want running", st["state"])
	}
	if j, _ := eng.Status(id); j.State != jobengine.StateRunning {
		t.Fatalf("job state = %v, want running", j.State)
	}
}

func TestRenderWaitUnknownJobIsError(t *testing.T) {
	cs, _ := waitTestClient(t, okBackend{}, func(*mcp.ProgressNotificationParams) {})
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "render_wait",
		Arguments: map[string]any{"job_id": "nope"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected IsError for unknown job_id")
	}
}
