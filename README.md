# gyroflow-mcp

An [MCP](https://modelcontextprotocol.io) server that wraps the
[Gyroflow](https://gyroflow.xyz) CLI so an LLM/agent can stabilize video,
inspect footage metadata, and look up lens profiles - through MCP tools,
resources, and prompts.

## Project status

This is an experimental hobby project. I built it while learning Gyroflow for my own drone footage, so some options or assumptions may be incomplete or wrong.

It ships as a **single self-contained Go binary**: no virtualenv, no Node, no
runtime dependencies, and no flags or environment variables. The server speaks
MCP over stdio and is configured purely by your MCP client's config file (a
path to the binary).

## Requirements

- **Go 1.25+** (to build).
- **Gyroflow installed yourself.** gyroflow-mcp does not bundle or download
  Gyroflow - it *detects* an already-installed binary at startup. Get it from
  <https://gyroflow.xyz/download>.

> ### macOS: App Store or DMG / Homebrew
>
> Both builds work, and `gyroflow_doctor` tells you which one was detected.
>
> - **DMG or Homebrew** (`brew install --cask gyroflow`): the CLI is driven directly.
> - **Mac App Store**: the sandbox only lets its CLI access
>   `~/Library/Application Support/Gyroflow/`, so each job clones the clips into
>   a work folder there, renders, moves the result next to the original (or to
>   `out_params.output_folder`) and removes the work folder.
>
> With the App Store build, clips on another disk need free space on the internal
> disk for the copy and the render, and `export_project` and `export_stmap` are
> not available because their output would reference a temporary copy.

## Installation

From source:

```sh
git clone https://github.com/Errorist79/gyroflow-mcp
cd gyroflow-mcp
go build -o ./bin/gyroflow-mcp ./cmd/gyroflow-mcp
```

Or, once published, via the Go toolchain:

```sh
go install github.com/Errorist79/gyroflow-mcp/cmd/gyroflow-mcp@latest
```

Note the resulting binary's absolute path - you reference it in your MCP client
config below.

## Client configuration

The binary takes no arguments and reads no environment variables, so every
client config is just a `command` pointing at it. Replace
`/ABSOLUTE/PATH/TO/gyroflow-mcp` accordingly.

**Claude Code:**

```sh
claude mcp add gyroflow-mcp -- /ABSOLUTE/PATH/TO/gyroflow-mcp
```

**Claude Desktop / Cursor / other stdio MCP clients** - add to the client's
`mcpServers` config:

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

(Claude Desktop: `~/Library/Application Support/Claude/claude_desktop_config.json`;
Cursor: `~/.cursor/mcp.json` or `.cursor/mcp.json`.)

## Usage

Renders are **asynchronous**: `render_start` returns a `job_id` immediately;
poll `render_status` (or `render_list`) until the job reaches a terminal state,
then read the output path from the result. Run `gyroflow_doctor` first - if
Gyroflow is missing or sandboxed it returns actionable advice instead of
failing mid-render.

A typical agent flow: `gyroflow_doctor` → `probe_metadata` →
`find_lens_profile` → `render_start` → poll `render_status`. The
`stabilize-footage` prompt encodes this end to end, so a natural-language
request like *"stabilize this video, lock the horizon, trim the first 3
seconds"* maps to a single `render_start` call with a typed config.

## Tools

All tools carry MCP annotations (read-only vs. destructive) so clients can gate
them appropriately.

| Tool | Hint | Description |
|------|------|-------------|
| `render_start` | destructive | Start a stabilization render. Returns a `job_id`; poll `render_status`. Accepts a typed `config` (see below). |
| `render_status` | read-only | Progress and state for a render job. |
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
