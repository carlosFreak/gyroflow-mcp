package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
)

// fakeGyroflow writes a script that records its args and renders like the real
// CLI: "<output_folder><stem><suffix>.mp4", printing "Rendering failed" (exit 0)
// when the input is not inside allowedDir, as the sandbox would.
func fakeGyroflow(t *testing.T, dir, allowedDir string) (bin, argsLog string) {
	t.Helper()
	bin = filepath.Join(dir, "gyroflow")
	argsLog = filepath.Join(dir, "args.log")
	script := `#!/bin/sh
printf '%s\n' "$@" > '` + argsLog + `'
input="$1"
case "$input" in
  '` + allowedDir + `'/*) ;;
  *) echo "Rendering failed: sandbox"; exit 0 ;;
esac
suffix=_stabilized
folder=
while [ $# -gt 0 ]; do
  case "$1" in
    -t) suffix="$2"; shift ;;
    -p) folder=$(printf '%s' "$2" | sed -n 's/.*"output_folder":"\([^"]*\)".*/\1/p'); shift ;;
  esac
  shift
done
base=$(basename "$input"); base="${base%.*}"
printf rendered > "$folder$base$suffix.mp4"
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsLog
}

func newStagingFixture(t *testing.T) (s *StagingBackend, clip, argsLog string) {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(tmp, "root")
	bin, argsLog := fakeGyroflow(t, tmp, root)
	videos := filepath.Join(tmp, "my videos")
	if err := os.Mkdir(videos, 0o755); err != nil {
		t.Fatal(err)
	}
	clip = filepath.Join(videos, "clip.MP4")
	if err := os.WriteFile(clip, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	return NewStaging(bin, root), clip, argsLog
}

func jobDirs(t *testing.T, root string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(root, "job-*"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestStagingMovesOutputNextToInputAndCleansUp(t *testing.T) {
	s, clip, _ := newStagingFixture(t)
	res, err := s.Stabilize(context.Background(), backend.StabilizeRequest{Inputs: []string{clip}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(clip), "clip_stabilized.mp4")
	if len(res.OutputPaths) != 1 || res.OutputPaths[0] != want {
		t.Fatalf("OutputPaths = %v, want %s", res.OutputPaths, want)
	}
	if b, err := os.ReadFile(want); err != nil || string(b) != "rendered" {
		t.Fatalf("output: %q, %v", b, err)
	}
	if b, _ := os.ReadFile(clip); string(b) != "video" {
		t.Fatalf("original changed: %q", b)
	}
	if d := jobDirs(t, s.root); len(d) != 0 {
		t.Fatalf("job dirs left behind: %v", d)
	}
}

func TestStagingUsesRequestedOutputFolder(t *testing.T) {
	s, clip, _ := newStagingFixture(t)
	dest := t.TempDir()
	res, err := s.Stabilize(context.Background(), backend.StabilizeRequest{
		Inputs:    []string{clip},
		Suffix:    "_s",
		OutParams: backend.OutParams{"output_folder": dest},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dest, "clip_s.mp4"); len(res.OutputPaths) != 1 || !strings.HasSuffix(res.OutputPaths[0], "clip_s.mp4") {
		t.Fatalf("OutputPaths = %v, want %s", res.OutputPaths, want)
	}
	if _, err := os.Stat(filepath.Join(dest, "clip_s.mp4")); err != nil {
		t.Fatal(err)
	}
}

func TestStagingRefusesToOverwriteBeforeRendering(t *testing.T) {
	s, clip, argsLog := newStagingFixture(t)
	existing := filepath.Join(filepath.Dir(clip), "clip_stabilized.mov")
	if err := os.WriteFile(existing, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := s.Stabilize(context.Background(), backend.StabilizeRequest{Inputs: []string{clip}}, nil)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(argsLog); statErr == nil {
		t.Fatal("gyroflow ran although the output exists")
	}
	if b, _ := os.ReadFile(existing); string(b) != "mine" {
		t.Fatalf("existing output changed: %q", b)
	}
}

func TestStagingOverwriteReplacesOutput(t *testing.T) {
	s, clip, _ := newStagingFixture(t)
	existing := filepath.Join(filepath.Dir(clip), "clip_stabilized.mp4")
	if err := os.WriteFile(existing, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := s.Stabilize(context.Background(), backend.StabilizeRequest{Inputs: []string{clip}, Overwrite: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(existing); string(b) != "rendered" {
		t.Fatalf("output = %q", b)
	}
}

func TestStagingRejectsDuplicateNamesAndProjects(t *testing.T) {
	s, clip, _ := newStagingFixture(t)
	other2 := filepath.Join(t.TempDir(), "clip.MP4")
	if err := os.WriteFile(other2, []byte("v"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stabilize(context.Background(), backend.StabilizeRequest{Inputs: []string{clip, other2}}, nil); err == nil {
		t.Fatal("expected duplicate name error")
	}
	project := filepath.Join(t.TempDir(), "p.gyroflow")
	if err := os.WriteFile(project, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stabilize(context.Background(), backend.StabilizeRequest{Inputs: []string{project}}, nil); !errors.Is(err, ErrStagingUnsupported) {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.ExportProject(context.Background(), backend.ExportRequest{}); !errors.Is(err, ErrStagingUnsupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestPruneStaleJobsKeepsRecent(t *testing.T) {
	root := t.TempDir()
	stale, recent := filepath.Join(root, "job-old"), filepath.Join(root, "job-new")
	for _, d := range []string{stale, recent} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * staleJobAge)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	pruneStaleJobs(root, staleJobAge)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale job not removed")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatal("recent job removed")
	}
}

func TestStabilizeReportsFailureDespiteExitZero(t *testing.T) {
	tmp := t.TempDir()
	bin, _ := fakeGyroflow(t, tmp, filepath.Join(tmp, "nowhere"))
	clip := filepath.Join(tmp, "clip.mp4")
	if err := os.WriteFile(clip, []byte("v"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := New(bin).Stabilize(context.Background(), backend.StabilizeRequest{Inputs: []string{clip}}, nil)
	if err == nil || !strings.Contains(err.Error(), "render failed") {
		t.Fatalf("err = %v", err)
	}
}

func TestPresetFileLosesOutputLocation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "p.gyroflow")
	preset := `{"version":2,"output":{"bitrate":124,"output_folder":"file:///elsewhere/","output_filename":"x.mp4","output_height":2160}}`
	if err := os.WriteFile(p, []byte(preset), 0o644); err != nil {
		t.Fatal(err)
	}
	req, err := prepareStabilize(backend.StabilizeRequest{PresetPath: p})
	if err != nil {
		t.Fatal(err)
	}
	if req.PresetPath != "" {
		t.Fatalf("PresetPath = %q, want inlined", req.PresetPath)
	}
	var got struct {
		Output map[string]any `json:"output"`
	}
	if err := json.Unmarshal([]byte(req.PresetJSON), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Output["output_folder"]; ok {
		t.Fatalf("output_folder kept: %v", got.Output)
	}
	if _, ok := got.Output["output_filename"]; ok {
		t.Fatalf("output_filename kept: %v", got.Output)
	}
	if got.Output["output_height"] != float64(2160) {
		t.Fatalf("output_height lost: %v", got.Output)
	}
}

func TestSummaryFromQuaternionsWithoutRawIMU(t *testing.T) {
	raw := []byte(`{"detected_source":"DJI O4P","camera_identifier":{"brand":"DJI","model":"O4P","fps":60000,"video_width":3840,"video_height":2880},
		"raw_imu":[],"quaternions":{"-500":[1,0,0,0],"500":[1,0,0,0],"2000000":[1,0,0,0]},
		"lens_profile":{"camera_brand":"DJI","camera_model":"O4P"}}`)
	got, err := parseMetadataSummary(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasGyro {
		t.Error("HasGyro = false")
	}
	if got.DurationS < 2 || got.DurationS > 2.01 {
		t.Errorf("DurationS = %v", got.DurationS)
	}
	if got.FrameCount != 120 {
		t.Errorf("FrameCount = %d, want 120", got.FrameCount)
	}
	if got.DetectedProfile != "DJI O4P" {
		t.Errorf("DetectedProfile = %q", got.DetectedProfile)
	}
}
