package mcpsurface

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// settingsChoice is one option of a settings question. apply is nil for the
// option that keeps Gyroflow's own default.
type settingsChoice struct {
	label string
	apply func(*backend.GyroflowConfig)
}

// settingsQuestion is one field of the form render_start shows when the agent
// passes no config. The first choice is the recommended one.
type settingsQuestion struct {
	key, title, description string
	choices                 []settingsChoice
}

func f64(v float64) *float64  { return &v }
func intPtr(v int) *int       { return &v }
func strPtr(v string) *string { return &v }

func smoothness(v float64) func(*backend.GyroflowConfig) {
	return func(c *backend.GyroflowConfig) {
		c.Smoothing = &backend.SmoothingConfig{Method: "Default", Smoothness: f64(v)}
	}
}

func zoomWindow(v float64) func(*backend.GyroflowConfig) {
	return func(c *backend.GyroflowConfig) {
		c.Zoom = &backend.ZoomConfig{AdaptiveZoomWindow: f64(v)}
	}
}

func horizon(amount float64) func(*backend.GyroflowConfig) {
	return func(c *backend.GyroflowConfig) {
		c.HorizonLock = &backend.HorizonLockConfig{Amount: f64(amount)}
	}
}

// output returns c.Output, creating it, so several answers can share it.
func output(c *backend.GyroflowConfig) *backend.OutputConfig {
	if c.Output == nil {
		c.Output = &backend.OutputConfig{}
	}
	return c.Output
}

func codec(name string) func(*backend.GyroflowConfig) {
	return func(c *backend.GyroflowConfig) { output(c).Codec = strPtr(name) }
}

func aspect(ratio string) func(*backend.GyroflowConfig) {
	return func(c *backend.GyroflowConfig) { output(c).AspectRatio = strPtr(ratio) }
}

// settingsQuestions are the main render settings, in form order. Values follow
// the gyroflow://capabilities reference.
var settingsQuestions = []settingsQuestion{
	{
		key: "smoothing", title: "Smoothing",
		description: "How much camera motion to remove. Stronger smoothing crops more.",
		choices: []settingsChoice{
			{label: "Medium - Gyroflow default (recommended)"},
			{label: "Light - keep natural motion", apply: smoothness(0.25)},
			{label: "Strong - gimbal-like", apply: smoothness(0.85)},
			{label: "Locked - fixed camera", apply: func(c *backend.GyroflowConfig) {
				c.Smoothing = &backend.SmoothingConfig{Method: "Fixed camera"}
			}},
		},
	},
	{
		key: "aspect", title: "Aspect ratio",
		description: "Shape of the output frame, cropped from the source.",
		choices: []settingsChoice{
			{label: "Same as source (recommended)"},
			{label: "16:9 - landscape", apply: aspect("16:9")},
			{label: "9:16 - vertical", apply: aspect("9:16")},
			{label: "1:1 - square", apply: aspect("1:1")},
			{label: "4:5 - portrait", apply: aspect("4:5")},
		},
	},
	{
		key: "crop", title: "Crop",
		description: "How to hide the borders the stabilization reveals.",
		choices: []settingsChoice{
			{label: "Dynamic zoom - Gyroflow default (recommended)"},
			{label: "Static zoom - fixed crop, less zoom", apply: zoomWindow(-1)},
			{label: "No zoom - full view, visible borders", apply: func(c *backend.GyroflowConfig) {
				zoomWindow(0)(c)
				c.Background = &backend.BackgroundConfig{Mode: intPtr(1)}
			}},
		},
	},
	{
		key: "horizon", title: "Horizon lock",
		description: "Keep the horizon level.",
		choices: []settingsChoice{
			{label: "Off (recommended)"},
			{label: "Partial", apply: horizon(50)},
			{label: "Full - always level", apply: horizon(100)},
		},
	},
	{
		key: "output", title: "Codec",
		description: "Video codec of the stabilized file.",
		choices: []settingsChoice{
			{label: "Same as source (recommended)"},
			{label: "H.264 - most compatible", apply: codec("H.264/AVC")},
			{label: "H.265/HEVC - smaller files", apply: codec("H.265/HEVC")},
			{label: "ProRes - for editing", apply: codec("ProRes")},
		},
	},
}

