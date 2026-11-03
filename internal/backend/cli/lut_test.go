package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
)

const identityCube = "LUT_3D_SIZE 2\n0 0 0\n1 0 0\n0 1 0\n1 1 0\n0 0 1\n1 0 1\n0 1 1\n1 1 1\n"

// fakeRender stands in for gyroflow: it writes a short real clip where gyroflow
// would put the output and records the request.
type fakeRender struct {
	backend.Backend
	ffmpeg string
	got    *backend.StabilizeRequest
}

func (f fakeRender) Stabilize(ctx context.Context, req backend.StabilizeRequest, p backend.ProgressFunc) (*backend.Result, error) {
	*f.got = req
	var outs []string
	for _, in := range req.Inputs {
		out := filepath.Join(filepath.Dir(in), stem(filepath.Base(in))+req.Suffix+".mp4")
		cmd := exec.CommandContext(ctx, f.ffmpeg, "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc=size=128x72:rate=30:duration=1",
			"-c:v", "libx264", "-pix_fmt", "yuv420p", out)
		if b, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("%v: %s", err, b)
		}
		outs = append(outs, out)
	}
	if p != nil {
		p(backend.Progress{Percent: 100, Stage: "render"})
	}
	return &backend.Result{OutputPaths: outs}, nil
}

func lutFixture(t *testing.T) (ffmpeg, dir, clip, cube string) {
	t.Helper()
	ffmpeg = FindFFmpeg()
	if ffmpeg == "" {
		t.Skip("ffmpeg not installed")
	}
	dir = t.TempDir()
	clip = filepath.Join(dir, "my clip.MP4")
	if err := os.WriteFile(clip, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	cube = filepath.Join(dir, "look: 'odd' name.cube")
	if err := os.WriteFile(cube, []byte(identityCube), 0o644); err != nil {
		t.Fatal(err)
	}
	return ffmpeg, dir, clip, cube
}

func TestLUTBackendAppliesLUTAndCleansUp(t *testing.T) {
	ffmpeg, dir, clip, cube := lutFixture(t)
	var got backend.StabilizeRequest
	b := NewLUT(fakeRender{ffmpeg: ffmpeg, got: &got}, ffmpeg)

	codec, br := "H.264/AVC", 5.0
	var stages []string
	res, err := b.Stabilize(context.Background(), backend.StabilizeRequest{
		Inputs: []string{clip}, LUT: cube,
		Config: &backend.GyroflowConfig{Output: &backend.OutputConfig{Codec: &codec, Bitrate: &br}},
	}, func(p backend.Progress) { stages = append(stages, p.Stage) })
	if err != nil {
		t.Fatal(err)
	}

	if got.LUT != "" || got.Suffix != "_stabilized_lutsrc" || !got.Overwrite {
		t.Fatalf("intermediate request = %+v", got)
	}
	if o := got.Config.Output; *o.Codec != "H.265/HEVC" || *o.Bitrate != lutIntermediateBitrate {
		t.Fatalf("intermediate output = %s %v", *o.Codec, *o.Bitrate)
	}
	want := filepath.Join(dir, "my clip_stabilized.mp4")
	if len(res.OutputPaths) != 1 || res.OutputPaths[0] != want {
		t.Fatalf("outputs = %v, want %s", res.OutputPaths, want)
	}
	if out, err := exec.Command(filepath.Join(filepath.Dir(ffmpeg), "ffprobe"), "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name", "-of", "csv=p=0", want).Output(); err != nil || strings.TrimSpace(string(out)) != "h264" {
		t.Fatalf("final codec = %q, %v", out, err)
	}
	left, _ := filepath.Glob(filepath.Join(dir, "*lutsrc*"))
	partial, _ := filepath.Glob(filepath.Join(dir, "*.partial*"))
	if len(left)+len(partial) != 0 {
		t.Fatalf("temporary files left: %v %v", left, partial)
	}
	if stages[len(stages)-1] != "lut" {
		t.Fatalf("no LUT progress reported: %v", stages)
	}
}

func TestLUTBackendRefusesExistingOutput(t *testing.T) {
	ffmpeg, dir, clip, cube := lutFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "my clip_stabilized.mov"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	var got backend.StabilizeRequest
	codec := "ProRes"
	_, err := NewLUT(fakeRender{ffmpeg: ffmpeg, got: &got}, ffmpeg).Stabilize(context.Background(), backend.StabilizeRequest{
		Inputs: []string{clip}, LUT: cube, Config: &backend.GyroflowConfig{Output: &backend.OutputConfig{Codec: &codec}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "already exists") || got.Inputs != nil {
		t.Fatalf("err = %v, rendered = %v", err, got.Inputs != nil)
	}
}

func TestLUTBackendWithoutFFmpeg(t *testing.T) {
	var got backend.StabilizeRequest
	_, err := NewLUT(fakeRender{got: &got}, "").Stabilize(context.Background(), backend.StabilizeRequest{Inputs: []string{"c.mp4"}, LUT: "x.cube"}, nil)
	if err == nil || !strings.Contains(err.Error(), "brew install ffmpeg") {
		t.Fatalf("err = %v", err)
	}
}

func TestLUTBackendRejectsUnsupportedCodec(t *testing.T) {
	if _, err := encoderFor("DNxHD", 100); err == nil {
		t.Fatal("expected an error")
	}
}
