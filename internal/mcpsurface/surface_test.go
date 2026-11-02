package mcpsurface

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
	"github.com/carlosFreak/gyroflow-mcp/internal/jobengine"
	"github.com/carlosFreak/gyroflow-mcp/internal/lens"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// progressBackend is like okBackend but also fires a progress callback
// so that the MCP progress notification path is exercised.
type progressBackend struct{}

func (progressBackend) Stabilize(ctx context.Context, r backend.StabilizeRequest, p backend.ProgressFunc) (*backend.Result, error) {
	if p != nil {
		p(backend.Progress{Percent: 50, Frame: 1, Total: 2, Stage: "render"})
		p(backend.Progress{Percent: 100, Frame: 2, Total: 2, Stage: "render"})
	}
	return &backend.Result{OutputPaths: []string{"out.mp4"}, ExitCode: 0}, nil
}
func (progressBackend) ExportProject(context.Context, backend.ExportRequest) (*backend.Result, error) {
	return &backend.Result{}, nil
}
func (progressBackend) ProbeMetadata(context.Context, string, backend.ProbeOptions) (*backend.MetadataResult, error) {
	return &backend.MetadataResult{RawPath: "/tmp/meta.json"}, nil
}
func (progressBackend) ExportSTMap(context.Context, backend.STMapRequest) (*backend.Result, error) {
	return &backend.Result{}, nil
}

type okBackend struct{}

func (okBackend) Stabilize(ctx context.Context, r backend.StabilizeRequest, p backend.ProgressFunc) (*backend.Result, error) {
	if p != nil {
		p(backend.Progress{Percent: 100, Frame: 1, Total: 1})
	}
	return &backend.Result{OutputPaths: []string{"out.mp4"}, ExitCode: 0}, nil
}
func (okBackend) ExportProject(context.Context, backend.ExportRequest) (*backend.Result, error) {
	return &backend.Result{}, nil
}
func (okBackend) ExportSTMap(context.Context, backend.STMapRequest) (*backend.Result, error) {
	return &backend.Result{}, nil
}
func (okBackend) ProbeMetadata(context.Context, string, backend.ProbeOptions) (*backend.MetadataResult, error) {
	return &backend.MetadataResult{
		Summary: backend.MetadataSummary{
			Camera:         backend.CameraInfo{Make: "GoPro", Model: "HERO10 Black", Identifier: "gopro-hero10black-wide-1920x1080@59940-no-eis"},
			DetectedSource: "GoPro HERO10 Black",
			Lens:           "Wide",
			HasGyro:        true,
			Width:          1920,
			Height:         1080,
			FPS:            59.94,
			DurationS:      23.1,
			FrameCount:     1384,
		},
		RawPath: "/tmp/gyroflow-meta.json",
	}, nil
}

