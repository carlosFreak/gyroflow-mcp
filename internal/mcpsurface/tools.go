// Package mcpsurface registers MCP tools/resources/prompts over the engine.
package mcpsurface

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
	"github.com/carlosFreak/gyroflow-mcp/internal/jobengine"
	"github.com/carlosFreak/gyroflow-mcp/internal/lens"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DoctorResult is the structured result of the gyroflow_doctor tool.
type DoctorResult struct {
	// Found is true when a gyroflow binary was detected.
	Found bool `json:"found"`
	// Usable is true when Found=true and the binary can read files: not sandboxed,
	// or sandboxed with staging available (see Advice).
	Usable    bool   `json:"usable"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
	Sandboxed bool   `json:"sandboxed"`
	AppStore  bool   `json:"app_store"`
	Advice    string `json:"advice,omitempty"`
}

// DoctorFunc is a function that inspects the gyroflow binary and returns a DoctorResult.
type DoctorFunc func(ctx context.Context) DoctorResult

// serverInstructions is the operational guidance advertised to MCP clients via
// ServerOptions.Instructions. Kept tight and accurate to the actual tools.
const serverInstructions = "gyroflow-mcp drives the Gyroflow CLI to stabilize gyro-equipped video. " +
	"Gyroflow must be installed; the sandboxed App Store build works through a temporary work folder " +
	"(no export_project or export_stmap). If any tool reports it is unusable or " +
	"\"Unable to read the video file\", call gyroflow_doctor and follow its advice. " +
	"If the user gave no stabilization settings and no .gyroflow preset, call render_start without config: the server asks " +
	"the user for smoothing, aspect ratio, crop, horizon lock and codec in a form. If your client cannot show " +
	"MCP forms, ask the user those five settings yourself before render_start. If the user wants the " +
	"defaults, pass config: {}. In a batch, reuse the config returned by the first render_start. " +
	"render_start also asks whether to apply a color LUT (form, or an error listing the options for you to ask the user " +
	"yourself); pass lut with a list_luts name, a .cube path, or none, and reuse it for the rest of a batch. " +
	"Renders are ASYNC: render_start returns a job_id immediately - then call render_wait, which " +
	"streams a live progress bar to clients that send a progressToken, or poll render_status. " +
	"Every time render_wait returns with state running, write the user one short line with the " +
	"percent of the current file and of the total, then call it again with the default timeout_s; " +
	"do not wait silently, use a longer timeout or run the render in the background unless the user " +
	"asks. Use render_cancel and render_list to manage " +
	"jobs. Typical flow: gyroflow_doctor → probe_metadata → find_lens_profile → render_start → render_wait."

// ServerInstructions returns the operational guidance for ServerOptions.Instructions.
func ServerInstructions() string { return serverInstructions }

// boolPtr returns a pointer to b (ToolAnnotations.DestructiveHint is *bool).
func boolPtr(b bool) *bool { return &b }

// readOnlyAnn marks a tool with no caller-visible side effects.
func readOnlyAnn(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true}
}

// destructiveAnn marks a tool that writes files, spawns a render, or mutates
// job state. ReadOnlyHint stays false; DestructiveHint is set explicitly.
func destructiveAnn(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, DestructiveHint: boolPtr(true)}
}

type startArgs struct {
	Inputs           []string                `json:"inputs"                      jsonschema:"video and/or .gyroflow files to stabilize"`
	PresetPath       string                  `json:"preset_path,omitempty"        jsonschema:"optional on-disk .gyroflow preset; the typed config overlays it"`
	GyroFile         string                  `json:"gyro_file,omitempty"          jsonschema:"optional external gyro data file"`
	Overwrite        bool                    `json:"overwrite,omitempty"          jsonschema:"overwrite existing output"`
	Suffix           string                  `json:"suffix,omitempty"             jsonschema:"output filename suffix (gyroflow -t), default _stabilized; the output aspect ratio is appended, e.g. _stabilized_1x1"`
	Config           *backend.GyroflowConfig `json:"config,omitempty"             jsonschema:"full typed Gyroflow configuration - see the gyroflow://capabilities resource for every field's meaning, unit, range and enum values. Set only what the user asked for."`
	ProcessingDevice *int                    `json:"processing_device,omitempty"  jsonschema:"GPU processing device index (gyroflow -b); omit for default"`
	RenderingDevice  string                  `json:"rendering_device,omitempty"   jsonschema:"GPU vendor for rendering (gyroflow -r): nvidia, intel, amd, or 'apple m'"`
	NoGPUDecoding    bool                    `json:"no_gpu_decoding,omitempty"    jsonschema:"disable GPU video decoding (gyroflow --no-gpu-decoding) - use only if decode fails"`
	OutParams        map[string]any          `json:"out_params,omitempty"         jsonschema:"advanced: raw gyroflow -p output params (prefer config.output)"`
	SyncParams       map[string]any          `json:"sync_params,omitempty"        jsonschema:"advanced: raw gyroflow -s sync params (prefer config.sync)"`
	LUT              string                  `json:"lut,omitempty"                jsonschema:"color LUT applied after stabilizing: a name from list_luts, a path to a .cube (saved to the LUT folder), or none. Omit when the user has not said: the server then asks."`
}

type startOut struct {
	JobID string `json:"job_id"`
	// Config is set when the settings came from the user's form, so the agent
	// can reuse it for the other files of a batch without asking again.
	Config *backend.GyroflowConfig `json:"config,omitempty" jsonschema:"settings the user chose in the form; pass this as config to the remaining render_start calls of the same batch"`
	// LUT is the user's LUT answer, to reuse for the rest of a batch.
	LUT string `json:"lut,omitempty" jsonschema:"LUT the user chose; pass this as lut to the remaining render_start calls of the same batch"`
}

type idArgs struct {
	JobID string `json:"job_id" jsonschema:"the render job id"`
}

type statusOut struct {
	State   string  `json:"state"`
	Percent float64 `json:"percent"`
	Frame   int     `json:"frame"`
	Total   int     `json:"total"`
	Stage   string  `json:"stage"`
	// ProgressBar is ready-to-show text, one line per bar; clients relay it verbatim.
	ProgressBar string    `json:"progress_bar"`
	Files       []fileOut `json:"files,omitempty"`
	Outputs     []string  `json:"outputs,omitempty"`
	Error       string    `json:"error,omitempty"`
}

// fileOut is the progress of one input of a render job.
type fileOut struct {
	Name    string  `json:"name"`
	State   string  `json:"state"`
	Percent float64 `json:"percent"`
	Frame   int     `json:"frame"`
	Total   int     `json:"total"`
}

func jobToStatus(j jobengine.Job) statusOut {
	var files []fileOut
	for _, f := range j.Progress.Files {
		files = append(files, fileOut{Name: f.Name, State: string(f.State), Percent: f.Percent, Frame: f.Frame, Total: f.Total})
	}
	return statusOut{
		State:       string(j.State),
		Percent:     j.Progress.Percent,
		Frame:       j.Progress.Frame,
		Total:       j.Progress.Total,
		Stage:       j.Progress.Stage,
		ProgressBar: progressBar(j),
		Files:       files,
		Outputs:     j.OutputPaths,
		Error:       j.Err,
	}
}

type waitArgs struct {
	JobID    string `json:"job_id"              jsonschema:"the render job id"`
	TimeoutS int    `json:"timeout_s,omitempty" jsonschema:"seconds to wait before returning the current status (default 55, max 600); the render keeps running either way"`
}

const (
	defaultWaitTimeout = 55 * time.Second
	maxWaitTimeout     = 600 * time.Second
	waitPollInterval   = time.Second
)

// waitForJob blocks until the job is terminal, timeout elapses or ctx is done,
// calling notify whenever the progress bar changes. It never affects the render.
func waitForJob(ctx context.Context, e *jobengine.Engine, id string, timeout time.Duration, notify func(jobengine.Job)) (jobengine.Job, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(waitPollInterval)
	defer tick.Stop()

	lastBar := ""
	for {
		j, ok := e.Status(id)
		if !ok {
			return jobengine.Job{}, fmt.Errorf("unknown job_id: %s", id)
		}
		if bar := progressBar(j); bar != lastBar {
			lastBar = bar
			notify(j)
		}
		if j.State == jobengine.StateCompleted || j.State == jobengine.StateFailed || j.State == jobengine.StateCancelled {
			return j, nil
		}
		select {
		case <-ctx.Done():
			return j, ctx.Err()
		case <-deadline.C:
			// j may be up to a poll old; report the state at the deadline.
			if latest, ok := e.Status(id); ok {
				return latest, nil
			}
			return j, nil
		case <-tick.C:
		}
	}
}

// carriesSettings reports whether the render already has the user's settings:
// a .gyroflow preset or project input. Lens profiles from find_lens_profile
// are .json and do not count.
func carriesSettings(a startArgs) bool {
	if isGyroflowFile(a.PresetPath) {
		return true
	}
	for _, in := range a.Inputs {
		if isGyroflowFile(in) {
			return true
		}
	}
	return false
}

func isGyroflowFile(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".gyroflow")
}

// resolveAspectRatio returns cfg with output.aspect_ratio turned into
// output_width/height for the inputs' resolution. cfg itself is left as is,
// so the ratio can be reused for a batch at another resolution.
func resolveAspectRatio(ctx context.Context, be backend.Backend, inputs []string, cfg *backend.GyroflowConfig) (*backend.GyroflowConfig, error) {
	if cfg == nil || cfg.Output == nil || cfg.Output.AspectRatio == nil {
		return cfg, nil
	}
	aw, ah, err := backend.ParseAspectRatio(*cfg.Output.AspectRatio)
	if err != nil {
		return nil, err
	}
	srcW, srcH := 0, 0
	for _, in := range inputs {
		m, err := be.ProbeMetadata(ctx, in, backend.ProbeOptions{})
		if err != nil {
			return nil, fmt.Errorf("output.aspect_ratio needs the source resolution: %w", err)
		}
		w, h := m.Summary.Width, m.Summary.Height
		if w <= 0 || h <= 0 {
			return nil, fmt.Errorf("output.aspect_ratio: no resolution found for %s; set output_width/output_height instead", in)
		}
		if srcW != 0 && (w != srcW || h != srcH) {
			return nil, fmt.Errorf("output.aspect_ratio: inputs have different resolutions (%dx%d and %dx%d); call render_start once per resolution", srcW, srcH, w, h)
		}
		srcW, srcH = w, h
	}
	w, h := backend.FitAspectRatio(srcW, srcH, aw, ah)
	out := *cfg.Output
	out.AspectRatio = nil
	out.OutputWidth, out.OutputHeight = &w, &h
	resolved := *cfg
	resolved.Output = &out
	return &resolved, nil
}

// RegisterTools adds render_start, render_status, render_cancel, render_list,
// gyroflow_doctor, probe_metadata, export_project, and find_lens_profile to the
// given server. idx may be nil when the lens database could not be loaded
// (e.g. offline at startup); find_lens_profile then returns an actionable error.
func RegisterTools(s *mcp.Server, e *jobengine.Engine, doc DoctorFunc, idx *lens.Index, opts ...ToolOption) {
	var tc toolConfig
	for _, o := range opts {
		o(&tc)
	}
	if tc.luts != nil {
		registerLUTTools(s, e.Backend(), tc.luts)
	}
	mcp.AddTool(s, &mcp.Tool{
		Name: "render_start",
		Description: "Start a video stabilization render. Returns a job_id immediately; follow it with render_wait, reporting progress to the user after each call. " +
			"Omit config when the user gave no settings: unless a .gyroflow preset or project is passed, the server then asks the user for the main settings in a form " +
			"(clients without form support must ask the user themselves first). Pass config: {} to use Gyroflow defaults without asking.",
		Annotations: destructiveAnn("Start render"),
	}, func(ctx context.Context, req *mcp.CallToolRequest, a startArgs) (*mcp.CallToolResult, startOut, error) {
		// No progress notifications here: this request ends as soon as the job is
		// queued, and MCP only allows progress for active requests. render_wait
		// stays open during the render and streams it under its own token.
		var asked bool
		if a.Config == nil && !carriesSettings(a) {
			cfg, ok, err := askRenderSettings(ctx, req.Session)
			if err != nil {
				return nil, startOut{}, err
			}
			a.Config, asked = cfg, ok
		}
		if a.Config != nil {
			if err := backend.ValidateGyroflowConfig(a.Config); err != nil {
				return nil, startOut{}, fmt.Errorf("invalid config: %w", err)
			}
		}
		resolved, err := resolveAspectRatio(ctx, e.Backend(), a.Inputs, a.Config)
		if err != nil {
			return nil, startOut{}, err
		}
		var lutPath string
		askedLUT := a.LUT == "" && tc.luts != nil
		if a.LUT != "" || tc.luts != nil {
			var choices lutChoices
			if tc.luts != nil {
				if choices, err = buildLUTChoices(ctx, e.Backend(), tc.luts, a.Inputs); err != nil {
					return nil, startOut{}, err
				}
			}
			if askedLUT {
				if a.LUT, err = askLUT(ctx, req.Session, choices); err != nil {
					return nil, startOut{}, err
				}
			}
			if lutPath, err = resolveLUT(tc.luts, a.LUT, choices.camera); err != nil {
				return nil, startOut{}, err
			}
		}
		id := e.StartStabilize(backend.StabilizeRequest{
			Inputs:           a.Inputs,
			PresetPath:       a.PresetPath,
			GyroFile:         a.GyroFile,
			Overwrite:        a.Overwrite,
			Suffix:           withAspect(ctx, e.Backend(), a, resolved),
			Config:           resolved,
			ProcessingDevice: a.ProcessingDevice,
			RenderingDevice:  a.RenderingDevice,
			NoGPUDecoding:    a.NoGPUDecoding,
			OutParams:        a.OutParams,
			SyncParams:       a.SyncParams,
			LUT:              lutPath,
		}, nil)
		out := startOut{JobID: id}
		if askedLUT {
			out.LUT = a.LUT
		}
		if asked {
			out.Config = a.Config
			if out.Config == nil {
				out.Config = &backend.GyroflowConfig{}
			}
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "render_status",
		Description: "Get progress and state for a render job.",
		Annotations: readOnlyAnn("Render status"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a idArgs) (*mcp.CallToolResult, statusOut, error) {
		j, ok := e.Status(a.JobID)
		if !ok {
			return nil, statusOut{}, fmt.Errorf("unknown job_id: %s", a.JobID)
		}
		return nil, jobToStatus(j), nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "render_wait",
		Description: "Wait for a render job, streaming a live progress bar as MCP progress notifications when the " +
			"client sends a progressToken. Returns the status when the job ends or timeout_s elapses. If state is still " +
			"running, write the user one short line with the percent of the current file and of the total, then call " +
			"again with the default timeout_s. Cancelling this call does not cancel the render.",
		Annotations: readOnlyAnn("Wait for render"),
	}, func(ctx context.Context, req *mcp.CallToolRequest, a waitArgs) (*mcp.CallToolResult, statusOut, error) {
		timeout := defaultWaitTimeout
		if a.TimeoutS > 0 {
			timeout = min(time.Duration(a.TimeoutS)*time.Second, maxWaitTimeout)
		}
		notify := func(jobengine.Job) {}
		if tok := req.Params.GetProgressToken(); tok != nil && req.Session != nil {
			notify = func(j jobengine.Job) {
				percent := j.Progress.Percent
				if j.State == jobengine.StateCompleted {
					percent = 100
				}
				// Errors are ignored: a lost notification must not end the wait.
				_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
					ProgressToken: tok,
					Progress:      percent,
					Total:         100,
					Message:       strings.ReplaceAll(progressBar(j), "\n", " | "),
				})
			}
		}
		j, err := waitForJob(ctx, e, a.JobID, timeout, notify)
		if err != nil {
			return nil, statusOut{}, err
		}
		return nil, jobToStatus(j), nil
	})

	type cancelOut struct {
		Cancelled bool `json:"cancelled"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "render_cancel",
		Description: "Cancel a running render job.",
		Annotations: destructiveAnn("Cancel render"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a idArgs) (*mcp.CallToolResult, cancelOut, error) {
		return nil, cancelOut{Cancelled: e.Cancel(a.JobID)}, nil
	})

	type listOut struct {
		Jobs []statusOut `json:"jobs"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "render_list",
		Description: "List all render jobs and their states.",
		Annotations: readOnlyAnn("List renders"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listOut, error) {
		jobs := e.List()
		out := make([]statusOut, 0, len(jobs))
		for _, j := range jobs {
			out = append(out, jobToStatus(j))
		}
		return nil, listOut{Jobs: out}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "gyroflow_doctor",
		Description: "Inspect the detected gyroflow binary: path, version, sandbox status, and actionable advice.",
		Annotations: readOnlyAnn("Gyroflow doctor"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, DoctorResult, error) {
		return nil, doc(ctx), nil
	})

	type probeArgs struct {
		Input  string `json:"input"            jsonschema:"video or .gyroflow file to probe"`
		Kind   int    `json:"kind,omitempty"   jsonschema:"metadata kind: 2 parsed summary (default), 1 full, 3 camera data"`
		Fields string `json:"fields,omitempty" jsonschema:"optional --export-metadata-fields JSON to limit exported camera fields"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name: "probe_metadata",
		Description: "Inspect a video or gyroflow project: returns {summary:{camera, lens, " +
			"has_gyro, resolution, fps, duration, frame_count}, raw_path}. The raw blob " +
			"is never inlined - read raw_path only if you need the full telemetry. This " +
			"is the SAME shape as the project://{path}/metadata resource.",
		Annotations: readOnlyAnn("Probe metadata"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a probeArgs) (*mcp.CallToolResult, *backend.MetadataResult, error) {
		// Return *backend.MetadataResult directly so probe_metadata and the
		// project://{path}/metadata resource emit the IDENTICAL JSON shape
		// {summary:{…}, raw_path} (MetadataResult already carries those tags).
		r, err := e.Backend().ProbeMetadata(ctx, a.Input, backend.ProbeOptions{Kind: a.Kind, Fields: a.Fields})
		if err != nil {
			return nil, nil, fmt.Errorf("probe_metadata: %w", err)
		}
		return nil, r, nil
	})

	type exportArgs struct {
		Input string `json:"input"               jsonschema:"video or .gyroflow project file"`
		Kind  int    `json:"kind"                jsonschema:"export kind: 1=default 2=gyro 3=proc 4=video"`
		Out   string `json:"out,omitempty"       jsonschema:"optional output path"`
	}
	type exportOut struct {
		OK bool `json:"ok"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "export_project",
		Description: "Export a gyroflow project to a file (kinds: 1=default, 2=gyro, 3=processed, 4=video).",
		Annotations: destructiveAnn("Export project"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a exportArgs) (*mcp.CallToolResult, exportOut, error) {
		_, err := e.Backend().ExportProject(ctx, backend.ExportRequest{
			Input: a.Input,
			Kind:  backend.ExportKind(a.Kind),
			Out:   a.Out,
		})
		if err != nil {
			return nil, exportOut{}, fmt.Errorf("export_project: %w", err)
		}
		return nil, exportOut{OK: true}, nil
	})

	type findLensArgs struct {
		Query  string  `json:"query"            jsonschema:"camera brand, model, lens, or identifier substring to search for"`
		Width  int     `json:"width,omitempty"  jsonschema:"recorded video width in px (from probe_metadata) to rank the nearest profile"`
		Height int     `json:"height,omitempty" jsonschema:"recorded video height in px (from probe_metadata) to rank the nearest profile"`
		FPS    float64 `json:"fps,omitempty"    jsonschema:"recorded frame rate (from probe_metadata) to tie-break the nearest profile"`
	}
	// findLensMatch is one ranked profile. Fields are the real lens-profile
	// keys (camera_brand/model, lens_model, calib_dimension, fps, identifier).
	type findLensMatch struct {
		Path       string  `json:"path"`
		Display    string  `json:"display"`
		Identifier string  `json:"identifier,omitempty"`
		Brand      string  `json:"brand"`
		Model      string  `json:"model"`
		Lens       string  `json:"lens,omitempty"`
		Width      int     `json:"width"`
		Height     int     `json:"height"`
		FPS        float64 `json:"fps"`
	}
	type findLensHit struct {
		Path    string `json:"path"`
		Display string `json:"display"`
	}
	type findLensOut struct {
		// Best is the single nearest match (null when there are no matches).
		Best *findLensMatch `json:"best"`
		// Alternatives is the ranked remainder (capped at 20).
		Alternatives []findLensMatch `json:"alternatives"`
		// Hits is the legacy flat list (path+display), kept for compatibility.
		Hits []findLensHit `json:"hits"`
		// GyroflowAutoLoads is true when the queried camera brand has its lens
		// profile auto-loaded by Gyroflow from embedded video metadata.
		GyroflowAutoLoads bool   `json:"gyroflow_auto_loads"`
		Advice            string `json:"advice,omitempty"`
	}
	toMatch := func(h lens.Hit) findLensMatch {
		return findLensMatch{
			Path: h.Path, Display: h.Display, Identifier: h.Identifier,
			Brand: h.Brand, Model: h.Model, Lens: h.Lens,
			Width: h.Width, Height: h.Height, FPS: h.FPS,
		}
	}
	mcp.AddTool(s, &mcp.Tool{
		Name: "find_lens_profile",
		Description: "Find the Gyroflow lens profile for a camera. Pass width/height/fps " +
			"(from probe_metadata) to get a single ranked `best` match plus ranked " +
			"`alternatives`. `gyroflow_auto_loads` reports whether the camera's profile " +
			"is loaded automatically from embedded metadata (no preset needed).",
		Annotations: readOnlyAnn("Find lens profile"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a findLensArgs) (*mcp.CallToolResult, findLensOut, error) {
		if idx == nil {
			return nil, findLensOut{}, fmt.Errorf("%s", lensUnavailableText)
		}
		best, alts := idx.RankedSearch(a.Query, a.Width, a.Height, a.FPS)
		out := findLensOut{
			Alternatives:      make([]findLensMatch, 0, len(alts)),
			Hits:              make([]findLensHit, 0, len(alts)+1),
			GyroflowAutoLoads: lens.GyroflowAutoLoads(a.Query),
		}
		if out.GyroflowAutoLoads {
			out.Advice = lens.AutoLoadAdvice
		}
		if best != nil {
			m := toMatch(*best)
			out.Best = &m
			out.Hits = append(out.Hits, findLensHit{Path: best.Path, Display: best.Display})
		}
		for _, h := range alts {
			out.Alternatives = append(out.Alternatives, toMatch(h))
			out.Hits = append(out.Hits, findLensHit{Path: h.Path, Display: h.Display})
		}
		// No match AND no auto-load → give the agent a concrete next step
		// instead of an empty advice that forces improvisation.
		if best == nil && !out.GyroflowAutoLoads {
			out.Advice = fmt.Sprintf("no lens profile matched %q - broaden the query "+
				"(brand only, e.g. \"GoPro\"), or read the lens://profiles resource to browse", a.Query)
		}
		return nil, out, nil
	})

	type stmapArgs struct {
		Input     string `json:"input"      jsonschema:"video or .gyroflow project to export an STMap for"`
		Type      int    `json:"type"       jsonschema:"1 = single frame, 2 = all frames"`
		OutFolder string `json:"out_folder" jsonschema:"destination folder for the STMap files"`
	}
	type stmapOut struct {
		OK bool `json:"ok"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "export_stmap",
		Description: "Export an STMap (per-pixel distortion map) for compositing pipelines (Nuke/Fusion/Resolve). type: 1=single frame, 2=all frames.",
		Annotations: destructiveAnn("Export STMap"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a stmapArgs) (*mcp.CallToolResult, stmapOut, error) {
		_, err := e.Backend().ExportSTMap(ctx, backend.STMapRequest{Input: a.Input, Type: a.Type, OutFolder: a.OutFolder})
		if err != nil {
			return nil, stmapOut{}, fmt.Errorf("export_stmap: %w", err)
		}
		return nil, stmapOut{OK: true}, nil
	})
}
