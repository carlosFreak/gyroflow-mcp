package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
)

func writeFakeGyroflow(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-binary test uses a POSIX shell script")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "gyroflow")
	// Emit real gyroflow --stdout-progress format (verified from testdata/stdout_progress_sample.txt).
	script := "#!/bin/sh\necho '[0f271661] Rendering progress: 1/2 frames (50.0%) ETA 1.0s'\necho '[0f271661] Rendering progress: 2/2 frames (100.0%) ETA 0.0s'\nexit 0\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// writeFakeReadErrorScript writes a gyroflow stub that emits the real
// "[WARN] [add_file]: Unable to read the video file." to stderr and exits 1.
// Pinned to the real captured error string in classify_test.go.
func writeFakeReadErrorScript(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-binary test uses a POSIX shell script")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "gyroflow")
	script := "#!/bin/sh\necho '[WARN] [add_file]: Unable to read the video file.' >&2\nexit 1\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestStabilizeReadErrorAdvice verifies that when gyroflow emits the real
// "Unable to read the video file" error, Stabilize wraps the error with
// actionable advice and preserves the raw line in Result.StderrTail.
func TestStabilizeReadErrorAdvice(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-binary test uses a POSIX shell script")
	}
	clipDir := t.TempDir()
	clipPath := filepath.Join(clipDir, "clip.mp4")
	if err := os.WriteFile(clipPath, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	be := New(writeFakeReadErrorScript(t))
	res, err := be.Stabilize(context.Background(),
		backend.StabilizeRequest{Inputs: []string{clipPath}}, nil)
	if err == nil {
		t.Fatal("expected error from failing gyroflow")
	}
	if runtime.GOOS == "darwin" {
		if !strings.Contains(err.Error(), "brew install --cask gyroflow") {
			t.Fatalf("darwin error missing brew advice: %v", err)
		}
	}
	// StderrTail must always contain the raw output regardless of platform.
	if res == nil || !strings.Contains(res.StderrTail, "Unable to read the video file") {
		t.Fatalf("StderrTail missing raw error line (res=%+v, err=%v)", res, err)
	}
}

// TestStabilizeUnrelatedErrorNoAdvice verifies that unrecognised gyroflow
// errors are returned unchanged (matched=false path in classifyRunFailure).
func TestStabilizeUnrelatedErrorNoAdvice(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-binary test uses a POSIX shell script")
	}
	clipDir := t.TempDir()
	clipPath := filepath.Join(clipDir, "clip.mp4")
	if err := os.WriteFile(clipPath, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "gyroflow")
	script := "#!/bin/sh\necho 'some completely unrelated gyroflow error' >&2\nexit 1\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	be := New(p)
	_, err := be.Stabilize(context.Background(),
		backend.StabilizeRequest{Inputs: []string{clipPath}}, nil)
	if err == nil {
		t.Fatal("expected error from failing gyroflow")
	}
	if strings.Contains(err.Error(), "brew install --cask gyroflow") {
		t.Fatalf("unrelated error must not contain brew advice: %v", err)
	}
}

func TestCLIBackendStabilizeStreamsProgress(t *testing.T) {
	// Create a real temp file so the path-existence check in Stabilize passes.
	// The fake gyroflow binary ignores argv and just emits progress lines.
	clipDir := t.TempDir()
	clipPath := filepath.Join(clipDir, "clip.mp4")
	if err := os.WriteFile(clipPath, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	be := New(writeFakeGyroflow(t))
	var seen []backend.Progress
	res, err := be.Stabilize(context.Background(),
		backend.StabilizeRequest{Inputs: []string{clipPath}},
		func(p backend.Progress) { seen = append(seen, p) })
	if err != nil {
		t.Fatalf("Stabilize: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d", res.ExitCode)
	}
	if len(seen) != 2 || seen[1].Percent != 100.0 {
		t.Fatalf("progress not streamed: %+v", seen)
	}
}

// TestTailBufferBound verifies the bounded writer retains only the last
// maxTailBytes bytes and that the most-recent content is preserved.
func TestTailBufferBound(t *testing.T) {
	var tb tailBuffer
	// Write well over the cap in chunks.
	chunk := strings.Repeat("A", 4096)
	for i := 0; i < (maxTailBytes/len(chunk))+50; i++ {
		if _, err := tb.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	tb.WriteString("TAILMARKER")
	if tb.Len() > maxTailBytes {
		t.Fatalf("tailBuffer grew past cap: len=%d cap=%d", tb.Len(), maxTailBytes)
	}
	if !strings.HasSuffix(tb.String(), "TAILMARKER") {
		t.Fatal("tailBuffer dropped the most-recent bytes")
	}
	// A single oversized write must also be bounded to its last maxTailBytes.
	var tb2 tailBuffer
	big := strings.Repeat("B", maxTailBytes*3) + "ENDZ"
	tb2.WriteString(big)
	if tb2.Len() > maxTailBytes {
		t.Fatalf("oversized single write not bounded: len=%d", tb2.Len())
	}
	if !strings.HasSuffix(tb2.String(), "ENDZ") {
		t.Fatal("oversized single write lost its tail")
	}
}

// TestStabilizeScannerErrorNoRace forces a stdout bufio.Scanner error
// (bufio.ErrTooLong: a single line larger than bufio.MaxScanTokenSize with no
// newline) WHILE gyroflow also writes stderr. Pre-fix, the scanner-error path
// wrote the exec-owned stderr tailBuffer concurrently with os/exec's internal
// stderr copier → data race. This test, run with -race, proves no race and
// that the scanner-error note + stderr both land in StderrTail.
func TestStabilizeScannerErrorNoRace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-binary test uses a POSIX shell script")
	}
	clipDir := t.TempDir()
	clipPath := filepath.Join(clipDir, "clip.mp4")
	if err := os.WriteFile(clipPath, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "gyroflow")
	// 200000 'A' bytes to stdout with NO newline → token > bufio.MaxScanTokenSize
	// (65536) → bufio.ErrTooLong. Concurrently emit a stderr marker, exit 1.
	script := "#!/bin/sh\n" +
		"head -c 200000 /dev/zero | tr '\\0' 'A'\n" +
		"echo 'STDERR_MARKER_LINE' >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	be := New(p)
	res, err := be.Stabilize(context.Background(),
		backend.StabilizeRequest{Inputs: []string{clipPath}}, nil)
	if err == nil {
		t.Fatal("expected error from failing gyroflow")
	}
	if res == nil {
		t.Fatal("expected non-nil Result")
	}
	if !strings.Contains(res.StderrTail, "[stdout scanner error") {
		t.Fatalf("scanner-error note missing from StderrTail: %q", res.StderrTail)
	}
	if !strings.Contains(res.StderrTail, "STDERR_MARKER_LINE") {
		t.Fatalf("stderr content missing from StderrTail: %q", res.StderrTail)
	}
}

// TestStabilizeBoundedBufferStillClassifies feeds many KB of non-progress
// stdout, then the real read-error line, and asserts the bound holds while
// classifyRunFailure + StderrTail still work.
func TestStabilizeBoundedBufferStillClassifies(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-binary test uses a POSIX shell script")
	}
	clipDir := t.TempDir()
	clipPath := filepath.Join(clipDir, "clip.mp4")
	if err := os.WriteFile(clipPath, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "gyroflow")
	// Spam ~2MB of non-progress stdout, then the real WARN line, exit 1.
	script := "#!/bin/sh\n" +
		"i=0\nwhile [ $i -lt 20000 ]; do " +
		"echo 'noise noise noise noise noise noise noise noise noise noise'; " +
		"i=$((i+1)); done\n" +
		"echo '[WARN] [add_file]: Unable to read the video file.' >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	be := New(p)
	res, err := be.Stabilize(context.Background(),
		backend.StabilizeRequest{Inputs: []string{clipPath}}, nil)
	if err == nil {
		t.Fatal("expected error from failing gyroflow")
	}
	if res == nil || !strings.Contains(res.StderrTail, "Unable to read the video file") {
		t.Fatalf("StderrTail lost the raw error line under heavy output: res=%+v", res)
	}
	if runtime.GOOS == "darwin" {
		if !strings.Contains(err.Error(), "brew install --cask gyroflow") {
			t.Fatalf("classifyRunFailure stopped matching under heavy output: %v", err)
		}
	}
}