func TestRenderStartThenStatus(t *testing.T) {
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	eng := jobengine.New(okBackend{}, 1)
	doc := func(_ context.Context) DoctorResult { return DoctorResult{Path: "/bin/gyroflow", Version: "1.6.3"} }
	RegisterTools(srv, eng, doc, nil)

	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, t1, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := c.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "render_start", Arguments: map[string]any{"inputs": []string{"clip.mp4"}},
	})
	if err != nil || res.IsError {
		t.Fatalf("render_start failed: %v %+v", err, res)
	}
	// structured output carries job_id
	out := res.StructuredContent.(map[string]any)
	jobID, _ := out["job_id"].(string)
	if jobID == "" {
		t.Fatalf("no job_id in %+v", out)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sr, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "render_status", Arguments: map[string]any{"job_id": jobID},
		})
		if err != nil {
			t.Fatal(err)
		}
		st := sr.StructuredContent.(map[string]any)
		if st["state"] == "completed" {
			// gyroflow's ETA is unreliable, so it must not reach clients.
			if _, hasETA := st["eta"]; hasETA {
				t.Fatalf("render_status must not expose 'eta': %+v", st)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("job never completed via render_status")
}

// TestRenderStartSendsNoProgressAfterResponding verifies that render_start
// ignores its progressToken: the request ends once the job is queued, and MCP
// only allows progress for active requests. The job must still report progress
// through render_status.
func TestRenderStartSendsNoProgressAfterResponding(t *testing.T) {
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	eng := jobengine.New(progressBackend{}, 1)
	doc := func(_ context.Context) DoctorResult { return DoctorResult{Path: "/bin/gyroflow", Version: "1.6.3"} }
	RegisterTools(srv, eng, doc, nil)

	got := make(chan struct{}, 1)

	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, _ *mcp.ProgressNotificationClientRequest) {
			select {
			case got <- struct{}{}:
			default:
			}
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
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "render_start",
		Arguments: map[string]any{"inputs": []string{"c.mp4"}},
		Meta:      mcp.Meta{"progressToken": "p1"},
	})
	if err != nil || res.IsError {
		t.Fatalf("render_start failed: %v %+v", err, res)
	}
	jobID, _ := res.StructuredContent.(map[string]any)["job_id"].(string)

	deadline := time.Now().Add(2 * time.Second)
	for {
		sr, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "render_status", Arguments: map[string]any{"job_id": jobID},
		})
		if err != nil {
			t.Fatal(err)
		}
		st := sr.StructuredContent.(map[string]any)
		if st["state"] == "completed" {
			if st["frame"] != float64(2) || st["total"] != float64(2) {
				t.Fatalf("job progress not recorded: %+v", st)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job never completed")
		}
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case <-got:
		t.Fatal("render_start sent a progress notification after responding")
	case <-time.After(100 * time.Millisecond):
	}
}

// TestDoctorTool verifies that gyroflow_doctor returns the DoctorResult
// produced by the injected DoctorFunc - stub returns sandboxed=true, Usable=false,
// and advice containing the brew DMG fix.
func TestDoctorTool(t *testing.T) {
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	eng := jobengine.New(okBackend{}, 1)
	doc := func(_ context.Context) DoctorResult {
		return DoctorResult{
			Found:     true,
			Path:      "/Applications/Gyroflow.app/Contents/MacOS/gyroflow",
			Version:   "1.6.3",
			Sandboxed: true,
			AppStore:  true,
			Usable:    false,
			Advice:    "install the DMG build via `brew install --cask gyroflow`",
		}
	}
	RegisterTools(srv, eng, doc, nil)

	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, t1, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := c.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "gyroflow_doctor"})
	if err != nil || res.IsError {
		t.Fatalf("gyroflow_doctor failed: %v %+v", err, res)
	}
	out := res.StructuredContent.(map[string]any)
	if out["sandboxed"] != true {
		t.Fatalf("expected sandboxed=true, got %+v", out)
	}
	if out["usable"] != false {
		t.Fatalf("expected usable=false for sandboxed build, got %+v", out)
	}
	advice, _ := out["advice"].(string)
	if !strings.Contains(advice, "brew install --cask gyroflow") {
		t.Fatalf("expected advice to contain brew fix, got %q", advice)
	}
}

// TestDoctorToolClean verifies gyroflow_doctor with a clean (non-sandboxed)
// build: Found=true, Usable=true, Sandboxed=false.
func TestDoctorToolClean(t *testing.T) {
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	eng := jobengine.New(okBackend{}, 1)
	doc := func(_ context.Context) DoctorResult {
		return DoctorResult{
			Found:     true,
			Path:      "/Applications/Gyroflow.app/Contents/MacOS/gyroflow",
			Version:   "1.6.3",
			Sandboxed: false,
			AppStore:  false,
			Usable:    true,
		}
	}
	RegisterTools(srv, eng, doc, nil)

	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, t1, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := c.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "gyroflow_doctor"})
	if err != nil || res.IsError {
		t.Fatalf("gyroflow_doctor failed: %v %+v", err, res)
	}
	out := res.StructuredContent.(map[string]any)
	if out["found"] != true {
		t.Fatalf("expected found=true, got %+v", out)
	}
	if out["usable"] != true {
		t.Fatalf("expected usable=true for clean build, got %+v", out)
	}
	if out["sandboxed"] != false {
		t.Fatalf("expected sandboxed=false, got %+v", out)
	}
}

