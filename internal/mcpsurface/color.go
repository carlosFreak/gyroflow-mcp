package mcpsurface

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
	"github.com/carlosFreak/gyroflow-mcp/internal/lut"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// previewer is the backend that can render a before/after color frame.
type previewer interface {
	Preview(ctx context.Context, input string, at float64, cube string, color *backend.ColorAdjust, out string) error
}

// resolveColor merges a saved look with explicit adjustments, which win
// field by field. nil when nothing changes.
func resolveColor(lib *lut.Library, look string, c *backend.ColorAdjust) (*backend.ColorAdjust, error) {
	var out backend.ColorAdjust
	if look != "" {
		if lib == nil {
			return nil, errors.New("looks need ffmpeg (brew install ffmpeg)")
		}
		l, err := lib.Look(look)
		if err != nil {
			return nil, err
		}
		out = l
	}
	if c != nil {
		for _, f := range []struct {
			dst *float64
			v   float64
		}{
			{&out.Exposure, c.Exposure}, {&out.Contrast, c.Contrast}, {&out.Saturation, c.Saturation},
			{&out.Vibrance, c.Vibrance}, {&out.Warmth, c.Warmth}, {&out.Sharpness, c.Sharpness},
		} {
			if f.v != 0 {
				*f.dst = f.v
			}
		}
	}
	if err := out.Validate(); err != nil {
		return nil, err
	}
	if out.IsZero() {
		return nil, nil
	}
	return &out, nil
}

type previewArgs struct {
	Input string               `json:"input"           jsonschema:"source video"`
	AtS   float64              `json:"at_s,omitempty"  jsonschema:"second of the clip to show (default 5)"`
	LUT   string               `json:"lut,omitempty"   jsonschema:"LUT to include: a list_luts name or a .cube path; omit or none for no LUT"`
	Color *backend.ColorAdjust `json:"color,omitempty" jsonschema:"color adjustments to show"`
	Look  string               `json:"look,omitempty"  jsonschema:"saved look to show; color fields override it"`
}

type previewOut struct {
	Path string `json:"path"`
}

type saveLookArgs struct {
	Name  string              `json:"name"  jsonschema:"name for the look, e.g. Dron soleado"`
	Color backend.ColorAdjust `json:"color" jsonschema:"the adjustments to save"`
}

type saveLookOut struct {
	Saved string `json:"saved"`
}

func registerColorTools(s *mcp.Server, be backend.Backend, lib *lut.Library) {
	pv, ok := be.(previewer)
	if ok {
		mcp.AddTool(s, &mcp.Tool{
			Name: "preview_color",
			Description: "Show one frame of a clip before (left) and after (right) a LUT and/or color adjustments, as an image " +
				"and a JPEG path, so the user can judge them before a full render. The frame is the unstabilized source.",
			Annotations: readOnlyAnn("Preview color"),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, a previewArgs) (*mcp.CallToolResult, previewOut, error) {
			color, err := resolveColor(lib, a.Look, a.Color)
			if err != nil {
				return nil, previewOut{}, err
			}
			cube := ""
			if a.LUT != "" && !strings.EqualFold(a.LUT, lut.None) {
				if cube, err = lib.Resolve(a.LUT); err != nil {
					return nil, previewOut{}, err
				}
			}
			at := a.AtS
			if at <= 0 {
				at = 5
			}
			dir := filepath.Join(os.TempDir(), "gyroflow-mcp-previews")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, previewOut{}, err
			}
			out := filepath.Join(dir, strings.TrimSuffix(filepath.Base(a.Input), filepath.Ext(a.Input))+"_preview.jpg")
			if err := pv.Preview(ctx, a.Input, at, cube, color, out); err != nil {
				return nil, previewOut{}, err
			}
			img, err := os.ReadFile(out)
			if err != nil {
				return nil, previewOut{}, err
			}
			return &mcp.CallToolResult{Content: []mcp.Content{
				&mcp.ImageContent{Data: img, MIMEType: "image/jpeg"},
				&mcp.TextContent{Text: fmt.Sprintf("Before (left) and after (right): %s", out)},
			}}, previewOut{Path: out}, nil
		})
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "save_look",
		Description: "Save a set of color adjustments under a name, to reuse later with render_start look or preview_color look.",
		Annotations: destructiveAnn("Save look"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a saveLookArgs) (*mcp.CallToolResult, saveLookOut, error) {
		if err := lib.SaveLook(a.Name, a.Color); err != nil {
			return nil, saveLookOut{}, err
		}
		return nil, saveLookOut{Saved: strings.TrimSpace(a.Name)}, nil
	})
}
