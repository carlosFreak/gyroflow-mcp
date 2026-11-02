package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
)

// TestResolveStabilizeReqAbsolutizesRelativeInput verifies that a relative
// input path is resolved to absolute before being passed to gyroflow.
func TestResolveStabilizeReqAbsolutizesRelativeInput(t *testing.T) {
	// Create a real temp file so the existence check passes.
	dir := t.TempDir()
	f := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(f, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Build a relative path from cwd.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(cwd, f)
	if err != nil {
		t.Fatal(err)
	}

	req := backend.StabilizeRequest{Inputs: []string{rel}}
	resolved, err := resolveStabilizeReq(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !filepath.IsAbs(resolved.Inputs[0]) {
		t.Fatalf("expected absolute path, got %q", resolved.Inputs[0])
	}
	if resolved.Inputs[0] != f {
		t.Fatalf("expected %q, got %q", f, resolved.Inputs[0])
	}
}

// TestResolveStabilizeReqRejectsNonexistentInput verifies that a clear
// "file not found: <abs>" error is returned instead of the opaque gyroflow
// "Unable to read the video file" message.
func TestResolveStabilizeReqRejectsNonexistentInput(t *testing.T) {
	req := backend.StabilizeRequest{Inputs: []string{"/definitely/does/not/exist.mp4"}}
	_, err := resolveStabilizeReq(req)
	if err == nil {
		t.Fatal("expected error for non-existent file, got nil")
	}
	if !strings.Contains(err.Error(), "file not found") {
		t.Fatalf("expected 'file not found' in error, got: %v", err)
	}
	// Absolute path must appear in the error message.
	if !strings.Contains(err.Error(), "/definitely/does/not/exist.mp4") {
		t.Fatalf("expected absolute path in error, got: %v", err)
	}
}

// TestResolveStabilizeReqOptionalPaths verifies GyroFile and PresetPath are
// also resolved to absolute when non-empty.
func TestResolveStabilizeReqOptionalPaths(t *testing.T) {
	dir := t.TempDir()
	gyroFile := filepath.Join(dir, "gyro.gcsv")
	presetFile := filepath.Join(dir, "preset.gyroflow")
	clipFile := filepath.Join(dir, "clip.mp4")
	for _, p := range []string{gyroFile, presetFile, clipFile} {
		if err := os.WriteFile(p, []byte("fake"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cwd, _ := os.Getwd()
	relClip, _ := filepath.Rel(cwd, clipFile)
	relGyro, _ := filepath.Rel(cwd, gyroFile)
	relPreset, _ := filepath.Rel(cwd, presetFile)

	req := backend.StabilizeRequest{
		Inputs:     []string{relClip},
		GyroFile:   relGyro,
		PresetPath: relPreset,
	}
	resolved, err := resolveStabilizeReq(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !filepath.IsAbs(resolved.GyroFile) {
		t.Fatalf("GyroFile not absolute: %q", resolved.GyroFile)
	}
	if !filepath.IsAbs(resolved.PresetPath) {
		t.Fatalf("PresetPath not absolute: %q", resolved.PresetPath)
	}
}
