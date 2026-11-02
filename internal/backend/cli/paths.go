package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
)

// absInput resolves p to an absolute path and verifies it exists.
// Returns a clear "file not found: <abs>" error to avoid the opaque
// gyroflow "Unable to read the video file" message on relative or missing paths.
func absInput(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("resolving path %q: %w", p, err)
	}
	if _, err := os.Stat(abs); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("file not found: %s", abs)
		}
		return "", fmt.Errorf("accessing path %s: %w", abs, err)
	}
	return abs, nil
}

// absOut resolves p to an absolute path without checking existence (output path).
func absOut(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("resolving output path %q: %w", p, err)
	}
	return abs, nil
}

// resolveStabilizeReq returns a shallow copy of req with all path fields
// resolved to absolute paths via filepath.Abs. Input paths that do not exist
// return a clear error immediately - gyroflow's "Unable to read the video file"
// is not surfaced.
func resolveStabilizeReq(req backend.StabilizeRequest) (backend.StabilizeRequest, error) {
	inputs := make([]string, len(req.Inputs))
	for i, p := range req.Inputs {
		abs, err := absInput(p)
		if err != nil {
			return req, err
		}
		inputs[i] = abs
	}
	req.Inputs = inputs

	if req.GyroFile != "" {
		abs, err := absInput(req.GyroFile)
		if err != nil {
			return req, err
		}
		req.GyroFile = abs
	}

	if req.PresetPath != "" {
		abs, err := absInput(req.PresetPath)
		if err != nil {
			return req, err
		}
		req.PresetPath = abs
	}

	return req, nil
}

// resolveExportReq returns a shallow copy of req with Input resolved to an
// absolute existing path and Out resolved to absolute (existence not required).
func resolveExportReq(req backend.ExportRequest) (backend.ExportRequest, error) {
	if req.Input != "" {
		abs, err := absInput(req.Input)
		if err != nil {
			return req, err
		}
		req.Input = abs
	}

	if req.Out != "" {
		abs, err := absOut(req.Out)
		if err != nil {
			return req, err
		}
		req.Out = abs
	}

	return req, nil
}
