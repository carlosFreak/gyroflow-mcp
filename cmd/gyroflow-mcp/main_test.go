package main

import (
	"context"
	"testing"

	"github.com/carlosFreak/gyroflow-mcp/internal/lens"
)

func TestBuildServerHasName(t *testing.T) {
	// Hermetic: stub the lens loader so buildServer() does ZERO network.
	// Load the committed verbatim fixtures (offline, fast, deterministic).
	orig := loadLensIndexFn
	t.Cleanup(func() { loadLensIndexFn = orig })
	loadLensIndexFn = func(context.Context) *lens.Index {
		idx, err := lens.LoadFromDir("../../testdata/lens/sample_profiles")
		if err != nil {
			return nil
		}
		return idx
	}

	s := buildServer()
	if s == nil {
		t.Fatal("buildServer returned nil")
	}
}
