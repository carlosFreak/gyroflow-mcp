package mcpsurface

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
	"github.com/carlosFreak/gyroflow-mcp/internal/jobengine"
	"github.com/carlosFreak/gyroflow-mcp/internal/lut"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const identityCube = "LUT_3D_SIZE 2\n0 0 0\n1 0 0\n0 1 0\n1 1 0\n0 0 1\n1 0 1\n0 1 1\n1 1 1\n"

// djiBackend reports every clip as a DJI O4P.
type djiBackend struct{ recordingBackend }

func (djiBackend) ProbeMetadata(context.Context, string, backend.ProbeOptions) (*backend.MetadataResult, error) {
	return &backend.MetadataResult{Summary: backend.MetadataSummary{Camera: backend.CameraInfo{Make: "DJI", Model: "O4P"}}}, nil
}

// startWithLUTs is startWithForm with LUTs on. args always carry config: {} so
// only the LUT question can be asked.
func startWithLUTs(t *testing.T, be backend.Backend, reqs chan backend.StabilizeRequest, lib *lut.Library, args map[string]any,
	handle func(*mcp.ElicitRequest) (*mcp.ElicitResult, error)) (*mcp.CallToolResult, backend.StabilizeRequest, []*mcp.ElicitRequest) {
	t.Helper()
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	RegisterTools(srv, jobengine.New(be, 1), func(context.Context) DoctorResult { return DoctorResult{} }, nil, WithLUTs(lib))

	var forms []*mcp.ElicitRequest
	opts := &mcp.ClientOptions{}
	if handle != nil {
		opts.ElicitationHandler = func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			forms = append(forms, req)
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

	args["config"] = map[string]any{}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "render_start", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		return res, backend.StabilizeRequest{}, forms
	}
	select {
	case r := <-reqs:
		return res, r, forms
	case <-time.After(2 * time.Second):
		t.Fatal("render never reached the backend")
	}
	return nil, backend.StabilizeRequest{}, forms
}

func newDJI() (djiBackend, chan backend.StabilizeRequest) {
	reqs := make(chan backend.StabilizeRequest, 1)
	return djiBackend{recordingBackend{reqs: reqs}}, reqs
}

func errText(res *mcp.CallToolResult) string { return res.Content[0].(*mcp.TextContent).Text }

func TestLUTQuestionWithEmptyFolderOffersDownloadAndOwn(t *testing.T) {
	be, reqs := newDJI()
	lib := &lut.Library{Dir: filepath.Join(t.TempDir(), "LUTs")}
	res, _, _ := startWithLUTs(t, be, reqs, lib, map[string]any{"inputs": []string{"c.mp4"}}, nil)
	if !res.IsError {
		t.Fatal("a client without forms must be told to ask")
	}
	text := errText(res)
	for _, want := range []string{"Ask the user", "o4-air-unit", "no LUTs saved", "<path to the .cube>", `lut: "none"`} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "suggested") {
		t.Errorf("must not suggest a LUT that does not exist:\n%s", text)
	}
}

func TestLUTQuestionNonDJIHasNoDownloadLink(t *testing.T) {
	reqs := make(chan backend.StabilizeRequest, 1)
	be := recordingBackend{reqs: reqs} // okBackend probes as a GoPro
	lib := &lut.Library{Dir: t.TempDir()}
	res, _, _ := startWithLUTs(t, be, reqs, lib, map[string]any{"inputs": []string{"c.mp4"}}, nil)
	if text := errText(res); strings.Contains(text, "dji.com") {
		t.Fatalf("non-DJI camera got a DJI link:\n%s", text)
	}
}

func TestLUTFormSuggestsCameraLUT(t *testing.T) {
	be, reqs := newDJI()
	lib := &lut.Library{Dir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(lib.Dir, "DJI O4 D-Log M to Rec709.cube"), []byte(identityCube), 0o644); err != nil {
		t.Fatal(err)
	}
	res, req, forms := startWithLUTs(t, be, reqs, lib, map[string]any{"inputs": []string{"c.mp4"}},
		func(r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			enum := r.Params.RequestedSchema.(map[string]any)["properties"].(map[string]any)["lut"].(map[string]any)["enum"].([]any)
			return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"lut": enum[0]}}, nil
		})
	if res.IsError {
		t.Fatalf("render_start failed: %s", errText(res))
	}
	enum := forms[0].Params.RequestedSchema.(map[string]any)["properties"].(map[string]any)["lut"].(map[string]any)["enum"].([]any)
	if enum[0] != "DJI O4 D-Log M to Rec709 (suggested for this clip)" || enum[len(enum)-1] != lutNone {
		t.Fatalf("choices = %v", enum)
	}
	if filepath.Base(req.LUT) != "DJI O4 D-Log M to Rec709.cube" {
		t.Fatalf("backend LUT = %q", req.LUT)
	}
	if out := res.StructuredContent.(map[string]any); out["lut"] != "DJI O4 D-Log M to Rec709" {
		t.Fatalf("lut not returned for the batch: %+v", out)
	}
}

