package cli

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
)

// maxTailBytes bounds how much gyroflow output we retain. A long render can
// spam non-progress stdout/stderr without limit; only the trailing portion is
// ever used (StderrTail tails ≤2000 bytes and classifyRunFailure matches the
// error/advice lines which appear at/near the end). 64 KiB is comfortably
// above those needs while keeping memory bounded.
const maxTailBytes = 64 << 10

// tailBuffer is an io.Writer that retains only the last maxTailBytes bytes
// written to it. The backing array is bounded (~maxTailBytes) after the first
// trim, so capturing huge output cannot grow memory without limit.
type tailBuffer struct{ buf []byte }

func (t *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if n >= maxTailBytes {
		// p alone overflows: keep only its last maxTailBytes.
		t.buf = append(t.buf[:0], p[n-maxTailBytes:]...)
		return n, nil
	}
	t.buf = append(t.buf, p...)
	if len(t.buf) > maxTailBytes {
		overflow := len(t.buf) - maxTailBytes
		copy(t.buf, t.buf[overflow:]) // shift retained suffix to the front
		t.buf = t.buf[:maxTailBytes]
	}
	return n, nil
}

func (t *tailBuffer) WriteString(s string) (int, error) { return t.Write([]byte(s)) }
func (t *tailBuffer) String() string                    { return string(t.buf) }
func (t *tailBuffer) Len() int                          { return len(t.buf) }

// CLIBackend implements backend.Backend by shelling out to the gyroflow binary.
type CLIBackend struct{ bin string }

// New creates a CLIBackend that uses the binary at the given path.
func New(bin string) *CLIBackend { return &CLIBackend{bin: bin} }

// adviseErr classifies the combined gyroflow output for known failure patterns.
// If matched, it returns the actionable advice wrapped around base; if not
// matched (or base is nil) it returns base unchanged. This deduplicates the
// classify+wrap sequence across Stabilize, ExportProject, and ProbeMetadata.
func adviseErr(combinedOutput string, base error) error {
	if base == nil {
		return nil
	}
	if advice, matched := classifyRunFailure(combinedOutput, runtime.GOOS); matched {
		return fmt.Errorf("%s: %w", advice, base)
	}
	return base
}

func (b *CLIBackend) Stabilize(ctx context.Context, req backend.StabilizeRequest, onProgress backend.ProgressFunc) (*backend.Result, error) {
	var err error
	req, err = prepareStabilize(req)
	if err != nil {
		return nil, err
	}
	req, err = resolveStabilizeReq(req)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, b.bin, stabilizeArgv(req)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr tailBuffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// Capture non-progress stdout lines - gyroflow emits WARN/ERROR messages
	// to stdout alongside --stdout-progress lines. These carry failure details
	// (e.g. "Unable to read the video file") and are needed for adviseErr.
	// Bounded so a long render spamming stdout cannot grow memory unbounded.
	var nonProgressOut tailBuffer
	var outputPaths []string
	seenOut := map[string]bool{}
	tracker := newProgressTracker()
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		line := sc.Text()
		if p, ok := parseProgressLine(line); ok {
			if onProgress != nil {
				onProgress(tracker.sample(p))
			}
			continue
		}
		tracker.queueLine(line)
		// gyroflow prints the RESOLVED output path on its Queue: entry line
		// (one per input). This is authoritative - it already reflects the
		// -t suffix / out_params / container, so no naming guess is needed.
		if op, ok := parseQueueOutputPath(line); ok && !seenOut[op] {
			seenOut[op] = true
			outputPaths = append(outputPaths, op)
		}
		nonProgressOut.WriteString(line + "\n")
	}
	if err := sc.Err(); err != nil {
		// Surface scanner errors for diagnosis. Write to nonProgressOut (owned
		// solely by THIS goroutine) - NOT stderr: os/exec's internal stderr
		// copier goroutine writes &stderr concurrently until cmd.Wait() returns,
		// so touching it here would be a data race. nonProgressOut is already
		// concatenated ahead of stderr into `combined`, preserving ordering.
		nonProgressOut.WriteString("\n[stdout scanner error: " + err.Error() + "]\n")
	}

	// Drain any stdout the scanner did not consume (e.g. it stopped early on
	// bufio.ErrTooLong). Without this the child can block writing to a full
	// stdout pipe and never exit, deadlocking cmd.Wait(). At normal EOF this
	// returns immediately. Discarding is fine: progress was already parsed and
	// the trailing error/advice text we classify on is on stderr/parsed lines.
	_, _ = io.Copy(io.Discard, stdout)

	waitErr := cmd.Wait()
	// StderrTail includes both non-progress stdout and actual stderr so all
	// diagnostic output is visible to the caller and tests.
	combined := nonProgressOut.String() + stderr.String()
	res := &backend.Result{StderrTail: tail(combined, 2000)}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if waitErr != nil {
		return res, adviseErr(combined, waitErr)
	}
	// gyroflow exits 0 even when a render fails, so the exit code alone would
	// report a failed render as completed.
	if strings.Contains(combined, "Rendering failed") {
		return res, fmt.Errorf("gyroflow render failed: %s", tail(combined, 500))
	}
	// Primary: the paths gyroflow itself reported on the Queue line.
	// Fallback (best-effort, only if no Queue line was parseable): derive
	// dir(input)/stem(input)+suffix+ext(input). Default suffix "_stabilized"
	// is empirically determined from a real gyroflow render (default suffix
	// "_stabilized" confirmed with testdata/sample_gyro.mp4).
	if len(outputPaths) == 0 {
		outputPaths = fallbackOutputPaths(req)
	}
	res.OutputPaths = outputPaths
	if onProgress != nil && len(tracker.files) > 0 {
		onProgress(tracker.finish())
	}
	return res, nil
}