func TestPrepareStabilizeBuildsPresetAndRoutesParams(t *testing.T) {
	req := backend.StabilizeRequest{
		Inputs: []string{"clip.mp4"},
		Config: &backend.GyroflowConfig{
			Smoothing: &backend.SmoothingConfig{Method: "Default", Smoothness: f(0.6)},
			Output:    &backend.OutputConfig{Codec: sp("H.265/HEVC"), Bitrate: f(120)},
			Sync:      &backend.SyncConfig{SearchSize: f(3)},
		},
	}
	out, err := prepareStabilize(req)
	if err != nil {
		t.Fatalf("prepareStabilize: %v", err)
	}
	if out.PresetJSON == "" || !strings.Contains(out.PresetJSON, `"method":"Default"`) {
		t.Fatalf("preset not built: %q", out.PresetJSON)
	}
	if out.OutParams["codec"] != "H.265/HEVC" || out.OutParams["bitrate"] != float64(120) {
		t.Fatalf("output not routed: %v", out.OutParams)
	}
	if out.SyncParams["search_size"] != float64(3) {
		t.Fatalf("sync not routed: %v", out.SyncParams)
	}
}

func TestPrepareStabilizeRejectsInvalidConfig(t *testing.T) {
	_, err := prepareStabilize(backend.StabilizeRequest{
		Inputs: []string{"c.mp4"},
		Config: &backend.GyroflowConfig{Smoothing: &backend.SmoothingConfig{Method: "Default", Smoothness: f(9)}},
	})
	if err == nil || !strings.Contains(err.Error(), "smoothness") {
		t.Fatalf("want smoothness range error, got %v", err)
	}
}