// TestDoctorToolSandboxError verifies that when the sandbox check fails
// (codesign error), the DoctorFunc returns Found=true but Usable=false,
// and the advice mentions "could not verify" (not a misleading usable:true).
func TestDoctorToolSandboxError(t *testing.T) {
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	eng := jobengine.New(okBackend{}, 1)
	doc := func(_ context.Context) DoctorResult {
		return DoctorResult{
			Found:     true,
			Path:      "/Applications/Gyroflow.app/Contents/MacOS/gyroflow",
			Version:   "1.6.3",
			Sandboxed: false,
			Usable:    false, // sandboxErr != nil → cannot confirm usability
			Advice:    "could not verify code signature (codesign: exit 1) - if this is the App Store build the CLI will fail; prefer the DMG build",
		}
	}
	RegisterTools(srv, eng, doc, nil)

	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, t1, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := c.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "gyroflow_doctor"})
	if err != nil || res.IsError {
		t.Fatalf("gyroflow_doctor failed: %v %+v", err, res)
	}
	out := res.StructuredContent.(map[string]any)
	if out["found"] != true {
		t.Fatalf("expected found=true (binary was detected), got %+v", out)
	}
	if out["usable"] != false {
		t.Fatalf("expected usable=false when sandbox check errored, got %+v", out)
	}
	advice, _ := out["advice"].(string)
	if !strings.Contains(advice, "could not verify") {
		t.Fatalf("expected advice to mention 'could not verify', got %q", advice)
	}
}

// loadTestIndex builds a lens.Index from the committed sample fixtures.
func loadTestIndex(t *testing.T) *lens.Index {
	t.Helper()
	idx, err := lens.LoadFromDir("../../testdata/lens/sample_profiles")
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

// TestFindLensProfileTool verifies the find_lens_profile tool delegates to
// lens.Index.Search and returns structured hits.
func TestFindLensProfileTool(t *testing.T) {
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	eng := jobengine.New(okBackend{}, 1)
	doc := func(_ context.Context) DoctorResult { return DoctorResult{} }
	RegisterTools(srv, eng, doc, loadTestIndex(t))

	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, t1, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := c.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "find_lens_profile",
		Arguments: map[string]any{"query": "GoPro HERO11"},
	})
	if err != nil || res.IsError {
		t.Fatalf("find_lens_profile failed: %v %+v", err, res)
	}
	out := res.StructuredContent.(map[string]any)
	hits, _ := out["hits"].([]any)
	if len(hits) == 0 {
		t.Fatalf("expected non-empty hits for GoPro HERO11, got %+v", out)
	}
	first := hits[0].(map[string]any)
	if first["path"] == "" || first["path"] == nil {
		t.Fatalf("hit missing path: %+v", first)
	}
	if first["display"] == "" || first["display"] == nil {
		t.Fatalf("hit missing display: %+v", first)
	}
}

