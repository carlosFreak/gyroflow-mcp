package mcpsurface

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
	"github.com/carlosFreak/gyroflow-mcp/internal/lut"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolOption configures RegisterTools.
type ToolOption func(*toolConfig)

type toolConfig struct {
	luts *lut.Library // nil: LUTs are off (no ffmpeg or not configured)
}

// WithLUTs turns on the LUT question and list_luts. Pass it only when ffmpeg
// is available, since every LUT render needs it.
func WithLUTs(lib *lut.Library) ToolOption {
	return func(c *toolConfig) { c.luts = lib }
}

const (
	lutAddOwn = "Add my own .cube (write its path below)"
	lutNone   = "No LUT"
)

// lutChoices is what the LUT question offers for one clip.
type lutChoices struct {
	camera      string // "DJI O4P"; "" when unknown
	suggested   *lut.Entry
	others      []lut.Entry
	downloadURL string
	folder      string
}

func suggestedLabel(e *lut.Entry) string { return e.Name + " (suggested for this clip)" }

func (c lutChoices) labels() []string {
	var l []string
	if c.suggested != nil {
		l = append(l, suggestedLabel(c.suggested))
	}
	for _, e := range c.others {
		l = append(l, e.Name)
	}
	return append(l, lutAddOwn, lutNone)
}

// message is the question text. With no LUT for this camera it says where the
// maker publishes one; non-DJI cameras get no link.
func (c lutChoices) message() string {
	var b strings.Builder
	b.WriteString("Apply a color LUT to the stabilized video? Camera LUTs like DJI's D-Log M to Rec.709 are only for clips " +
		"recorded in that flat log profile; on normal footage they oversaturate.")
	if c.suggested == nil {
		if len(c.others) == 0 {
			fmt.Fprintf(&b, " You have no LUTs saved yet (folder: %s).", c.folder)
		}
		if c.downloadURL != "" {
			fmt.Fprintf(&b, " The official LUT for this camera is a free download: %s", c.downloadURL)
		}
	}
	return b.String()
}

// buildLUTChoices lists the library and probes the first video for its camera.
func buildLUTChoices(ctx context.Context, be backend.Backend, lib *lut.Library, inputs []string) (lutChoices, error) {
	c := lutChoices{folder: lib.Dir}
	entries, err := lib.List()
	if err != nil {
		return c, err
	}
	for _, in := range inputs {
		if isGyroflowFile(in) {
			continue
		}
		if m, err := be.ProbeMetadata(ctx, in, backend.ProbeOptions{}); err == nil {
			make, model := m.Summary.Camera.Make, m.Summary.Camera.Model
			c.camera = strings.TrimSpace(make + " " + model)
			c.suggested = lut.Suggest(entries, make, model)
			c.downloadURL = lut.DownloadURL(make, model)
		}
		break
	}
	for _, e := range entries {
		if c.suggested == nil || e.Path != c.suggested.Path {
			c.others = append(c.others, e)
		}
	}
	return c, nil
}

// lutValue turns a choice label (plus the typed path for "add my own") into a
// render_start lut value.
func (c lutChoices) lutValue(label, path string) (string, error) {
	switch {
	case label == lutNone:
		return lut.None, nil
	case label == lutAddOwn:
		if strings.TrimSpace(path) == "" {
			return "", errors.New("no .cube path was given for your own LUT")
		}
		return strings.TrimSpace(path), nil
	case c.suggested != nil && label == suggestedLabel(c.suggested):
		return c.suggested.Name, nil
	}
	for _, e := range c.others {
		if e.Name == label {
			return e.Name, nil
		}
	}
	return "", fmt.Errorf("unknown LUT answer %q", label)
}

// askLUTYourselfError tells the agent to ask the LUT question in chat, as for
// the settings form (the Claude desktop app declines MCP forms unshown).
func (c lutChoices) askYourselfError(why string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "render not started: %s. Ask the user this yourself (e.g. with your question tool), then call render_start "+
		"again with the same arguments plus lut. %s\nOptions:", why, c.message())
	if c.suggested != nil {
		fmt.Fprintf(&b, "\n  - %s → lut: %q", suggestedLabel(c.suggested), c.suggested.Name)
	}
	for _, e := range c.others {
		fmt.Fprintf(&b, "\n  - %s → lut: %q", e.Name, e.Name)
	}
	fmt.Fprintf(&b, "\n  - Add their own LUT → lut: \"<path to the .cube>\" (it is checked and saved to %s for next time)", c.folder)
	fmt.Fprintf(&b, "\n  - No LUT → lut: %q", lut.None)
	return errors.New(b.String())
}