func sp(s string) *string { return &s }

func TestPrepareStabilizeOverlaysPresetPath(t *testing.T) {
	dir := t.TempDir()
	pf := filepath.Join(dir, "base.gyroflow")
	// base preset: method "Plain 3D" + a key the config does NOT set ("description"
	// is not emitted by buildPresetJSON, so it survives the overlay unchanged).
	if err := os.WriteFile(pf, []byte(`{"version":3,"stabilization":{"method":"Plain 3D"},"description":"base"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := prepareStabilize(backend.StabilizeRequest{
		Inputs:     []string{"c.mp4"},
		PresetPath: pf,
		Config:     &backend.GyroflowConfig{Smoothing: &backend.SmoothingConfig{Method: "Default", Smoothness: f(0.6)}},
	})
	if err != nil {
		t.Fatalf("prepareStabilize: %v", err)
	}
	if out.PresetPath != "" {
		t.Fatalf("PresetPath not cleared: %q", out.PresetPath)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out.PresetJSON), &m); err != nil {
		t.Fatalf("merged preset not JSON: %v", err)
	}
	if m["description"] != "base" {
		t.Fatalf("base-only key lost in overlay: %v", m["description"])
	}
	st := m["stabilization"].(map[string]any)
	if st["method"] != "Default" {
		t.Fatalf("generated config did not override base method: %v", st["method"])
	}
}

func TestPrepareStabilizePresetPathErrors(t *testing.T) {
	// missing file
	_, err := prepareStabilize(backend.StabilizeRequest{
		Inputs: []string{"c.mp4"}, PresetPath: "/no/such/preset.gyroflow",
		Config: &backend.GyroflowConfig{Smoothing: &backend.SmoothingConfig{Method: "Default"}},
	})
	if err == nil || !strings.Contains(err.Error(), "preset_path") {
		t.Fatalf("want preset_path read error, got %v", err)
	}
	// invalid JSON file
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.gyroflow")
	if werr := os.WriteFile(bad, []byte("{not json"), 0o644); werr != nil {
		t.Fatal(werr)
	}
	_, err = prepareStabilize(backend.StabilizeRequest{
		Inputs: []string{"c.mp4"}, PresetPath: bad,
		Config: &backend.GyroflowConfig{Smoothing: &backend.SmoothingConfig{Method: "Default"}},
	})
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("want invalid-JSON error, got %v", err)
	}
}