// fallbackOutputPaths derives the expected gyroflow output path per input
// when no Queue line could be parsed. Best-effort only; the Queue-line parse
// is primary. suffix = req.Suffix when set, else gyroflow's default
// "_stabilized" (empirically determined from a real gyroflow render).
func fallbackOutputPaths(req backend.StabilizeRequest) []string {
	suffix := req.Suffix
	if suffix == "" {
		suffix = "_stabilized"
	}
	out := make([]string, 0, len(req.Inputs))
	for _, in := range req.Inputs {
		ext := filepath.Ext(in)
		stem := strings.TrimSuffix(in, ext)
		out = append(out, stem+suffix+ext)
	}
	return out
}

func (b *CLIBackend) ExportProject(ctx context.Context, req backend.ExportRequest) (*backend.Result, error) {
	var err error
	req, err = resolveExportReq(req)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, b.bin, exportProjectArgv(req)...)
	var combined tailBuffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	if err := cmd.Run(); err != nil {
		raw := combined.String()
		return &backend.Result{StderrTail: tail(raw, 2000)}, adviseErr(raw, err)
	}
	res := &backend.Result{StderrTail: tail(combined.String(), 2000)}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	return res, nil
}

func (b *CLIBackend) ExportSTMap(ctx context.Context, req backend.STMapRequest) (*backend.Result, error) {
	if req.Type != 1 && req.Type != 2 {
		return nil, fmt.Errorf("export_stmap: type must be 1 (single frame) or 2 (all frames), got %d", req.Type)
	}
	in, err := absInput(req.Input)
	if err != nil {
		return nil, err
	}
	outAbs, err := absOut(req.OutFolder)
	if err != nil {
		return nil, err
	}
	req.Input, req.OutFolder = in, outAbs
	cmd := exec.CommandContext(ctx, b.bin, exportSTMapArgv(req)...)
	var combined tailBuffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	if err := cmd.Run(); err != nil {
		raw := combined.String()
		return &backend.Result{StderrTail: tail(raw, 2000)}, adviseErr(raw, err)
	}
	res := &backend.Result{StderrTail: tail(combined.String(), 2000)}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	return res, nil
}

// metadataRawPath returns a DETERMINISTIC temp path keyed by the absolute
// input path: re-probing the same file overwrites its single export rather
// than accumulating a unique ~1.7MB orphan per call. Bounded by the number of
// distinct inputs, never by call count (same bounded-resource rule as
// tailBuffer). sha256 keeps the filename safe and fixed-length regardless of
// the input path's characters/length.
func metadataRawPath(absInput string) string {
	sum := sha256.Sum256([]byte(absInput))
	return filepath.Join(os.TempDir(), "gyroflow-meta-"+hex.EncodeToString(sum[:])+".json")
}

