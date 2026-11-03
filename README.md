# gyroflow-mcp

Stabilize drone and action-camera footage by asking your AI agent, using your
own [Gyroflow](https://gyroflow.xyz) settings. This [MCP](https://modelcontextprotocol.io)
server wraps the Gyroflow CLI so you don't have to open and export clips one by one.

> Stabilize the clips in ~/Desktop/DRON/Pavo20Pro with my 16-9.gyroflow preset

Everything runs locally: your footage is never uploaded by this server. The only
network request it makes is downloading Gyroflow's public lens-profile database
at startup.

Fork of [Errorist79/gyroflow-mcp](https://github.com/Errorist79/gyroflow-mcp),
adding support for the free Mac App Store build of Gyroflow.

## Project status

Experimental. Real-footage testing so far covers one setup: Gyroflow 1.6.3 from
the Mac App Store on Apple Silicon, with DJI O4 Pro clips on the internal disk.

Not tested yet: the DMG/Homebrew build with these changes, clips on an external
disk or SD card, several clips in one job, other cameras, and a Gyroflow install
that has never been opened.

## Quick start

You need Gyroflow from the
[Mac App Store](https://apps.apple.com/app/gyroflow/id6447994244) or
[its download page](https://gyroflow.xyz/download). This server detects Gyroflow;
it does not install it. On macOS:

```sh
brew install carlosFreak/tap/gyroflow-mcp
claude mcp add --scope user gyroflow-mcp -- "$(brew --prefix)/bin/gyroflow-mcp"
```

Restart Claude Code, then ask it to stabilize a clip.

To build from source instead, with [Go 1.25+](https://go.dev/dl/):

```sh
git clone https://github.com/carlosFreak/gyroflow-mcp
cd gyroflow-mcp
go build -o bin/gyroflow-mcp ./cmd/gyroflow-mcp
claude mcp add --scope user gyroflow-mcp -- "$PWD/bin/gyroflow-mcp"
```

The registration points at the built binary, so keep that folder where it is.

For Claude Desktop, Cursor and other stdio MCP clients, add this to the client's
`mcpServers` config (Claude Desktop:
`~/Library/Application Support/Claude/claude_desktop_config.json`; Cursor:
`~/.cursor/mcp.json`):

```json
{
  "mcpServers": {
    "gyroflow-mcp": {
      "command": "/ABSOLUTE/PATH/TO/gyroflow-mcp",
      "args": []
    }
  }
}
```

## Before you start: does your camera record gyro data?

Gyroflow stabilizes using motion data recorded in the clip. FPV cameras such as
the DJI O3/O4 and most action cameras record it; gimbal drones such as the DJI
Mini, Air and Mavic usually don't. Ask your agent to check a clip, which uses
`probe_metadata` and reports `has_gyro`.

For best results, record with in-camera stabilization (RockSteady, EIS) turned off.

## Settings questions

If you ask without any settings (*"stabilize this clip"*), you are asked five
questions before anything renders:

| Question | Options (first is the default) |
|----------|--------------------------------|
| Smoothing | Medium, Light, Strong (gimbal-like), Locked camera |
| Aspect ratio | Same as source, 16:9, 9:16, 1:1, 4:5 |
| Crop | Dynamic zoom, Static zoom, No zoom (visible borders) |
| Horizon lock | Off, Partial, Full |
| Codec | Same as source, H.264, H.265/HEVC, ProRes |

Clients that support MCP forms (elicitation) show them as a form. Others, such
as the Claude desktop app, have the agent ask them in chat.
When stabilizing several clips you answer once for all of them.

Nothing is asked when you give a [`.gyroflow` preset](#use-your-own-settings),
since it already holds your settings. You can also put the settings in your
request (*"stabilize this clip in 9:16 with the horizon locked"*); anything you
leave out keeps Gyroflow's default. Say *"use the defaults"* to skip the
questions entirely.

## Color LUTs

After stabilizing, the result can get a color LUT, such as DJI's free
*D-Log M to Rec.709*. Before each render you are asked whether to apply one,
even when you give a preset:

- **The LUT for this clip's camera**, when your LUT folder has one.
- **Another LUT** from your folder.
- **Your own `.cube`**: give its path. It is checked, then saved to the folder
  so it is offered next time.
- **No LUT.**

With no LUT saved yet for a DJI camera, the question links to its official
download. Other brands get no link. A camera LUT is only for clips recorded in
its log profile (D-Log M for DJI): on normal footage it oversaturates.

LUTs live in `~/Movies/LUTs`, or the folder in `GYROFLOW_MCP_LUT_DIR`. They
need ffmpeg (`brew install ffmpeg`); without it nothing is asked. Gyroflow first
writes a near-lossless intermediate next to the result (about 3 GB a minute in
4K60), then ffmpeg applies the LUT and encodes the final file once, in the
codec and bitrate you chose. LUTs work with H.265/HEVC, H.264 and ProRes.

## Use your own settings

Export your Gyroflow settings once and reuse them on every clip:

1. Open a clip in Gyroflow and adjust it until you like the result.
2. Click the arrow next to **Export** and choose **Create settings preset**.
3. Save the `.gyroflow` file and give your agent its path.

When choosing what to include:

- **Include** stabilization settings, codec, bitrate and output size.
- **Leave out** lens profile (usually detected from the clip), keyframes, and the
  approximate gyro offset, which belong to one clip.

Output size is what sets the aspect ratio, not the preset's name, and it applies
to every clip. Make one preset per format. For a 3840x2880 source, these sizes
avoid upscaling:

| Format | Output size |
|--------|-------------|
| 4:3 | 3840x2880 |
| 16:9 | 3840x2160 |
| 1:1 | 2880x2880 |
| 9:16 | 1620x2880 |

The crop is centered, so 9:16 loses the most from the sides.

Without a preset, asking for a ratio (*"in 1:1"*) works out the same sizes from
each clip's resolution.

Any output folder or filename stored in a preset is ignored. A preset can also
contain `frame_readout_time`, which suits the recording mode it was exported
from; export a separate preset for other modes.

## Where the results go

Each result is saved next to the original as `<name>_stabilized_<ratio>.<ext>`,
e.g. `DJI_0005_D_stabilized_1x1.mp4` or `_16x9`, `_9x16`, `_4x3` (the source's
own ratio when the size is unchanged). A suffix you ask for gets the ratio too
(`_test` → `_test_1x1`), unless it already ends in it. Clips of different ratios
rendered together keep the plain suffix. You can also choose the output folder. Originals are not modified, and an
existing output is only replaced if you ask to overwrite it.

## App Store or DMG / Homebrew

`gyroflow_doctor` tells you which build was detected. Both are free.

| | Mac App Store | DMG / Homebrew |
|---|---|---|
| How it runs | Through a temporary work folder | CLI driven directly |
| Clips on another disk | Need internal-disk space for the copy and the render | No extra copy |
| Clips on the internal APFS disk | The copy is a clone; only the render takes space | No extra copy |
| `export_project`, `export_stmap`, `.gyroflow` project inputs | Not available | Available |

The App Store sandbox only lets the CLI access
`~/Library/Application Support/Gyroflow/`. Each job clones its clips into
`gyroflow-mcp/job-*` there, renders, moves the result out and removes the job
folder. Metadata exports stay in `gyroflow-mcp/metadata/` (about 22 MB for a
90-second O4 Pro clip) and are not removed automatically.

To use the DMG build: `brew install --cask gyroflow`. Its app bundle uses the same
location as the App Store one, so remove the App Store app first.

## Usage for agents

Renders are **asynchronous**: `render_start` returns a `job_id` immediately;
call `render_wait` (or poll `render_status`) until the job reaches a terminal state,
then read the output path from the result. Run `gyroflow_doctor` first - if
Gyroflow is missing or unusable it returns actionable advice instead of
failing mid-render.

A typical agent flow: `gyroflow_doctor` → `probe_metadata` →
`find_lens_profile` → `render_start` → poll `render_status`. The
`stabilize-footage` prompt encodes this end to end, so a natural-language
request like *"stabilize this video, lock the horizon, trim the first 3
seconds"* maps to a single `render_start` call with a typed config.

If the request gives no settings, the agent calls `render_start` without
`config` and, unless `preset_path` or an input is a `.gyroflow` file, the server asks the [settings questions](#settings-questions) in
an MCP form (elicitation). `render_start` returns the chosen `config` so a
batch asks only once. If the form is not answered - declined, cancelled or
empty (the Claude desktop app declines or cancels forms without showing them) -
nothing renders: the error lists every option with its `config` so the agent
asks the user in chat and calls again. Clients without form support are told
to ask those five settings themselves; pass `config: {}` to skip the questions
and use the defaults.

## Tools

All tools carry MCP annotations (read-only vs. destructive) so clients can gate
them appropriately.

| Tool | Hint | Description |
|------|------|-------------|
| `render_start` | destructive | Start a stabilization render. Returns a `job_id`; follow it with `render_wait` or `render_status`. Accepts a typed `config` (see below); without one, asks the user for the main settings in a form. `lut` takes a `list_luts` name, a `.cube` path or `none`; omitted, the user is asked. |
| `list_luts` | read-only | List the `.cube` LUTs in the LUT folder; with `input`, also the one suggested for that clip's camera and, for DJI, its download link. |
| `render_status` | read-only | Progress and state for a render job, including a ready-to-show `progress_bar` (with several inputs, one bar for the current file and one for the total) and per-file `files`. |
| `render_wait` | read-only | Wait for a job, streaming a live progress bar as MCP progress notifications when the client sends a `progressToken`. Returns on completion or after `timeout_s` (default 55, max 600). Agents are told to report the percent to the user after each call. |
| `render_cancel` | destructive | Cancel a running render job. |
| `render_list` | read-only | List all render jobs and their states. |
| `gyroflow_doctor` | read-only | Inspect the detected gyroflow binary: path, version, sandbox status, advice. |
| `probe_metadata` | read-only | Inspect a video/`.gyroflow` file: `{summary:{camera, lens, has_gyro, resolution, fps, duration}, raw_path}`. Optional `kind` (2=parsed default, 1=full, 3=camera) and `fields`. |
| `export_project` | destructive | Export a gyroflow project file (kinds: 1=default, 2=gyro, 3=processed, 4=video). |
| `find_lens_profile` | read-only | Search the Gyroflow lens-profile database by camera/lens. |
| `export_stmap` | destructive | Export a per-pixel distortion map (STMap) for compositing (Nuke/Fusion/Resolve). `type`: 1=single frame, 2=all frames. |

## Resources

| URI | Description |
|-----|-------------|
| `lens://profiles` | Index of all known Gyroflow lens profiles. |
| `lens://profile/{id}` | Raw JSON of a single lens profile by identifier. |
| `project://{path}/metadata` | Gyroflow-extracted metadata for a video/project file. |
| `gyroflow://capabilities` | Authoritative reference for every `render_start` `config` field - meaning, unit, range, enum values. |

## Prompts

| Prompt | Description |
|--------|-------------|
| `stabilize-footage` | Step-by-step recipe to stabilize a single video. |
| `batch-stabilize` | Stabilize many files (folder or list) and track all jobs. |
| `diagnose` | Triage a bad or failed stabilization result. |

## Configuration surface

`render_start` accepts an optional typed `config` object covering Gyroflow's
full parameter surface. Every field is optional; unset fields fall back to
Gyroflow's defaults. Values are verified against **Gyroflow 1.6.3**;
out-of-range or invalid input is rejected with an actionable error the agent
can correct in one turn.

| `config` group | Controls |
|----------------|----------|
| `stabilization` | FOV multiplier, lens-correction amount, additional rotation/translation, frame offset |
| `smoothing` | Algorithm (`Default` / `Plain 3D` / `Fixed camera` / `No smoothing`) + its strength / time-constant / per-axis params |
| `horizon_lock` | Horizon-levelling strength (0-100), roll offset, gravity-vector integration |
| `zoom` | Adaptive zoom window (`-1` static crop, `0` no zoom, `>0` seconds), max zoom, center offset |
| `rolling_shutter` | Frame readout time (ms) and direction (0-3) |
| `trim` | `ranges_ms`: `[start_ms, end_ms]` pairs restricting which frames render |
| `speed` | Playback-speed multiplier and whether smoothing/zoom adapt to it |
| `lens` | Digital lens correction (`""` / `gopro_superview` / `gopro_hyperview`) |
| `rotation` | Input video rotation offset (degrees) |
| `background` | Border fill mode (solid/repeat/mirror/feather) and RGBA color |
| `keyframes` | Per-keyframe animation: `param`, `timestamp_ms`, `value`, `easing` |
| `output` | Codec, bitrate, resolution, pixel format, audio, GPU encode (typed `-p`) |
| `sync` | Gyro-video synchronisation parameters (typed `-s`) |
| `raw_overrides` | JSON deep-merged last over the generated preset - escape hatch for un-typed fields |

Plus standalone `render_start` arguments: `suffix` (output name suffix, `-t`),
`processing_device` (GPU index, `-b`), `rendering_device`
(`nvidia`/`intel`/`amd`/`apple m`, `-r`), `no_gpu_decoding`
(`--no-gpu-decoding`).

For exact units, ranges, and enum values per field, read the
**`gyroflow://capabilities`** resource - it is the authoritative, always-in-sync
reference (kept here as a pointer to avoid schema drift).

## Development

```sh
go build ./...
go vet ./...
go test ./...                                          # hermetic unit suite, no external binary
go test -tags integration ./internal/backend/cli/ -v   # real-binary, requires Gyroflow installed
```

CI runs `go build`, `go vet`, and the hermetic `go test ./...` unit suite. The
`integration` tag requires a non-sandboxed Gyroflow install and is run locally.

## Roadmap

v1 drives Gyroflow via its CLI behind a `Backend` interface. A later phase can
swap in a native core backend behind that same interface - no external process
per render - without changing the MCP surface clients depend on.

## License

[MIT](LICENSE) © Errorist79
