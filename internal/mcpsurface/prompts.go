package mcpsurface

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Prompt API pinned via `go doc` (go-sdk v1.6.0):
//
//	Server.AddPrompt(*Prompt, PromptHandler)
//	Prompt{ Name, Description string; Arguments []*PromptArgument }
//	PromptArgument{ Name, Description string; Required bool }
//	PromptHandler = func(context.Context, *GetPromptRequest) (*GetPromptResult, error)
//	GetPromptRequest = ServerRequest[*GetPromptParams]
//	GetPromptParams{ Name string; Arguments map[string]string }
//	GetPromptResult{ Description string; Messages []*PromptMessage }
//	PromptMessage{ Content Content; Role Role }
//	&TextContent{Text string} implements Content
//	Role is a string; MCP roles are "user" / "assistant".
//
// Client side: ClientSession.GetPrompt(ctx, *GetPromptParams) (*GetPromptResult, error)

// roleUser is the MCP message role for instructional prompt bodies.
const roleUser = mcp.Role("user")

// arg returns the named argument or fallback when absent/empty.
func arg(req *mcp.GetPromptRequest, name, fallback string) string {
	if req != nil && req.Params != nil {
		if v, ok := req.Params.Arguments[name]; ok && v != "" {
			return v
		}
	}
	return fallback
}

// textPrompt builds a single-message GetPromptResult with a user-role body.
func textPrompt(desc, body string) *mcp.GetPromptResult {
	return &mcp.GetPromptResult{
		Description: desc,
		Messages: []*mcp.PromptMessage{
			{Role: roleUser, Content: &mcp.TextContent{Text: body}},
		},
	}
}