func (b *CLIBackend) ProbeMetadata(ctx context.Context, input string, opts backend.ProbeOptions) (*backend.MetadataResult, error) {
	absIn, err := absInput(input)
	if err != nil {
		return nil, err
	}
	// Deterministic, returnable path (see metadataRawPath): created/overwritten
	// here and NOT removed on success. The caller gets the path in
	// MetadataResult.RawPath and may read the full blob on demand; it is never
	// inlined into the result or an MCP response.
	return b.probeTo(ctx, absIn, metadataRawPath(absIn), opts)
}

// probeTo exports metadata for absIn to rawPath and summarizes it.
func (b *CLIBackend) probeTo(ctx context.Context, absIn, rawPath string, opts backend.ProbeOptions) (*backend.MetadataResult, error) {
	kind := opts.Kind
	if kind == 0 {
		kind = 2
	}

	cmd := exec.CommandContext(ctx, b.bin, probeMetadataArgv(absIn, kind, rawPath, opts.Fields)...)
	var combined tailBuffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	if err := cmd.Run(); err != nil {
		os.Remove(rawPath) // no usable export produced; don't leak an empty temp
		raw := combined.String()
		if advised := adviseErr(raw, err); advised != err {
			return nil, advised
		}
		return nil, fmt.Errorf("gyroflow export-metadata: %w (output: %s)", err, tail(raw, 500))
	}
	if kind != 2 {
		// kinds 1/3: do not load the (possibly multi-MB) export into memory;
		// just confirm gyroflow actually wrote it. Caller reads RawPath on demand.
		if _, statErr := os.Stat(rawPath); statErr != nil {
			os.Remove(rawPath)
			return nil, fmt.Errorf("gyroflow export-metadata: output missing: %w", statErr)
		}
		return &backend.MetadataResult{RawPath: rawPath}, nil
	}
	data, err := os.ReadFile(rawPath)
	if err != nil {
		os.Remove(rawPath)
		return nil, fmt.Errorf("reading metadata output: %w", err)
	}
	summary, err := parseMetadataSummary(data)
	if err != nil {
		os.Remove(rawPath)
		return nil, fmt.Errorf("gyroflow export-metadata: %w", err)
	}
	return &backend.MetadataResult{Summary: summary, RawPath: rawPath}, nil
}

// tail returns the last n bytes of s (trimmed), or all of s if shorter.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// prepareStabilize validates Config, builds the inline preset JSON (overlaying
// any on-disk PresetPath as the base), and routes Output/Sync to -p/-s. It is
// pure (no exec) so it is unit-testable; Stabilize calls it before running.
func prepareStabilize(req backend.StabilizeRequest) (backend.StabilizeRequest, error) {
	if req.Config == nil {
		if req.PresetPath == "" {
			return req, nil
		}
		base, err := os.ReadFile(req.PresetPath)
		if err != nil {
			return req, fmt.Errorf("reading preset_path %s: %w", req.PresetPath, err)
		}
		var obj map[string]any
		if err := json.Unmarshal(base, &obj); err != nil {
			return req, fmt.Errorf("preset_path %s is not valid JSON: %w", req.PresetPath, err)
		}
		return inlinePreset(req, obj)
	}
	if err := validateConfig(req.Config); err != nil {
		return req, err
	}
	b, err := buildPresetJSON(req.Config)
	if err != nil {
		return req, err
	}
	if req.PresetPath != "" {
		base, rerr := os.ReadFile(req.PresetPath)
		if rerr != nil {
			return req, fmt.Errorf("reading preset_path %s: %w", req.PresetPath, rerr)
		}
		var baseObj, genObj map[string]any
		if jerr := json.Unmarshal(base, &baseObj); jerr != nil {
			return req, fmt.Errorf("preset_path %s is not valid JSON: %w", req.PresetPath, jerr)
		}
		if jerr := json.Unmarshal(b, &genObj); jerr != nil {
			return req, fmt.Errorf("internal: generated preset is not valid JSON (bug): %w", jerr)
		}
		deepMerge(baseObj, genObj) // file = base, generated config overrides
		if b, err = json.Marshal(baseObj); err != nil {
			return req, err
		}
	}
	var merged map[string]any
	if err := json.Unmarshal(b, &merged); err != nil {
		return req, fmt.Errorf("internal: preset is not valid JSON (bug): %w", err)
	}
	if req, err = inlinePreset(req, merged); err != nil {
		return req, err
	}
	if o := req.Config.Output; o != nil {
		if req.OutParams == nil {
			req.OutParams = backend.OutParams{}
		}
		routeOutput(o, req.OutParams)
	}
	if s := req.Config.Sync; s != nil {
		if req.SyncParams == nil {
			req.SyncParams = backend.SyncParams{}
		}
		routeSync(s, req.SyncParams)
	}
	return req, nil
}