// askLUT asks the user whether to apply a LUT and returns the lut value.
func askLUT(ctx context.Context, ss *mcp.ServerSession, c lutChoices) (string, error) {
	if !canElicitForm(ss) {
		return "", c.askYourselfError("the user has not said whether to apply a LUT")
	}
	labels := c.labels()
	res, err := ss.Elicit(ctx, &mcp.ElicitParams{
		Message: c.message(),
		RequestedSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"lut": map[string]any{
					"type": "string", "title": "LUT", "enum": labels, "default": labels[0],
					"description": "Color LUT applied after stabilizing.",
				},
				"lut_path": map[string]any{
					"type": "string", "title": "Your .cube file",
					"description": "Only for \"" + lutAddOwn + "\": full path to the .cube file.",
				},
			},
			"required": []string{"lut"},
		},
	})
	if err != nil {
		return "", c.askYourselfError(fmt.Sprintf("the LUT form failed (%v)", err))
	}
	if res.Action != "accept" || len(res.Content) == 0 {
		return "", c.askYourselfError(fmt.Sprintf("the LUT form was not answered (%s; this client may not show MCP forms)", res.Action))
	}
	label, _ := res.Content["lut"].(string)
	path, _ := res.Content["lut_path"].(string)
	return c.lutValue(label, path)
}

// resolveLUT turns a lut value into the .cube path for the backend, saving a
// LUT from outside the library into it, tagged with the clip's camera.
func resolveLUT(lib *lut.Library, value, camera string) (string, error) {
	if value == "" || strings.EqualFold(value, lut.None) {
		return "", nil
	}
	if lib == nil {
		return "", errors.New("LUTs are not available: applying one needs ffmpeg (brew install ffmpeg)")
	}
	path, err := lib.Resolve(value)
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(lib.Dir, path); err != nil || strings.HasPrefix(rel, "..") {
		e, err := lib.Add(path, camera)
		if err != nil {
			return "", err
		}
		return e.Path, nil
	}
	return path, nil
}

type listLUTsArgs struct {
	Input string `json:"input,omitempty" jsonschema:"optional video whose camera picks the suggested LUT"`
}

type listLUTsOut struct {
	Folder      string      `json:"folder"`
	LUTs        []lut.Entry `json:"luts"`
	Suggested   string      `json:"suggested,omitempty"`
	Camera      string      `json:"camera,omitempty"`
	DownloadURL string      `json:"download_url,omitempty"`
	// Looks are the saved color looks, by name.
	Looks map[string]backend.ColorAdjust `json:"looks,omitempty"`
}

func registerLUTTools(s *mcp.Server, be backend.Backend, lib *lut.Library) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_luts",
		Description: "List the .cube LUTs in the user's LUT folder and the saved color looks. With input, also the LUT suggested " +
			"for that clip's camera and, for DJI cameras, where to download the official LUT.",
		Annotations: readOnlyAnn("List LUTs"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a listLUTsArgs) (*mcp.CallToolResult, listLUTsOut, error) {
		var inputs []string
		if a.Input != "" {
			inputs = []string{a.Input}
		}
		c, err := buildLUTChoices(ctx, be, lib, inputs)
		if err != nil {
			return nil, listLUTsOut{}, err
		}
		out := listLUTsOut{Folder: lib.Dir, LUTs: c.others, Camera: c.camera, DownloadURL: c.downloadURL, Looks: lib.Looks()}
		if c.suggested != nil {
			out.Suggested = c.suggested.Name
			out.LUTs = append([]lut.Entry{*c.suggested}, c.others...)
		}
		if out.LUTs == nil {
			out.LUTs = []lut.Entry{}
		}
		return nil, out, nil
	})
}