func TestLUTFormNoLUT(t *testing.T) {
	be, reqs := newDJI()
	res, req, _ := startWithLUTs(t, be, reqs, &lut.Library{Dir: t.TempDir()}, map[string]any{"inputs": []string{"c.mp4"}},
		func(*mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"lut": lutNone}}, nil
		})
	if res.IsError || req.LUT != "" {
		t.Fatalf("got %+v, LUT %q", res, req.LUT)
	}
}

func TestLUTOwnPathIsSavedToFolder(t *testing.T) {
	be, reqs := newDJI()
	lib := &lut.Library{Dir: filepath.Join(t.TempDir(), "LUTs")}
	mine := filepath.Join(t.TempDir(), "My Look.cube")
	if err := os.WriteFile(mine, []byte(identityCube), 0o644); err != nil {
		t.Fatal(err)
	}
	res, req, forms := startWithLUTs(t, be, reqs, lib, map[string]any{"inputs": []string{"c.mp4"}, "lut": mine}, nil)
	if res.IsError {
		t.Fatalf("render_start failed: %s", errText(res))
	}
	if len(forms) != 0 {
		t.Fatal("a given lut must not be asked again")
	}
	if req.LUT != filepath.Join(lib.Dir, "My Look.cube") {
		t.Fatalf("backend LUT = %q", req.LUT)
	}
	entries, _ := lib.List()
	if len(entries) != 1 || len(entries[0].Cameras) != 1 || entries[0].Cameras[0] != "DJI O4P" {
		t.Fatalf("library = %+v", entries)
	}
}

func TestLUTBrokenFileFailsBeforeRender(t *testing.T) {
	be, reqs := newDJI()
	bad := filepath.Join(t.TempDir(), "bad.cube")
	if err := os.WriteFile(bad, []byte("LUT_3D_SIZE 2\n0 0 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, _, _ := startWithLUTs(t, be, reqs, &lut.Library{Dir: t.TempDir()}, map[string]any{"inputs": []string{"c.mp4"}, "lut": bad}, nil)
	if !res.IsError || !strings.Contains(errText(res), "table rows") {
		t.Fatalf("expected a validation error, got %+v", res)
	}
}

func TestRatioName(t *testing.T) {
	for size, want := range map[[2]int]string{
		{2880, 2880}: "1x1", {3840, 2160}: "16x9", {1620, 2880}: "9x16", {3840, 2880}: "4x3",
		{2304, 2880}: "4x5", {1920, 1080}: "16x9", {1919, 1080}: "16x9", {1000, 300}: "1000x300",
	} {
		if got := ratioName(size[0], size[1]); got != want {
			t.Errorf("ratioName(%v) = %q, want %q", size, got, want)
		}
	}
}

func TestSuffixGetsAspectRatio(t *testing.T) {
	preset := filepath.Join(t.TempDir(), "1-1.gyroflow")
	if err := os.WriteFile(preset, []byte(`{"output":{"output_width":2880,"output_height":2880}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"preset size":      {map[string]any{"preset_path": preset}, "_stabilized_1x1"},
		"custom suffix":    {map[string]any{"preset_path": preset, "suffix": "_test"}, "_test_1x1"},
		"already named":    {map[string]any{"preset_path": preset, "suffix": "_stabilized_1x1"}, "_stabilized_1x1"},
		"aspect_ratio":     {map[string]any{"config": map[string]any{"output": map[string]any{"aspect_ratio": "9:16"}}}, "_stabilized_9x16"},
		"source unchanged": {map[string]any{"config": map[string]any{}}, "_stabilized_16x9"}, // okBackend probes 1920x1080
	} {
		tc.args["inputs"] = []string{"c.mp4"}
		res, req, _ := startWithForm(t, tc.args, nil)
		if res.IsError {
			t.Fatalf("%s: %s", name, errText(res))
		}
		if req.Suffix != tc.want {
			t.Errorf("%s: suffix = %q, want %q", name, req.Suffix, tc.want)
		}
	}
}

func TestDoctorReportsFFmpeg(t *testing.T) {
	off := DoctorResult{Advice: "ok."}.WithFFmpeg("")
	if off.LUTs || !strings.Contains(off.Advice, "brew install ffmpeg") || !strings.HasPrefix(off.Advice, "ok. ") {
		t.Fatalf("without ffmpeg: %+v", off)
	}
	on := DoctorResult{}.WithFFmpeg("/opt/homebrew/bin/ffmpeg")
	if !on.LUTs || on.Advice != "" {
		t.Fatalf("with ffmpeg: %+v", on)
	}
}