// inlinePreset stores preset as the inline --preset value, without the location
// fields a GUI-exported preset may carry: gyroflow would prefer them over -p and
// send every render to the folder the preset was exported from.
func inlinePreset(req backend.StabilizeRequest, preset map[string]any) (backend.StabilizeRequest, error) {
	if out, ok := preset["output"].(map[string]any); ok {
		delete(out, "output_folder")
		delete(out, "output_filename")
	}
	b, err := json.Marshal(preset)
	if err != nil {
		return req, err
	}
	req.PresetJSON = string(b)
	req.PresetPath = "" // consumed into PresetJSON
	return req, nil
}

func routeOutput(o *backend.OutputConfig, m backend.OutParams) {
	if o.Codec != nil {
		m["codec"] = *o.Codec
	}
	if o.CodecOptions != nil {
		m["codec_options"] = *o.CodecOptions
	}
	if o.Bitrate != nil {
		m["bitrate"] = *o.Bitrate
	}
	if o.UseGPU != nil {
		m["use_gpu"] = *o.UseGPU
	}
	if o.Audio != nil {
		m["audio"] = *o.Audio
	}
	if o.PixelFormat != nil {
		m["pixel_format"] = *o.PixelFormat
	}
	if o.OutputWidth != nil {
		m["output_width"] = *o.OutputWidth
	}
	if o.OutputHeight != nil {
		m["output_height"] = *o.OutputHeight
	}
	if o.AudioCodec != nil {
		m["audio_codec"] = *o.AudioCodec
	}
	if o.Interpolation != nil {
		m["interpolation"] = *o.Interpolation
	}
	if o.KeyframeDistance != nil {
		m["keyframe_distance"] = *o.KeyframeDistance
	}
	if o.PadWithBlack != nil {
		m["pad_with_black"] = *o.PadWithBlack
	}
	if o.PreserveOtherTracks != nil {
		m["preserve_other_tracks"] = *o.PreserveOtherTracks
	}
	if o.ExportTrimsSeparately != nil {
		m["export_trims_separately"] = *o.ExportTrimsSeparately
	}
	if o.EncoderOptions != nil {
		m["encoder_options"] = *o.EncoderOptions
	}
	if o.MetadataComment != nil {
		m["metadata"] = map[string]any{"comment": *o.MetadataComment}
	}
}

func routeSync(s *backend.SyncConfig, m backend.SyncParams) {
	if s.InitialOffset != nil {
		m["initial_offset"] = *s.InitialOffset
	}
	if s.InitialOffsetInv != nil {
		m["initial_offset_inv"] = *s.InitialOffsetInv
	}
	if s.SearchSize != nil {
		m["search_size"] = *s.SearchSize
	}
	if s.CalcInitialFast != nil {
		m["calc_initial_fast"] = *s.CalcInitialFast
	}
	if s.MaxSyncPoints != nil {
		m["max_sync_points"] = *s.MaxSyncPoints
	}
	if s.EveryNthFrame != nil {
		m["every_nth_frame"] = *s.EveryNthFrame
	}
	if s.TimePerSyncpoint != nil {
		m["time_per_syncpoint"] = *s.TimePerSyncpoint
	}
	if s.OfMethod != nil {
		m["of_method"] = *s.OfMethod
	}
	if s.OffsetMethod != nil {
		m["offset_method"] = *s.OffsetMethod
	}
	if s.PoseMethod != nil {
		m["pose_method"] = *s.PoseMethod
	}
	if s.AutoSyncPoints != nil {
		m["auto_sync_points"] = *s.AutoSyncPoints
	}
	if s.ProcessingResolution != nil {
		m["processing_resolution"] = *s.ProcessingResolution
	}
}