// TestFindLensProfileRankedAndAutoLoad verifies the ranked-search behavior: a
// camera+resolution+fps query returns a single ranked `best` (with the real
// pinned profile fields) and gyroflow_auto_loads=true + advice for an
// embedded-profile family; a non-embedded query yields no best and
// gyroflow_auto_loads=false.
func TestFindLensProfileRankedAndAutoLoad(t *testing.T) {
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	eng := jobengine.New(okBackend{}, 1)
	doc := func(_ context.Context) DoctorResult { return DoctorResult{} }
	RegisterTools(srv, eng, doc, loadTestIndex(t))

	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, t1, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := c.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	// GoPro HERO11 with the real fixture's resolution/fps → ranked best + auto-load.
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "find_lens_profile",
		Arguments: map[string]any{
			"query": "GoPro HERO11", "width": 5312, "height": 2988, "fps": 29.97,
		},
	})
	if err != nil || res.IsError {
		t.Fatalf("find_lens_profile failed: %v %+v", err, res)
	}
	out := res.StructuredContent.(map[string]any)
	best, ok := out["best"].(map[string]any)
	if !ok || best == nil {
		t.Fatalf("expected a non-null best, got %+v", out)
	}
	if best["brand"] != "GoPro" || best["model"] != "HERO11 Black" {
		t.Fatalf("best camera wrong: %+v", best)
	}
	if w, _ := best["width"].(float64); w != 5312 {
		t.Fatalf("best width = %v, want 5312 (real calib_dimension)", best["width"])
	}
	if al, _ := out["gyroflow_auto_loads"].(bool); !al {
		t.Fatalf("gyroflow_auto_loads = %v, want true for GoPro", out["gyroflow_auto_loads"])
	}
	if adv, _ := out["advice"].(string); adv == "" {
		t.Fatal("expected non-empty advice when gyroflow_auto_loads is true")
	}
	// Legacy hits shape still present and non-empty.
	if hits, _ := out["hits"].([]any); len(hits) == 0 {
		t.Fatalf("legacy hits must stay populated: %+v", out)
	}

	// Non-embedded camera with no fixture match → no best, auto_loads false.
	res2, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "find_lens_profile", Arguments: map[string]any{"query": "Nikon Z6"},
	})
	if err != nil || res2.IsError {
		t.Fatalf("find_lens_profile (Nikon) failed: %v %+v", err, res2)
	}
	out2 := res2.StructuredContent.(map[string]any)
	if out2["best"] != nil {
		t.Fatalf("expected null best for unmatched Nikon, got %+v", out2["best"])
	}
	if al, _ := out2["gyroflow_auto_loads"].(bool); al {
		t.Fatalf("gyroflow_auto_loads = true for Nikon, want false")
	}
	// I-2: a no-match + no-auto-load result MUST still give the agent a
	// concrete next step (broaden query / browse lens://profiles), not "".
	adv, _ := out2["advice"].(string)
	if adv == "" {
		t.Fatal("expected actionable advice on no-match, got empty")
	}
	if !strings.Contains(adv, "Nikon Z6") || !strings.Contains(adv, "lens://profiles") {
		t.Fatalf("no-match advice not actionable: %q", adv)
	}
}

// TestLensProfilesResource verifies the lens://profiles resource returns
// non-empty contents.
func TestLensProfilesResource(t *testing.T) {
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	RegisterResources(srv, loadTestIndex(t), okBackend{})

	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, t1, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := c.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	rr, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "lens://profiles"})
	if err != nil {
		t.Fatalf("ReadResource(lens://profiles): %v", err)
	}
	if len(rr.Contents) == 0 {
		t.Fatal("expected non-empty contents for lens://profiles")
	}
	if rr.Contents[0].Text == "" {
		t.Fatalf("expected non-empty text in lens://profiles content: %+v", rr.Contents[0])
	}
}

