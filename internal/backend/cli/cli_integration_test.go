//go:build integration

package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
	"github.com/carlosFreak/gyroflow-mcp/internal/gyroflow"
)

// resStderr safely extracts StderrTail from a possibly-nil Result.
func resStderr(r *backend.Result) string {
	if r == nil {
		return "<nil result>"
	}
	return r.StderrTail
}

// TestCLIBackendStabilizeRelativePath verifies that CLIBackend resolves a
// relative input path to absolute before invoking gyroflow. This guards the
// empirically confirmed behaviour: gyroflow CLI exits with "File X doesn't
// exist" when given a relative path, even if the file is present.
//
// Requires:
//   - testdata/vids/sample_gyro_h264.mp4 (H.264 re-encode, git-excluded - see testdata/README.md)
//   - /Applications/Gyroflow.app/Contents/MacOS/gyroflow (real DMG build, not App Store sandbox)
//
// Run with: go test -tags integration ./internal/backend/cli/ -run TestCLIBackendStabilizeRelativePath -v
func TestCLIBackendStabilizeRelativePath(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("gyroflow binary only available on macOS")
	}

	bin := "/Applications/Gyroflow.app/Contents/MacOS/gyroflow"
	if _, err := os.Stat(bin); os.IsNotExist(err) {
		t.Skipf("gyroflow binary not found at %s", bin)
	}

	// Locate testdata/vids/sample_gyro_h264.mp4 relative to this test file's directory.
	// The test runs with cwd = package directory; go up two levels to the module root.
	absFixture, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "vids", "sample_gyro_h264.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(absFixture); os.IsNotExist(err) {
		t.Skip("testdata/vids/sample_gyro_h264.mp4 not present (see testdata/README.md)")
	}

	// Build a relative path from the test's working directory (package dir).
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relPath, err := filepath.Rel(cwd, absFixture)
	if err != nil {
		t.Fatal(err)
	}

	be := New(bin)
	var progressCount int
	_, err = be.Stabilize(
		context.Background(),
		backend.StabilizeRequest{
			Inputs:    []string{relPath},
			Overwrite: true,
		},
		func(p backend.Progress) {
			progressCount++
			t.Logf("progress: frame=%d/%d (%.1f%%) ETA=%.1fs", p.Frame, p.Total, p.Percent, p.ETA)
		},
	)
	// We expect either success or a gyroflow render error - but NOT a
	// "file not found" path-resolution error, which would mean the relative
	// path was passed verbatim to gyroflow.
	if err != nil {
		if progressCount > 0 {
			// Got progress then failed - gyroflow issue, not a path problem.
			t.Logf("gyroflow exited with error after %d progress events: %v", progressCount, err)
			return
		}
		t.Fatalf("Stabilize failed before any progress - possible relative-path bug: %v", err)
	}
	if progressCount == 0 {
		t.Fatal("expected at least one progress event from gyroflow render")
	}
}

// TestStabilizeRealBinary runs the real detected gyroflow binary against the
// committed sample and proves a real stabilized output file is produced (no
// mocking). The sample is HEVC (~1384 frames) so a full render may take a
// few minutes - acceptable for an integration-tagged test that is excluded
// from the default suite.
//
// Run with:
//
//	go test -tags integration ./internal/backend/cli/ -run TestStabilizeRealBinary -v -timeout 15m
func TestStabilizeRealBinary(t *testing.T) {
	info, err := gyroflow.Detect(context.Background())
	if err != nil {
		t.Skipf("gyroflow not installed: %v", err)
	}
	t.Logf("using gyroflow %s at %s", info.Version, info.Path)

	be := New(info.Path)
	// Failure-safe cleanup: register BEFORE the render so a produced output
	// file can't leak into testdata/ even if a t.Fatalf guard below fires.
	defer func() {
		m, _ := filepath.Glob("../../../testdata/vids/sample_gyro_stab*")
		for _, f := range m {
			_ = os.Remove(f)
		}
	}()
	res, err := be.Stabilize(
		context.Background(),
		backend.StabilizeRequest{
			Inputs:    []string{"../../../testdata/vids/sample_gyro.mp4"},
			Overwrite: true,
			Suffix:    "_stab",
		},
		func(p backend.Progress) {
			t.Logf("progress frame=%d/%d %.1f%% ETA=%.1fs", p.Frame, p.Total, p.Percent, p.ETA)
		},
	)
	if err != nil {
		t.Fatalf("Stabilize: %v stderr=%s", err, resStderr(res))
	}
	if res == nil || res.ExitCode != 0 {
		t.Fatalf("exit/res bad: %+v stderr=%s", res, resStderr(res))
	}

	matches, _ := filepath.Glob("../../../testdata/vids/sample_gyro_stab*")
	if len(matches) == 0 {
		t.Fatalf("no stabilized output file produced (stderr=%s)", resStderr(res))
	}

	// Result.OutputPaths must be populated (parsed from gyroflow's real Queue
	// line) and the path it names must exist on disk and be the same file the
	// glob found. This closes the gap that let fake backends mask an empty
	// OutputPaths.
	if len(res.OutputPaths) == 0 {
		t.Fatalf("res.OutputPaths empty - Queue-line parse failed (stderr=%s)", resStderr(res))
	}
	t.Logf("res.OutputPaths = %v", res.OutputPaths)
	absMatch, _ := filepath.Abs(matches[0])
	matchedReported := false
	for _, op := range res.OutputPaths {
		fi, statErr := os.Stat(op)
		if statErr != nil {
			t.Fatalf("reported output path does not exist: %s (%v)", op, statErr)
		}
		t.Logf("reported output exists: %s (%d bytes)", op, fi.Size())
		if absOp, _ := filepath.Abs(op); absOp == absMatch {
			matchedReported = true
		}
	}
	if !matchedReported {
		t.Fatalf("reported OutputPaths %v do not include the produced file %s", res.OutputPaths, absMatch)
	}

	for _, m := range matches {
		_ = os.Remove(m)
	}
}

