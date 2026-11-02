package jobengine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
)

type fakeBackend struct{}

func (fakeBackend) Stabilize(ctx context.Context, r backend.StabilizeRequest, p backend.ProgressFunc) (*backend.Result, error) {
	p(backend.Progress{Percent: 100, Frame: 1, Total: 1, Stage: "render"})
	return &backend.Result{OutputPaths: []string{"clip_stabilized.mp4"}, ExitCode: 0}, nil
}
func (fakeBackend) ExportProject(context.Context, backend.ExportRequest) (*backend.Result, error) {
	return &backend.Result{}, nil
}
func (fakeBackend) ProbeMetadata(context.Context, string, backend.ProbeOptions) (*backend.MetadataResult, error) {
	return nil, nil
}
func (fakeBackend) ExportSTMap(context.Context, backend.STMapRequest) (*backend.Result, error) {
	return &backend.Result{}, nil
}

func TestEngineRunToCompletion(t *testing.T) {
	e := New(fakeBackend{}, 1)
	id := e.StartStabilize(backend.StabilizeRequest{Inputs: []string{"clip.mp4"}}, nil)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if j, _ := e.Status(id); j.State == StateCompleted {
			if len(j.OutputPaths) != 1 {
				t.Fatalf("expected 1 output path on completion, got %+v", j)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not complete in time")
}

type blockingBackend struct{ release chan struct{} }

func (b blockingBackend) Stabilize(ctx context.Context, r backend.StabilizeRequest, p backend.ProgressFunc) (*backend.Result, error) {
	<-ctx.Done()
	return &backend.Result{}, ctx.Err()
}
func (blockingBackend) ExportProject(context.Context, backend.ExportRequest) (*backend.Result, error) {
	return &backend.Result{}, nil
}
func (blockingBackend) ProbeMetadata(context.Context, string, backend.ProbeOptions) (*backend.MetadataResult, error) {
	return nil, nil
}
func (blockingBackend) ExportSTMap(context.Context, backend.STMapRequest) (*backend.Result, error) {
	return &backend.Result{}, nil
}

// countingBlockingBackend blocks in Stabilize until ctx is done or unblock is closed,
// and counts how many times Stabilize was called.
type countingBlockingBackend struct {
	calls   atomic.Int32
	unblock chan struct{}
}

func (b *countingBlockingBackend) Stabilize(ctx context.Context, r backend.StabilizeRequest, p backend.ProgressFunc) (*backend.Result, error) {
	b.calls.Add(1)
	select {
	case <-b.unblock:
		return &backend.Result{}, nil
	case <-ctx.Done():
		return &backend.Result{}, ctx.Err()
	}
}
func (*countingBlockingBackend) ExportProject(context.Context, backend.ExportRequest) (*backend.Result, error) {
	return &backend.Result{}, nil
}
func (*countingBlockingBackend) ProbeMetadata(context.Context, string, backend.ProbeOptions) (*backend.MetadataResult, error) {
	return nil, nil
}
func (*countingBlockingBackend) ExportSTMap(context.Context, backend.STMapRequest) (*backend.Result, error) {
	return &backend.Result{}, nil
}

// TestEngineCancelQueuedJob verifies that a job waiting for the semaphore
// (concurrency=1, slot occupied) transitions to StateCancelled when Cancel()
// is called, and that its Stabilize is NEVER invoked.
func TestEngineCancelQueuedJob(t *testing.T) {
	be := &countingBlockingBackend{unblock: make(chan struct{})}
	e := New(be, 1)

	// Job1: occupies the single slot.
	id1 := e.StartStabilize(backend.StabilizeRequest{Inputs: []string{"a.mp4"}}, nil)
	for { // wait until job1 is running (slot taken)
		if j, _ := e.Status(id1); j.State == StateRunning {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Job2: queues - cannot acquire the semaphore.
	id2 := e.StartStabilize(backend.StabilizeRequest{Inputs: []string{"b.mp4"}}, nil)
	// Give goroutine a moment to reach the semaphore select.
	time.Sleep(20 * time.Millisecond)

	// Cancel job2 while it's still queued.
	if !e.Cancel(id2) {
		t.Fatal("Cancel(id2) returned false")
	}

	// Job2 must reach StateCancelled.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if j, _ := e.Status(id2); j.State == StateCancelled {
			// Assert Stabilize was NOT called for job2 (only job1 called it).
			if got := be.calls.Load(); got != 1 {
				t.Fatalf("expected Stabilize called 1 time (job1 only), got %d", got)
			}
			// Cleanup: release job1.
			e.Cancel(id1)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job2 did not reach StateCancelled - queued-job cancel not honored")
}

// selectiveBackend blocks Stabilize when Inputs[0]=="block" (until release or
// ctx done) and completes immediately otherwise.
type selectiveBackend struct{ release chan struct{} }

func (b *selectiveBackend) Stabilize(ctx context.Context, r backend.StabilizeRequest, p backend.ProgressFunc) (*backend.Result, error) {
	if len(r.Inputs) > 0 && r.Inputs[0] == "block" {
		select {
		case <-b.release:
			return &backend.Result{}, nil
		case <-ctx.Done():
			return &backend.Result{}, ctx.Err()
		}
	}
	return &backend.Result{OutputPaths: []string{"out.mp4"}}, nil
}
func (*selectiveBackend) ExportProject(context.Context, backend.ExportRequest) (*backend.Result, error) {
	return &backend.Result{}, nil
}
func (*selectiveBackend) ProbeMetadata(context.Context, string, backend.ProbeOptions) (*backend.MetadataResult, error) {
	return nil, nil
}
func (*selectiveBackend) ExportSTMap(context.Context, backend.STMapRequest) (*backend.Result, error) {
	return &backend.Result{}, nil
}

func waitState(t *testing.T, e *Engine, id string, want State) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if j, ok := e.Status(id); ok && j.State == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s never reached state %s", id, want)
}

// TestEngineEvictsOldTerminalJobs verifies that terminal jobs beyond the
// retention cap are evicted oldest-first, while queued/running jobs are NEVER
// evicted and a still-running job's status stays intact.
func TestEngineEvictsOldTerminalJobs(t *testing.T) {
	be := &selectiveBackend{release: make(chan struct{})}
	e := New(be, 10)  // enough concurrency so quick jobs run alongside the blocker
	e.maxTerminal = 2 // white-box test seam (same package)

	// One long-running job that must NEVER be evicted.
	runID := e.StartStabilize(backend.StabilizeRequest{Inputs: []string{"block"}}, nil)
	waitState(t, e, runID, StateRunning)

	// Five quick jobs that complete, in order.
	var ids []string
	for range 5 {
		id := e.StartStabilize(backend.StabilizeRequest{Inputs: []string{"quick"}}, nil)
		waitState(t, e, id, StateCompleted)
		ids = append(ids, id)
	}

	// Cap=2 → the 3 oldest terminal jobs evicted, last 2 retained.
	for _, id := range ids[:3] {
		if _, ok := e.Status(id); ok {
			t.Fatalf("expected oldest terminal job %s to be evicted", id)
		}
	}
	for _, id := range ids[3:] {
		if _, ok := e.Status(id); !ok {
			t.Fatalf("expected recent terminal job %s to be retained", id)
		}
	}
	// Running job must still be present and running.
	if j, ok := e.Status(runID); !ok || j.State != StateRunning {
		t.Fatalf("running job must never be evicted; ok=%v state=%v", ok, j.State)
	}
	// Total retained = 2 terminal + 1 running.
	if n := len(e.List()); n != 3 {
		t.Fatalf("expected 3 retained jobs (2 terminal + 1 running), got %d", n)
	}
	close(be.release) // let the blocker finish
}

func TestEngineCancel(t *testing.T) {
	e := New(blockingBackend{}, 1)
	id := e.StartStabilize(backend.StabilizeRequest{Inputs: []string{"c.mp4"}}, nil)
	for { // wait until running
		if j, _ := e.Status(id); j.State == StateRunning {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !e.Cancel(id) {
		t.Fatal("Cancel returned false")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if j, _ := e.Status(id); j.State == StateCancelled {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not reach cancelled")
}