// TestProjectMetadataResourceMissingPath verifies a nonexistent path maps to a
// not-found error (ResourceNotFoundError) rather than a raw backend error.
func TestProjectMetadataResourceMissingPath(t *testing.T) {
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	RegisterResources(srv, loadTestIndex(t), okBackend{})

	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, t1, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := c.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	_, err = cs.ReadResource(ctx, &mcp.ReadResourceParams{
		URI: "project:///nonexistent/does/not/exist.mp4/metadata",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent path, got nil")
	}
	// Must be the structured not-found mapping, not a raw backend error.
	if !strings.Contains(strings.ToLower(err.Error()), "not found") {
		t.Fatalf("expected resource-not-found error, got: %v", err)
	}
}

// TestToolAnnotations verifies every registered tool carries the correct
// ToolAnnotations (readOnlyHint vs destructiveHint) so real MCP clients can
// drive auto-approve / confirm UX correctly.
func TestToolAnnotations(t *testing.T) {
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	eng := jobengine.New(okBackend{}, 1)
	doc := func(_ context.Context) DoctorResult { return DoctorResult{} }
	RegisterTools(srv, eng, doc, loadTestIndex(t))

	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, t1, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := c.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	lt, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]*mcp.Tool, len(lt.Tools))
	for _, tl := range lt.Tools {
		got[tl.Name] = tl
	}

	// name → wantReadOnly (true) | wantDestructive (false=readOnly path)
	wantReadOnly := map[string]bool{
		"render_status":     true,
		"render_wait":       true,
		"render_list":       true,
		"gyroflow_doctor":   true,
		"probe_metadata":    true,
		"find_lens_profile": true,
		"render_start":      false,
		"render_cancel":     false,
		"export_project":    false,
		"export_stmap":      false,
	}
	for name, ro := range wantReadOnly {
		tl, ok := got[name]
		if !ok {
			t.Fatalf("tool %q not registered", name)
		}
		if tl.Annotations == nil {
			t.Fatalf("tool %q has nil Annotations", name)
		}
		if ro {
			if !tl.Annotations.ReadOnlyHint {
				t.Errorf("tool %q: expected ReadOnlyHint=true", name)
			}
		} else {
			if tl.Annotations.ReadOnlyHint {
				t.Errorf("tool %q: expected ReadOnlyHint=false (destructive)", name)
			}
			if tl.Annotations.DestructiveHint == nil || !*tl.Annotations.DestructiveHint {
				t.Errorf("tool %q: expected DestructiveHint=true", name)
			}
		}
	}
}

// TestServerInstructionsDelivered verifies the server's ServerOptions.Instructions
// are actually transmitted over the wire and exposed to the client. The real
// accessor (go doc, go-sdk v1.6.0) is ClientSession.InitializeResult().Instructions
// (InitializeResult.Instructions string - the initialize-response field).
func TestServerInstructionsDelivered(t *testing.T) {
	ctx := context.Background()
	srv := mcp.NewServer(
		&mcp.Implementation{Name: "t", Version: "0"},
		&mcp.ServerOptions{Instructions: serverInstructions},
	)
	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, t1, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := c.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	ir := cs.InitializeResult()
	if ir == nil {
		t.Fatal("InitializeResult is nil")
	}
	got := ir.Instructions // wire-delivered value the client actually received
	if got == "" {
		t.Fatal("server did not deliver Instructions over the wire")
	}
	for _, must := range []string{"gyroflow_doctor", "render_start", "render_status", "async"} {
		if !strings.Contains(strings.ToLower(got), strings.ToLower(must)) {
			t.Errorf("delivered Instructions should mention %q; got: %s", must, got)
		}
	}
}

