---
name: gyroflow-mcp
description: Use when the user wants to stabilize shaky/gyro-equipped video, inspect video metadata, or find a Gyroflow lens profile via the gyroflow-mcp server. Guides the doctor-first → probe → lens → async render → poll workflow and handles the macOS sandbox pitfall.
---

# gyroflow-mcp workflow

`gyroflow-mcp` exposes the Gyroflow CLI as MCP tools. This skill is the
recommended operating procedure. It is **complementary to** the server's
built-in MCP prompts (`stabilize-footage`, `batch-stabilize`, `diagnose`) - use
those for ready-made recipes; use this skill to drive the tools directly.

## Hard boundary

This server only **detects** an installed Gyroflow binary; it never installs
one. If Gyroflow is not present or the doctor reports it is not usable,
renders cannot work - surface the doctor's advice to the user instead of
retrying. Do not fabricate stabilized output.

## Workflow

### 1. Always run `gyroflow_doctor` first

Call `gyroflow_doctor` before anything else. Inspect the result:

- **Not found** → tell the user to install Gyroflow from
  <https://gyroflow.xyz/download> and stop.
- **Not usable** → relay the doctor's advice verbatim and do not attempt a
  render until it reports usable.
- **Sandboxed and usable (macOS App Store build)** → renders work through a
  temporary work folder. `export_project` and `export_stmap` need the DMG build.
- **Usable** → proceed.

> **Use only the MCP tools.** Do NOT shell out to `ffprobe`, `jq`, `grep`, or
> any external command, and do NOT open `raw_path`. `probe_metadata`'s
> `summary` already contains the camera, resolution, fps and `has_gyro` you
> need; `raw_path` is only for rare manual deep inspection.

### 2. Inspect the footage - `probe_metadata`

Call `probe_metadata` with `input` = the video (or `.gyroflow` project) path.
It returns `{summary:{…}, raw_path}` - the **same shape** as the
`project://{path}/metadata` resource. Read **only** `summary`:

- `summary.camera.{make,model,identifier}`, `summary.detected_source`
- `summary.lens`, `summary.has_gyro`
- `summary.width`, `summary.height`, `summary.fps`, `summary.duration_s`,
  `summary.frame_count`

**Honest boundary:** if `summary.has_gyro` is `false`, STOP - there is no
embedded motion data, so a render would only produce an unstabilized copy.
Explain this and point the user to the `diagnose` prompt (external gyro log
via `render_start` `gyro_file`/`-g`, or confirm the camera recorded motion).

### 3. Find a lens profile - `find_lens_profile`

Call `find_lens_profile` with `query` = `summary.camera.make + " " +
summary.camera.model` (or `summary.detected_source` if those are empty), and
`width`/`height`/`fps` from the probe `summary`. It returns
`{best, alternatives, hits, gyroflow_auto_loads, advice}`. The full profile
JSON is also available via `lens://profile/{id}` (index via `lens://profiles`).

### 4. Decide the preset (deterministic - never guess a profile)

- `gyroflow_auto_loads == true` → call `render_start` **without**
  `preset_path`; Gyroflow auto-loads the camera's profile from embedded
  metadata. Relay `advice` (a preset is only needed to override it).
- else `best != null` → `render_start` with `preset_path = best.path`.
- else (`best == null` and not auto-load) → surface `advice` verbatim and
  STOP / ask the user. Do not invent a profile.

### 5. Build the typed config - consult `gyroflow://capabilities`

Translate the user's natural-language request into a typed `config` object for
`render_start`. Before choosing non-obvious values, read the
`gyroflow://capabilities` resource - it documents every field's meaning, unit,
range, and enum values. Set ONLY what the user asked for; leave everything else
unset (Gyroflow defaults apply).

Examples of the intent→field mapping you must perform yourself (never ask the
user to write JSON):

| User says | Config field(s) |
|-----------|----------------|
| "lock the horizon" | `config.horizon_lock.amount = 100` |
| "smoother / less shaky" | `config.smoothing.method = "Default"`, `smoothness ≈ 0.7`; "very smooth" ≈ 0.9 |
| "crop as little as possible" | `config.zoom.adaptive_zoom_window = 0` (no zoom) or `-1` (static crop); explain border trade-off |
| "fix jello / rolling shutter" | `config.rolling_shutter.frame_readout_time` (ms from camera spec or probe output) |
| "trim the first N seconds" | `config.trim.ranges_ms = [[N×1000, duration_ms]]` where `duration_ms = summary.duration_s × 1000` |
| "ramp/animate FOV at 0:12" | `config.keyframes` entry: `{param:"Fov", timestamp_ms:12000, value:<v>, easing:"EaseIn"}` |

If `render_start` returns a range/field error, correct that one field from
`gyroflow://capabilities` and retry once - do NOT shell out to ffprobe/jq/grep.

### 6. Start the render - `render_start` (asynchronous)

Call `render_start` with `inputs=[path]`, `preset_path` per step 4 (omit for
case 4a), and `config` from step 5 (omit entirely if no fields were needed).
It returns a **`job_id` immediately** - the render is NOT done yet. This is the
ONLY `render_start` call. Never assume completion from this return.

### 7. Wait - `render_wait`

Call `render_wait` with the `job_id` until a terminal state
(`completed`/`failed`/`cancelled`); call it again if it returns `running`.
Use the default `timeout_s`, and every time it returns `running`, write the
user one short line with the percent of the current file and of the total, so
clients that do not draw the live bar still show progress. `render_status` gives
the same fields without waiting. Use `render_list` for all jobs, `render_cancel`
to stop one. `frame` and `total` are gyroflow's packet counts: on clips with
pre-roll they exceed the frames in the output, and no video is missing.

On `completed`, read the output path from the status `outputs` and give it to
the user. If `outputs` is absent/empty, the stabilized file is the input
path's directory + the input file name with the configured suffix (default
`_stabilized`) and the same extension. On `failed`, relay the `error`; if it
mentions "Unable to read the video file", re-run `gyroflow_doctor` and relay
its sandbox/DMG advice. On `cancelled`, report it was cancelled (no output).

## Other tools

- `export_project` - export a `.gyroflow` project to a file (kinds:
  1=default, 2=gyro, 3=processed, 4=video). Destructive (writes a file).
- `export_stmap` - export a per-pixel distortion/warp map (STMap) for
  compositing pipelines (Nuke/Fusion/Resolve). `type`: 1=single frame,
  2=all frames; `out_folder`: destination folder. Destructive (writes files).

## Tips

- All paths should be ones the user gave or that exist on disk; the server
  resolves them to absolute paths internally.
- Tools are annotated read-only vs. destructive - destructive ones
  (`render_start`, `render_cancel`, `export_project`, `export_stmap`) write/alter state.
- Batch work: start each render with `render_start`, collect the `job_id`s,
  then poll each via `render_status` (see the `batch-stabilize` prompt).
- `probe_metadata` accepts a `kind` argument (2=parsed summary [default],
  1=full, 3=camera data) and an optional `fields` JSON string to limit
  exported fields. The default kind=2 summary is all that the standard flow
  needs; read `raw_path` only for rare manual deep inspection.
- The `gyroflow://capabilities` resource is the authoritative reference for
  every `config` field - read it before setting non-obvious parameters.
