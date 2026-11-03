package mcpsurface

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
	"github.com/carlosFreak/gyroflow-mcp/internal/jobengine"
	"github.com/carlosFreak/gyroflow-mcp/internal/lut"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestResolveColorMergesLook(t *testing.T) {
	lib := &lut.Library{Dir: t.TempDir()}
	if err := lib.SaveLook("sol", backend.ColorAdjust{Saturation: 0.2, Warmth: 0.3}); err != nil {
		t.Fatal(err)
	}
	c, err := resolveColor(lib, "sol", &backend.ColorAdjust{Warmth: 0.5, Sharpness: 0.2})
	if err != nil || *c != (backend.ColorAdjust{Saturation: 0.2, Warmth: 0.5, Sharpness: 0.2}) {
		t.Fatalf("merged = %+v, %v", c, err)
	}
	if c, err := resolveColor(lib, "", &backend.ColorAdjust{}); c != nil || err != nil {
		t.Fatalf("empty adjust = %+v, %v", c, err)
	}
	if _, err := resolveColor(lib, "", &backend.ColorAdjust{Contrast: 3}); err == nil {
		t.Fatal("out-of-range contrast accepted")
	}
	if _, err := resolveColor(lib, "missing", nil); err == nil {
		t.Fatal("unknown look accepted")
	}
}

func TestRenderStartPassesColor(t *testing.T) {
	be, reqs := newDJI()
	res, req, _ := startWithLUTs(t, be, reqs, &lut.Library{Dir: t.TempDir()},
		map[string]any{"inputs": []string{"c.mp4"}, "lut": "none", "color": map[string]any{"saturation": 0.2}}, nil)
	if res.IsError {
		t.Fatalf("render_start failed: %s", errText(res))
	}
	if req.Color == nil || req.Color.Saturation != 0.2 || req.LUT != "" {
		t.Fatalf("request color %+v lut %q", req.Color, req.LUT)
	}
}

// fakePreviewer writes a tiny file instead of a real frame.
type fakePreviewer struct{ djiBackend }

func (fakePreviewer) Preview(_ context.Context, _ string, _ float64, _ string, _ *backend.ColorAdjust, out string) error {
	return os.WriteFile(out, []byte("\xff\xd8jpeg"), 0o644)
}

func TestPreviewColorReturnsImage(t *testing.T) {
	ctx := context.Background()
	t.Setenv("TMPDIR", t.TempDir()) // previews go to os.TempDir
	dji, _ := newDJI()
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	RegisterTools(srv, jobengine.New(fakePreviewer{dji}, 1), func(context.Context) DoctorResult { return DoctorResult{} }, nil,
		WithLUTs(&lut.Library{Dir: t.TempDir()}))
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, t1, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "preview_color", Arguments: map[string]any{
		"input": filepath.Join(t.TempDir(), "clip.mp4"), "color": map[string]any{"warmth": 0.4},
	}})
	if err != nil || res.IsError {
		t.Fatalf("preview_color: %v %+v", err, res)
	}
	if img, ok := res.Content[0].(*mcp.ImageContent); !ok || img.MIMEType != "image/jpeg" {
		t.Fatalf("first content = %#v", res.Content[0])
	}
}