// TestStabilizeFootagePrompt verifies the stabilize-footage prompt returns a
// non-empty message whose body encodes the ordered tool flow and interpolates
// the path argument.
func TestStabilizeFootagePrompt(t *testing.T) {
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	RegisterPrompts(srv)

	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, t1, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := c.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	res, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{
		Name:      "stabilize-footage",
		Arguments: map[string]string{"path": "clip.mp4"},
	})
	if err != nil {
		t.Fatalf("GetPrompt(stabilize-footage): %v", err)
	}
	if len(res.Messages) == 0 {
		t.Fatal("expected non-empty Messages")
	}
	var body strings.Builder
	for _, m := range res.Messages {
		if tc, ok := m.Content.(*mcp.TextContent); ok {
			body.WriteString(tc.Text)
		}
	}
	text := body.String()
	// Assert the deterministic ordered flow appears, in order.
	flow := []string{"gyroflow_doctor", "probe_metadata", "find_lens_profile", "render_start", "render_status"}
	last := 0
	for _, tool := range flow {
		i := strings.Index(text[last:], tool)
		if i < 0 {
			t.Fatalf("prompt body missing %q in ordered flow; body=%s", tool, text)
		}
		last += i + len(tool)
	}
	if !strings.Contains(text, "clip.mp4") {
		t.Fatalf("prompt body should interpolate the path arg 'clip.mp4'; body=%s", text)
	}
	// The no-external-tools / no-raw_path instruction must be explicit.
	for _, must := range []string{"ffprobe", "jq", "raw_path", "summary"} {
		if !strings.Contains(text, must) {
			t.Fatalf("prompt body must steer away from external tools; missing %q; body=%s", must, text)
		}
	}
	if !strings.Contains(text, "Do NOT shell out") {
		t.Fatalf("prompt body must explicitly forbid shelling out; body=%s", text)
	}
	// The has_gyro honest-stop boundary must be present.
	if !strings.Contains(text, "summary.has_gyro") {
		t.Fatalf("prompt body must read summary.has_gyro and stop when false; body=%s", text)
	}
	// The gyroflow_auto_loads decision branch must be described.
	if !strings.Contains(text, "gyroflow_auto_loads") {
		t.Fatalf("prompt body must describe the gyroflow_auto_loads branch; body=%s", text)
	}
	if !strings.Contains(text, "best.path") {
		t.Fatalf("prompt body must encode the best.path preset decision; body=%s", text)
	}
	// #2: step 4 must be DECISION-ONLY (no render_start there) and step 5 must
	// be the SOLE render_start invocation, to prevent an LLM double-call.
	if !strings.Contains(text, "do NOT call render_start\n   yet") &&
		!strings.Contains(text, "do NOT call render_start") {
		t.Fatalf("step 4 must be decision-only ('do NOT call render_start yet'); body=%s", text)
	}
	if !strings.Contains(text, "call render_start ONCE") {
		t.Fatalf("step 5 must be the single render_start invocation ('call render_start ONCE'); body=%s", text)
	}
	if !strings.Contains(text, "render_start ONCE") || !strings.Contains(text, "ONLY render_start call") {
		t.Fatalf("prompt must state render_start is called exactly once; body=%s", text)
	}
	// The terminal cancelled clause must be present.
	if !strings.Contains(text, "cancelled: report") {
		t.Fatalf("prompt body must handle the 'cancelled' terminal state; body=%s", text)
	}
	// Belt-and-suspenders: outputs-absent fallback wording present.
	if !strings.Contains(text, "outputs\" is absent") {
		t.Fatalf("prompt body must give the outputs-absent fallback; body=%s", text)
	}
}

// bulkRawBackend's ProbeMetadata writes a realistic multi-element raw export
// to a temp file and returns ONLY a compact summary + that path. It exists to
// prove probe_metadata never inlines the bulk telemetry into the MCP response
// (so the caller never has to parse the raw multi-MB export directly).
type bulkRawBackend struct{ rawPath string }

func (bulkRawBackend) Stabilize(context.Context, backend.StabilizeRequest, backend.ProgressFunc) (*backend.Result, error) {
	return &backend.Result{}, nil
}
func (bulkRawBackend) ExportProject(context.Context, backend.ExportRequest) (*backend.Result, error) {
	return &backend.Result{}, nil
}
func (bulkRawBackend) ExportSTMap(context.Context, backend.STMapRequest) (*backend.Result, error) {
	return &backend.Result{}, nil
}
func (b *bulkRawBackend) ProbeMetadata(context.Context, string, backend.ProbeOptions) (*backend.MetadataResult, error) {
	// Build a bulky raw_imu/quaternions payload like a real type-2 export.
	var imu []map[string]any
	for i := range 4000 {
		imu = append(imu, map[string]any{"timestamp_ms": float64(i) * 5, "gyro": []float64{1, 2, 3}})
	}
	raw, _ := json.Marshal(map[string]any{"raw_imu": imu})
	if err := os.WriteFile(b.rawPath, raw, 0o600); err != nil {
		return nil, err
	}
	return &backend.MetadataResult{
		Summary: backend.MetadataSummary{
			Camera:         backend.CameraInfo{Make: "GoPro", Model: "HERO10 Black", Identifier: "gopro-hero10black-wide-1920x1080@59940-no-eis"},
			DetectedSource: "GoPro HERO10 Black",
			Lens:           "Wide",
			HasGyro:        true,
			Width:          1920,
			Height:         1080,
			FPS:            59.94,
			DurationS:      23.1,
			FrameCount:     1384,
		},
		RawPath: b.rawPath,
	}, nil
}