// settingsSchema is the flat elicitation schema for settingsQuestions.
func settingsSchema() map[string]any {
	props := map[string]any{}
	required := make([]string, 0, len(settingsQuestions))
	for _, q := range settingsQuestions {
		labels := make([]string, 0, len(q.choices))
		for _, c := range q.choices {
			labels = append(labels, c.label)
		}
		props[q.key] = map[string]any{
			"type":        "string",
			"title":       q.title,
			"description": q.description,
			"enum":        labels,
			"default":     labels[0],
		}
		required = append(required, q.key)
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

// configFromAnswers builds a config from the submitted form. It returns nil
// when every answer keeps Gyroflow's default, so the render is the same as
// one started without config.
func configFromAnswers(answers map[string]any) (*backend.GyroflowConfig, error) {
	cfg := &backend.GyroflowConfig{}
	for _, q := range settingsQuestions {
		v, ok := answers[q.key].(string)
		if !ok {
			continue // missing answer: keep the default
		}
		found := false
		for _, c := range q.choices {
			if c.label == v {
				found = true
				if c.apply != nil {
					c.apply(cfg)
				}
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown %s answer %q", q.key, v)
		}
	}
	if reflect.ValueOf(*cfg).IsZero() {
		return nil, nil
	}
	return cfg, nil
}

// canElicitForm reports whether the client accepts form elicitation requests.
func canElicitForm(ss *mcp.ServerSession) bool {
	if ss == nil {
		return false
	}
	p := ss.InitializeParams()
	if p == nil || p.Capabilities == nil || p.Capabilities.Elicitation == nil {
		return false
	}
	e := p.Capabilities.Elicitation
	// Neither mode set means form (see mcp.ElicitationCapabilities).
	return e.Form != nil || e.URL == nil
}

// askYourselfError tells the agent to ask the settings in chat. Some clients
// advertise form support but decline or cancel every form without showing it
// (the Claude desktop app does both), so an unanswered form is neither consent
// to the defaults nor a refusal to render.
func askYourselfError(why string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "render not started: %s. Ask the user these settings yourself (e.g. with your question tool), "+
		"then call render_start again with the matching config merged into one object - or config: {} if they want Gyroflow defaults. "+
		"If the user does not want to render, stop.", why)
	for _, q := range settingsQuestions {
		fmt.Fprintf(&b, "\n%s (%s):", q.title, q.description)
		for _, c := range q.choices {
			patch := "{}"
			if c.apply != nil {
				cfg := &backend.GyroflowConfig{}
				c.apply(cfg)
				if j, err := json.Marshal(cfg); err == nil {
					patch = string(j)
				}
			}
			fmt.Fprintf(&b, "\n  - %s → %s", c.label, patch)
		}
	}
	return errors.New(b.String())
}

// askRenderSettings asks the user for the main render settings. asked is false
// when the client cannot show forms; the agent is then expected to have asked
// the user itself (see serverInstructions). A form that is not answered
// (declined, cancelled, empty or failed) returns askYourselfError.
func askRenderSettings(ctx context.Context, ss *mcp.ServerSession) (cfg *backend.GyroflowConfig, asked bool, err error) {
	if !canElicitForm(ss) {
		return nil, false, nil
	}
	res, err := ss.Elicit(ctx, &mcp.ElicitParams{
		Message:         "Choose the stabilization settings for this render.",
		RequestedSchema: settingsSchema(),
	})
	if err != nil {
		return nil, true, askYourselfError(fmt.Sprintf("the settings form failed (%v)", err))
	}
	if res.Action == "accept" && len(res.Content) > 0 {
		cfg, err := configFromAnswers(res.Content)
		return cfg, true, err
	}
	return nil, true, askYourselfError(fmt.Sprintf("the settings form was not answered (%s; this client may not show MCP forms)", res.Action))
}
