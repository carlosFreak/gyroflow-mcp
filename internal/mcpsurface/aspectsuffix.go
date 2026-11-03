package mcpsurface

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
)

const defaultSuffix = "_stabilized"

// namedRatios are the ratios a file name spells out; sizes within 1% of one
// (even-pixel rounding, 2.39:1 crops) get its name instead of their pixels.
var namedRatios = [][2]int{
	{1, 1}, {4, 3}, {3, 4}, {16, 9}, {9, 16}, {4, 5}, {5, 4}, {3, 2}, {2, 3}, {21, 9}, {9, 21}, {2, 1}, {1, 2},
}

// ratioName names a frame size for a file name: 2880x2880 → "1x1",
// 1620x2880 → "9x16". Other shapes keep their pixels, e.g. "1919x1080".
func ratioName(w, h int) string {
	r := float64(w) / float64(h)
	for _, n := range namedRatios {
		if math.Abs(r/(float64(n[0])/float64(n[1]))-1) < 0.01 {
			return fmt.Sprintf("%dx%d", n[0], n[1])
		}
	}
	return fmt.Sprintf("%dx%d", w, h)
}

// withAspect appends the output's aspect ratio to the file-name suffix:
// "" → "_stabilized_1x1", "_test" → "_test_1x1". The suffix is left alone when
// it already ends in that ratio or the size cannot be known for every input.
func withAspect(ctx context.Context, be backend.Backend, a startArgs, cfg *backend.GyroflowConfig) string {
	suffix := a.Suffix
	if suffix == "" {
		suffix = defaultSuffix
	}
	w, h := outputSize(a, cfg)
	name := ""
	if w > 0 && h > 0 {
		name = ratioName(w, h)
	} else {
		name = sourceRatio(ctx, be, a.Inputs)
	}
	if name == "" || strings.HasSuffix(strings.ToLower(suffix), "_"+name) {
		return suffix
	}
	return suffix + "_" + name
}

// outputSize is the output frame the user set: typed config (aspect_ratio is
// already resolved to a size), raw out_params, then the preset file. 0, 0 when
// the output keeps each source's size.
func outputSize(a startArgs, cfg *backend.GyroflowConfig) (w, h int) {
	if a.PresetPath != "" {
		if raw, err := os.ReadFile(a.PresetPath); err == nil {
			var p struct {
				Output struct {
					W int `json:"output_width"`
					H int `json:"output_height"`
				} `json:"output"`
			}
			if json.Unmarshal(raw, &p) == nil {
				w, h = p.Output.W, p.Output.H
			}
		}
	}
	if v, ok := a.OutParams["output_width"].(float64); ok {
		w = int(v)
	}
	if v, ok := a.OutParams["output_height"].(float64); ok {
		h = int(v)
	}
	if cfg != nil && cfg.Output != nil {
		if cfg.Output.OutputWidth != nil {
			w = *cfg.Output.OutputWidth
		}
		if cfg.Output.OutputHeight != nil {
			h = *cfg.Output.OutputHeight
		}
	}
	return w, h
}

// sourceRatio is the ratio shared by every input video, or "" when they differ,
// one is a .gyroflow project, or a probe fails.
func sourceRatio(ctx context.Context, be backend.Backend, inputs []string) string {
	name := ""
	for _, in := range inputs {
		if isGyroflowFile(in) {
			return ""
		}
		m, err := be.ProbeMetadata(ctx, in, backend.ProbeOptions{})
		if err != nil || m.Summary.Width <= 0 || m.Summary.Height <= 0 {
			return ""
		}
		n := ratioName(m.Summary.Width, m.Summary.Height)
		if name != "" && n != name {
			return ""
		}
		name = n
	}
	return name
}
