package mcpsurface

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
	"github.com/carlosFreak/gyroflow-mcp/internal/jobengine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// recordingBackend is okBackend that hands each StabilizeRequest to reqs.
type recordingBackend struct {
	okBackend
	reqs chan backend.StabilizeRequest
}

func (b recordingBackend) Stabilize(ctx context.Context, r backend.StabilizeRequest, p backend.ProgressFunc) (*backend.Result, error) {
	b.reqs <- r
	return b.okBackend.Stabilize(ctx, r, p)
}

// startWithForm calls render_start with args from a client whose form handler
// is handle (nil = client without elicitation). It returns the tool result and
// the request the backend received (zero when no job ran).
func startWithForm(t *testing.T, args map[string]any, handle func(*mcp.ElicitRequest) (*mcp.ElicitResult, error)) (*mcp.CallToolResult, backend.StabilizeRequest, int) {
	t.Helper()
	ctx := context.Background()
	be := recordingBackend{reqs: make(chan backend.StabilizeRequest, 1)}
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	doc := func(_ context.Context) DoctorResult { return DoctorResult{} }
	RegisterTools(srv, jobengine.New(be, 1), doc, nil)

	forms := 0
	opts := &mcp.ClientOptions{}
	if handle != nil {
		opts.ElicitationHandler = func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			forms++
			return handle(req)
		}
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, opts)
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, t1, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := c.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "render_start", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		return res, backend.StabilizeRequest{}, forms
	}
	select {
	case r := <-be.reqs:
		return res, r, forms
	case <-time.After(2 * time.Second):
		t.Fatal("render never reached the backend")
	}
	return nil, backend.StabilizeRequest{}, forms
}

func TestRenderStartAsksForSettingsWithoutConfig(t *testing.T) {
	var schema map[string]any
	res, req, forms := startWithForm(t, map[string]any{"inputs": []string{"c.mp4"}}, func(r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		schema, _ = r.Params.RequestedSchema.(map[string]any)
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{
			"smoothing": "Strong - gimbal-like",
			"aspect":    "1:1 - square",
			"crop":      "Static zoom - fixed crop, less zoom",
			"horizon":   "Full - always level",
			"output":    "H.265/HEVC - smaller files",
		}}, nil
	})
	if res.IsError || forms != 1 {
		t.Fatalf("want one form and a render, got forms=%d res=%+v", forms, res)
	}
	props, _ := schema["properties"].(map[string]any)
	for _, k := range []string{"smoothing", "aspect", "crop", "horizon", "output"} {
		if _, ok := props[k]; !ok {
			t.Errorf("form schema missing %q: %+v", k, schema)
		}
	}
	c := req.Config
	if c == nil || c.Smoothing == nil || *c.Smoothing.Smoothness != 0.85 ||
		c.Zoom == nil || *c.Zoom.AdaptiveZoomWindow != -1 ||
		c.HorizonLock == nil || *c.HorizonLock.Amount != 100 ||
		c.Output == nil || *c.Output.Codec != "H.265/HEVC" {
		t.Fatalf("answers not applied: %+v", c)
	}
	// okBackend probes 1920x1080, so 1:1 is the centred 1080x1080 frame.
	if c.Output.AspectRatio != nil || c.Output.OutputWidth == nil || *c.Output.OutputWidth != 1080 || *c.Output.OutputHeight != 1080 {
		t.Fatalf("aspect ratio not resolved to 1080x1080: %+v", c.Output)
	}
	out, _ := res.StructuredContent.(map[string]any)
	cfg, ok := out["config"].(map[string]any)
	if !ok {
		t.Fatalf("render_start must return the chosen config for batch reuse: %+v", out)
	}
	// The echoed config keeps the ratio, not the size, so it fits other resolutions.
	if o, _ := cfg["output"].(map[string]any); o["aspect_ratio"] != "1:1" || o["output_width"] != nil {
		t.Fatalf("echoed config should carry aspect_ratio only: %+v", cfg)
	}
}

func TestRenderStartAspectRatioRejectsWithSize(t *testing.T) {
	res, _, _ := startWithForm(t, map[string]any{"inputs": []string{"c.mp4"}, "config": map[string]any{
		"output": map[string]any{"aspect_ratio": "16:9", "output_width": 1920},
	}}, nil)
	if !res.IsError {
		t.Fatalf("aspect_ratio with output_width must be rejected: %+v", res)
	}
}

func TestRenderStartSkipsFormWhenConfigGiven(t *testing.T) {
	res, _, forms := startWithForm(t, map[string]any{"inputs": []string{"c.mp4"}, "config": map[string]any{}},
		func(*mcp.ElicitRequest) (*mcp.ElicitResult, error) { return &mcp.ElicitResult{Action: "cancel"}, nil })
	if res.IsError || forms != 0 {
		t.Fatalf("config given: want no form, got forms=%d res=%+v", forms, res)
	}
	if out, _ := res.StructuredContent.(map[string]any); out["config"] != nil {
		t.Fatalf("config must only be echoed when it came from the form: %+v", out)
	}
}

func TestRenderStartWithoutFormSupportRendersDefaults(t *testing.T) {
	res, req, _ := startWithForm(t, map[string]any{"inputs": []string{"c.mp4"}}, nil)
	if res.IsError || req.Config != nil {
		t.Fatalf("want a default render, got config=%+v res=%+v", req.Config, res)
	}
}

// Some clients decline or cancel every form without showing it, so an
// unanswered form must make the agent ask in chat instead of rendering defaults.
func TestRenderStartUnansweredFormAsksAgent(t *testing.T) {
	for name, result := range map[string]*mcp.ElicitResult{
		"decline":      {Action: "decline"},
		"cancel":       {Action: "cancel"},
		"empty accept": {Action: "accept"},
	} {
		res, _, _ := startWithForm(t, map[string]any{"inputs": []string{"c.mp4"}},
			func(*mcp.ElicitRequest) (*mcp.ElicitResult, error) { return result, nil })
		if !res.IsError {
			t.Fatalf("%s: must not render, got %+v", name, res)
		}
		text := res.Content[0].(*mcp.TextContent).Text
		for _, want := range []string{"Ask the user", "config: {}", `"horizon_lock":{"amount":100}`, `"codec":"ProRes"`} {
			if !strings.Contains(text, want) {
				t.Errorf("%s: error should contain %q, got:\n%s", name, want, text)
			}
		}
	}
}

func TestConfigFromAnswersDefaultsIsNil(t *testing.T) {
	answers := map[string]any{}
	for _, q := range settingsQuestions {
		answers[q.key] = q.choices[0].label
	}
	cfg, err := configFromAnswers(answers)
	if err != nil || cfg != nil {
		t.Fatalf("all recommended answers must keep Gyroflow defaults, got %+v %v", cfg, err)
	}
	if _, err := configFromAnswers(map[string]any{"crop": "bogus"}); err == nil {
		t.Fatal("unknown answer must be rejected")
	}
}

// Every non-default choice must pass the same validation as agent configs.
func TestSettingsChoicesAreValid(t *testing.T) {
	for _, q := range settingsQuestions {
		for _, c := range q.choices {
			if c.apply == nil {
				continue
			}
			cfg := &backend.GyroflowConfig{}
			c.apply(cfg)
			if err := backend.ValidateGyroflowConfig(cfg); err != nil {
				t.Errorf("%s / %s: %v", q.key, c.label, err)
			}
		}
	}
}