// TestStabilizeCompoundConfigRealBinary drives the real gyroflow binary with
// a compound GyroflowConfig (smoothing method + smoothness, horizon lock,
// adaptive zoom, trim range, a Fov keyframe, and output codec) and asserts
// that the output file is produced on disk and reported in res.OutputPaths.
//
// Run with:
//
//	go test -tags integration ./internal/backend/cli/ -run TestStabilizeCompoundConfigRealBinary -v -timeout 15m
func TestStabilizeCompoundConfigRealBinary(t *testing.T) {
	info, err := gyroflow.Detect(context.Background())
	if err != nil {
		t.Skipf("gyroflow not installed: %v", err)
	}
	t.Logf("using gyroflow %s at %s", info.Version, info.Path)

	be := New(info.Path)
	defer func() {
		m, _ := filepath.Glob("../../../testdata/vids/sample_gyro_compound*")
		for _, p := range m {
			_ = os.Remove(p)
		}
	}()

	codec := "H.265/HEVC"
	cfg := &backend.GyroflowConfig{
		Smoothing:   &backend.SmoothingConfig{Method: "Default", Smoothness: f(0.7)},
		HorizonLock: &backend.HorizonLockConfig{Amount: f(100)},
		Zoom:        &backend.ZoomConfig{AdaptiveZoomWindow: f(-1)},
		Trim:        &backend.TrimConfig{RangesMS: [][2]float64{{0, 1500}}},
		Keyframes:   []backend.KeyframeConfig{{Param: "Fov", TimestampMS: 500, Value: 1.3, Easing: "EaseIn"}},
		Output:      &backend.OutputConfig{Codec: &codec},
	}

	res, err := be.Stabilize(
		context.Background(),
		backend.StabilizeRequest{
			Inputs:    []string{"../../../testdata/vids/sample_gyro.mp4"},
			Overwrite: true,
			Suffix:    "_compound",
			Config:    cfg,
		},
		func(p backend.Progress) {
			t.Logf("progress frame=%d/%d %.1f%% ETA=%.1fs", p.Frame, p.Total, p.Percent, p.ETA)
		},
	)
	if err != nil {
		t.Fatalf("Stabilize: %v stderr=%s", err, resStderr(res))
	}
	if res == nil || res.ExitCode != 0 {
		t.Fatalf("exit/res bad: %+v stderr=%s", res, resStderr(res))
	}
	if len(res.OutputPaths) == 0 {
		t.Fatalf("res.OutputPaths empty - compound config render produced no output (stderr=%s)", resStderr(res))
	}
	t.Logf("res.OutputPaths = %v", res.OutputPaths)
	for _, op := range res.OutputPaths {
		fi, statErr := os.Stat(op)
		if statErr != nil {
			t.Fatalf("reported output path does not exist: %s (%v)", op, statErr)
		}
		t.Logf("output exists on disk: %s (%d bytes)", op, fi.Size())
	}
}