// stabilizeFootagePromptText returns the full operating recipe for the
// stabilize-footage prompt. Extracted so tests can assert on the exact text
// without duplicating the literal string.
func stabilizeFootagePromptText(path string) string {
	return fmt.Sprintf(`You are stabilizing exactly one file: %s

Use ONLY the gyroflow-mcp MCP tools below, in this exact order, and do not
skip or reorder steps.
Do NOT shell out to ffprobe/jq/grep or open raw_path.
probe_metadata's summary already contains the camera, resolution, fps and
has_gyro you need; raw_path is only for rare manual deep inspection and is
NOT needed for this flow.

1. gyroflow_doctor - verify the Gyroflow CLI. Result fields: found, usable,
   sandboxed, app_store, path, version, advice.
   - If found=false OR usable=false: relay the doctor's advice verbatim and
     STOP. Do NOT attempt a render - it will fail.
   - Only continue when usable=true. sandboxed=true with usable=true is fine:
     the App Store build renders through a temporary work folder.

2. probe_metadata - call probe_metadata with input=%q. It returns
   {summary:{...}, raw_path}. Read ONLY summary:
     summary.camera.make, summary.camera.model, summary.camera.identifier
     summary.detected_source, summary.lens, summary.has_gyro
     summary.width, summary.height, summary.fps, summary.duration_s,
     summary.frame_count
   - If summary.has_gyro is false: STOP. Explain honestly that there is no
     embedded gyro/IMU motion data, so Gyroflow has nothing to stabilize.
     Point the user to the diagnose prompt (supply an external gyro log via
     render_start gyro_file / -g, or confirm the camera recorded motion). Do
     NOT start a render - it would only produce an unstabilized copy.
   - Do NOT open raw_path or run ffprobe/jq/grep - everything required is in
     summary.

3. find_lens_profile - call find_lens_profile with:
     query  = summary.camera.make + " " + summary.camera.model
              (use summary.detected_source if make/model are empty)
     width  = summary.width
     height = summary.height
     fps    = summary.fps
   It returns {best, alternatives, hits, gyroflow_auto_loads, advice}.

4. Decide preset_path from step 3 - DECISION ONLY: do NOT call render_start
   yet, and do NOT guess a profile. Choose exactly one:
   a. gyroflow_auto_loads = true → preset_path = none (omit it). Gyroflow
      auto-loads this camera's lens profile from the video's embedded
      metadata; relay find_lens_profile.advice so the user knows an explicit
      preset is only needed to override it.
   b. else best is not null → preset_path = best.path.
   c. else (best is null AND gyroflow_auto_loads = false) → surface
      find_lens_profile.advice verbatim and STOP / ask the user how to
      proceed. Do NOT invent or guess a lens profile.

5. Translate the user's natural-language intent into a typed config object
   for render_start. Before choosing non-obvious values, read the
   gyroflow://capabilities resource (it documents every field's meaning,
   unit, range and enum values). Examples of the mapping you must do
   yourself (never ask the user to write JSON):
     - "lock the horizon"        → config.horizon_lock.amount = 100
     - "smoother / less shaky"   → config.smoothing.method "Default" with a
                                    higher smoothness (e.g. 0.7); "very
                                    smooth" ~0.9
     - "crop as little as possible" / "minimal crop" → config.zoom.adaptive_zoom_window = -1
                                    (static crop - least cropping with a stable frame). Use
                                    0 only if the user explicitly accepts visible black borders
                                    on shake (no zoom at all). Do NOT use a positive value for
                                    minimal crop - positive values (e.g. the 4.0 default) zoom
                                    in dynamically and crop MORE, not less.
     - "fix the jello / rolling shutter" → set
                                    config.rolling_shutter.frame_readout_time (ms)
     - "trim the first N seconds" → config.trim.ranges_ms = [[N*1000, <end>]]
                                    where <end> = summary.duration_s*1000 (ms)
     - "make the FOV zoom in at 0:12" → a config.keyframes entry
                                    {param:"Fov", timestamp_ms:12000,
                                    value:<v>, easing:"EaseIn"}
   Set ONLY what the user asked for; leave everything else unset (Gyroflow
   defaults apply). If the user gave no settings at all (a .gyroflow preset
   counts as settings), omit config: the server asks the user in a form (if your client cannot show MCP forms, ask
   the user for smoothing, aspect ratio, crop, horizon lock and codec first). Make
   exactly ONE render_start call with this config.

6. render_start - call render_start ONCE with inputs=[%q], the preset_path
   from step 4 (omit entirely for case 4a), and the config from step 5 (omit
   if no config fields were needed). This is the ONLY render_start call. It
   returns {job_id} IMMEDIATELY; the render is NOT finished. Capture job_id.
   Do NOT block or assume completion from this call.

7. If render_start returns an error mentioning a field/range, correct that
   single field from the gyroflow://capabilities ranges and retry once -
   do NOT shell out to ffprobe/jq/grep or hand-edit a preset file.

8. render_wait - call render_wait with the job_id, and again while state is
   "running", until state is terminal: "completed", "failed", or "cancelled".
   Use the default timeout_s. Every time it returns "running", write the user
   one short line with the percent of the current file and of the total.
   - completed: report the output path(s) from the status "outputs". If
     "outputs" is absent/empty, the stabilized file is the input path's
     directory + the input file name with the configured suffix (default
     "_stabilized") and the same extension.
   - failed: report the actionable "error"; if it mentions "Unable to read
     the video file", re-run gyroflow_doctor and relay its sandbox/DMG advice.
   - cancelled: report that the render was cancelled; there is no output.

Finish with a concise summary: doctor status, has_gyro, the lens decision
(auto-load / preset path / stopped), job_id, final state, and output path.`, path, path, path)
}