// TestProbeMetadataCompactNoRawDump asserts probe_metadata returns the compact
// structured object with a non-empty raw_path and does NOT inline the bulk
// telemetry arrays - the raw blob must stay on disk at raw_path.
func TestProbeMetadataCompactNoRawDump(t *testing.T) {
	ctx := context.Background()
	rawPath := t.TempDir() + "/raw.json"
	be := &bulkRawBackend{rawPath: rawPath}

	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	eng := jobengine.New(be, 1)
	doc := func(_ context.Context) DoctorResult { return DoctorResult{Path: "/bin/gyroflow"} }
	RegisterTools(srv, eng, doc, nil)

	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, t1, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := c.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "probe_metadata", Arguments: map[string]any{"input": "clip.mp4"},
	})
	if err != nil || res.IsError {
		t.Fatalf("probe_metadata failed: %v %+v", err, res)
	}

	out := res.StructuredContent.(map[string]any)
	// Unified shape: {summary:{…}, raw_path} - same as project://{path}/metadata.
	summary := out["summary"].(map[string]any)
	cam := summary["camera"].(map[string]any)
	if cam["make"] != "GoPro" || cam["model"] != "HERO10 Black" {
		t.Fatalf("camera not surfaced: %+v", out)
	}
	if summary["has_gyro"] != true {
		t.Errorf("has_gyro = %v, want true", summary["has_gyro"])
	}
	if fc, _ := summary["frame_count"].(float64); fc != 1384 {
		t.Errorf("frame_count = %v, want 1384", summary["frame_count"])
	}
	rp, _ := out["raw_path"].(string)
	if rp == "" {
		t.Fatalf("raw_path empty; want the on-disk export path")
	}
	if _, ok := out["raw_imu"]; ok {
		t.Errorf("response inlined raw_imu; bulk must stay on disk")
	}

	// The entire serialized response must NOT contain the bulk arrays.
	var body strings.Builder
	for _, m := range res.Content {
		if tc, ok := m.(*mcp.TextContent); ok {
			body.WriteString(tc.Text)
		}
	}
	full, _ := json.Marshal(out)
	body.Write(full)
	if strings.Contains(body.String(), "raw_imu") {
		t.Fatalf("MCP response contains bulk 'raw_imu'; it must only be at raw_path")
	}
	if body.Len() > 4096 {
		t.Fatalf("probe_metadata response too large (%d bytes) - likely inlined raw", body.Len())
	}

	// Sanity: the bulk really IS on disk at raw_path (proving it was not lost,
	// just not inlined).
	onDisk, err := os.ReadFile(rp)
	if err != nil {
		t.Fatalf("reading raw_path %s: %v", rp, err)
	}
	if !strings.Contains(string(onDisk), "raw_imu") {
		t.Fatalf("raw_path file missing the bulk export")
	}
}

// TestAllPromptsRegistered verifies all three prompts are listed and return
// non-empty messages.
func TestAllPromptsRegistered(t *testing.T) {
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	RegisterPrompts(srv)

	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, t1, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := c.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	for _, name := range []string{"stabilize-footage", "batch-stabilize", "diagnose"} {
		res, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: name})
		if err != nil {
			t.Fatalf("GetPrompt(%s): %v", name, err)
		}
		if len(res.Messages) == 0 {
			t.Fatalf("prompt %s returned no messages", name)
		}
	}
}