// RegisterPrompts adds the stabilize-footage, batch-stabilize, and diagnose
// prompts to s. Each prompt body is the full, literal operating recipe - no
// placeholders - so an LLM client can follow it directly.
func RegisterPrompts(s *mcp.Server) {
	// ---- stabilize-footage ----
	s.AddPrompt(&mcp.Prompt{
		Name:        "stabilize-footage",
		Description: "Step-by-step recipe to stabilize a single video with Gyroflow.",
		Arguments: []*mcp.PromptArgument{
			{Name: "path", Description: "Path to the video (or .gyroflow project) to stabilize.", Required: true},
		},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		path := arg(req, "path", "<the input video path>")
		return textPrompt("Stabilize a single video, end to end.", stabilizeFootagePromptText(path)), nil
	})

	// ---- batch-stabilize ----
	s.AddPrompt(&mcp.Prompt{
		Name:        "batch-stabilize",
		Description: "Recipe to stabilize many files (a folder or list) and track all jobs.",
		Arguments: []*mcp.PromptArgument{
			{Name: "inputs", Description: "A folder path or comma/space-separated list of video paths.", Required: true},
		},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		inputs := arg(req, "inputs", "<folder or list of video paths>")
		body := fmt.Sprintf(`You are batch-stabilizing: %s

Follow this flow:

1. gyroflow_doctor - run ONCE up front. If found=false or usable=false, surface the advice and STOP for the whole batch (every file would fail the same way).

2. Enumerate the inputs:
   - If %q is a folder, list the video files in it (skip non-video files and existing *_stabilized outputs).
   - If it is a list, split on commas/whitespace into individual paths.
   - Report the resolved file list and count before starting.

3. Per file, in order (use ONLY MCP tools - no ffprobe/jq/grep, do not open raw_path):
   a. probe_metadata - call with the file path; read its summary. If summary.has_gyro is false and the user has no external -g for it, record it as SKIPPED with the reason (do not silently render it) and continue with the rest.
   b. find_lens_profile - query = summary.camera.make+" "+summary.camera.model (or summary.detected_source), width=summary.width, height=summary.height, fps=summary.fps. If gyroflow_auto_loads=true, render without preset_path (relay advice); else if best!=null use preset_path=best.path; else surface advice and skip/ask. Reuse a cached decision for identical camera+resolution+fps to avoid redundant lookups.
   c. render_start - start the render with the chosen preset (or none per 3b); collect the returned job_id paired with the file. If the user gave no settings, omit config on the FIRST file so the server asks the user, then pass the config it returns to every other file.

4. Track progress without blocking:
   - Use render_list to see all jobs, and render_status per job_id for detail.
   - Poll until every job is "completed", "failed", or "cancelled".

5. Summarize a table: file, job_id, final state, output path (or failure reason / SKIPPED reason). Call out any files that failed with "Unable to read the video file" - that points back to gyroflow_doctor (sandbox/DMG advice).`, inputs, inputs)
		return textPrompt("Stabilize many files and track all jobs.", body), nil
	})

	// ---- diagnose ----
	s.AddPrompt(&mcp.Prompt{
		Name:        "diagnose",
		Description: "Triage a bad or failed stabilization result.",
		Arguments: []*mcp.PromptArgument{
			{Name: "path", Description: "The input file involved (optional).", Required: false},
			{Name: "symptom", Description: "What went wrong (optional free text).", Required: false},
		},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		path := arg(req, "path", "<the input file>")
		symptom := arg(req, "symptom", "(no symptom given - ask the user what is wrong)")
		body := fmt.Sprintf(`Diagnose a stabilization problem.
Input: %s
Reported symptom: %s

First, run gyroflow_doctor. If usable=false, that is almost always the root cause. Relay the doctor advice and stop here - nothing else will work until that is fixed. The error signature "Unable to read the video file" maps to this same cause (also confirm the path is absolute and the file exists).

Otherwise triage by symptom:

A. NO GYRO DATA / "nothing was stabilized":
   - Run probe_metadata on %q and read summary. If summary.has_gyro is false, Gyroflow has no motion to correct. (Use the summary; do not shell out to ffprobe/jq or open raw_path.)
   - Guidance: supply an external gyro/IMU log via the gyro_file (-g) argument to render_start (e.g. .bbl/Betaflight blackbox, GoPro .mp4 GPMF, Insta360, .gcsv). Confirm the camera actually recorded motion data and that the file format is one Gyroflow supports. Without motion data, stabilization is impossible - say so plainly.

B. WRONG / DISTORTED LENS (warping, over/under-correction):
   - The lens profile likely does not match. Run find_lens_profile with query from summary.camera (make+model) plus width=summary.width, height=summary.height, fps=summary.fps so the ranked best matches the real shooting mode.
   - Present best and the top alternatives (display + path) and confirm the exact shooting mode (e.g. Linear vs Wide vs SuperView, the resolution/FPS). Re-render with the corrected preset_path. If gyroflow_auto_loads=true, the embedded profile is used automatically - only override it deliberately.
   - If no calibrated profile exists for that mode, say so; accuracy will be limited until one is calibrated.

C. SYNC FAILURE (footage drifts/swims, gyro present but mistimed):
   - This is a gyro↔frame synchronization problem, not a lens problem. Re-run render_start passing sync_params (-s) - adjust the sync search timestamps/range and the rough frame offset so Gyroflow can lock the autosync, or set a manual offset. Iterate: change sync params, render a short range, inspect, repeat.

Always finish with a concrete next action (which tool to call with which arguments), not just a description of the problem.`, path, symptom, path)
		return textPrompt("Triage a bad or failed stabilization.", body), nil
	})
}
