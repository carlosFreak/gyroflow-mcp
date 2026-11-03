package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
)

// A LUT render first has gyroflow write a near-lossless intermediate, then
// ffmpeg applies the LUT and encodes the final file once. HEVC 10-bit at
// 400 Mbps is about 3 GB a minute in 4K60, a third of what ProRes takes.
const (
	lutIntermediateCodec   = "H.265/HEVC"
	lutIntermediateBitrate = 400.0 // Mbps
	lutIntermediateSuffix  = "_lutsrc"
	// lutSpaceFactor estimates the disk an intermediate needs from the source
	// size: drone clips are about 100-150 Mbps against the 400 Mbps intermediate.
	lutSpaceFactor = 4
)

// FindFFmpeg returns the ffmpeg binary, or "" when it is not installed. The
// server often runs with a minimal PATH, so Homebrew's folders are tried too.
func FindFFmpeg() string {
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	for _, p := range []string{"/opt/homebrew/bin/ffmpeg", "/usr/local/bin/ffmpeg"} {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// LUTBackend applies StabilizeRequest.LUT after the inner backend renders.
// Requests without a LUT pass straight through.
type LUTBackend struct {
	backend.Backend
	ffmpeg string
}

// NewLUT wraps inner. ffmpeg may be "": LUT requests then fail with advice.
func NewLUT(inner backend.Backend, ffmpeg string) *LUTBackend {
	return &LUTBackend{Backend: inner, ffmpeg: ffmpeg}
}

// lutEncoder is how ffmpeg writes the final file for one gyroflow codec.
type lutEncoder struct {
	args []string
	ext  string
}

func encoderFor(codec string, mbps float64) (lutEncoder, error) {
	br := strconv.FormatFloat(mbps, 'f', -1, 64) + "M"
	switch codec {
	case "H.265/HEVC":
		return lutEncoder{[]string{"-c:v", "hevc_videotoolbox", "-profile:v", "main10", "-pix_fmt", "p010le", "-tag:v", "hvc1", "-b:v", br}, ".mp4"}, nil
	case "H.264/AVC":
		return lutEncoder{[]string{"-c:v", "h264_videotoolbox", "-pix_fmt", "yuv420p", "-b:v", br}, ".mp4"}, nil
	case "ProRes":
		return lutEncoder{[]string{"-c:v", "prores_videotoolbox", "-profile:v", "standard"}, ".mov"}, nil
	}
	return lutEncoder{}, fmt.Errorf("a LUT can only be applied with codec H.265/HEVC, H.264/AVC or ProRes, not %q", codec)
}

// finalOutput is the codec and bitrate the user asked for: typed config, then
// raw out_params, then the preset file, then the source's own.
func (b *LUTBackend) finalOutput(ctx context.Context, req backend.StabilizeRequest) (codec string, mbps float64) {
	if req.PresetPath != "" {
		if raw, err := os.ReadFile(req.PresetPath); err == nil {
			var p struct {
				Output struct {
					Codec   string  `json:"codec"`
					Bitrate float64 `json:"bitrate"`
				} `json:"output"`
			}
			if json.Unmarshal(raw, &p) == nil {
				codec, mbps = p.Output.Codec, p.Output.Bitrate
			}
		}
	}
	if v, ok := req.OutParams["codec"].(string); ok && v != "" {
		codec = v
	}
	if v, ok := req.OutParams["bitrate"].(float64); ok && v > 0 {
		mbps = v
	}
	if o := req.Config; o != nil && o.Output != nil {
		if o.Output.Codec != nil {
			codec = *o.Output.Codec
		}
		if o.Output.Bitrate != nil {
			mbps = *o.Output.Bitrate
		}
	}
	if codec == "" || mbps <= 0 {
		srcCodec, srcMbps := b.probeVideo(ctx, req.Inputs[0])
		if codec == "" {
			codec = srcCodec
		}
		if mbps <= 0 {
			mbps = srcMbps
		}
	}
	if codec == "" {
		codec = "H.265/HEVC"
	}
	if mbps <= 0 {
		mbps = 100
	}
	return codec, mbps
}

// probeVideo returns the gyroflow codec name and Mbps of path's video, or zero
// values when ffprobe cannot tell.
func (b *LUTBackend) probeVideo(ctx context.Context, path string) (string, float64) {
	out, err := exec.CommandContext(ctx, b.ffprobe(), "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name,bit_rate", "-of", "default=nw=1", path).Output()
	if err != nil {
		return "", 0
	}
	var codec string
	var mbps float64
	for _, line := range strings.Split(string(out), "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch k {
		case "codec_name":
			codec = map[string]string{"hevc": "H.265/HEVC", "h264": "H.264/AVC", "prores": "ProRes"}[v]
		case "bit_rate":
			if n, err := strconv.ParseFloat(v, 64); err == nil {
				mbps = math.Round(n / 1e6)
			}
		}
	}
	return codec, mbps
}

func (b *LUTBackend) ffprobe() string {
	return filepath.Join(filepath.Dir(b.ffmpeg), "ffprobe")
}

func (b *LUTBackend) Stabilize(ctx context.Context, req backend.StabilizeRequest, onProgress backend.ProgressFunc) (*backend.Result, error) {
	if req.LUT == "" && req.Color.IsZero() {
		return b.Backend.Stabilize(ctx, req, onProgress)
	}
	if b.ffmpeg == "" {
		return nil, fmt.Errorf("a LUT or color adjustments need ffmpeg: install it with `brew install ffmpeg`, or render without them")
	}
	if err := req.Color.Validate(); err != nil {
		return nil, err
	}
	if len(req.Inputs) == 0 {
		return nil, fmt.Errorf("no inputs")
	}
	codec, mbps := b.finalOutput(ctx, req)
	enc, err := encoderFor(codec, mbps)
	if err != nil {
		return nil, err
	}
	suffix := req.Suffix
	if suffix == "" {
		suffix = "_stabilized"
	}
	destDir, err := requestedOutputFolder(req.OutParams)
	if err != nil {
		return nil, err
	}
	var need int64
	for _, in := range req.Inputs {
		dir := destDir
		if dir == "" {
			dir = filepath.Dir(in)
		}
		final := filepath.Join(dir, stem(filepath.Base(in))+suffix+enc.ext)
		if !req.Overwrite {
			if _, err := os.Lstat(final); err == nil {
				return nil, fmt.Errorf("output already exists: %s (set overwrite or use another suffix)", final)
			}
		}
		if info, err := os.Stat(in); err == nil {
			need += info.Size() * lutSpaceFactor
		}
		if free, ok := freeBytes(dir); ok && uint64(need) > free {
			return nil, fmt.Errorf("not enough space for the LUT intermediate: need about %.1f GB in %s, %.1f GB free", float64(need)/1e9, dir, float64(free)/1e9)
		}
	}

	inter := req
	inter.LUT, inter.Color = "", nil
	inter.Suffix = suffix + lutIntermediateSuffix
	inter.Overwrite = true // its name is ours; the final file was checked above
	cfg := backend.GyroflowConfig{}
	if req.Config != nil {
		cfg = *req.Config
	}
	out := backend.OutputConfig{}
	if cfg.Output != nil {
		out = *cfg.Output
	}
	ic, ib, gpu, none := lutIntermediateCodec, lutIntermediateBitrate, true, ""
	out.Codec, out.Bitrate, out.UseGPU, out.CodecOptions = &ic, &ib, &gpu, &none
	cfg.Output = &out
	inter.Config = &cfg
	inter.OutParams = backend.OutParams{}
	for k, v := range req.OutParams {
		if k != "codec" && k != "bitrate" && k != "codec_options" {
			inter.OutParams[k] = v
		}
	}

	res, err := b.Backend.Stabilize(ctx, inter, onProgress)
	if err != nil {
		if res != nil {
			removeAll(res.OutputPaths)
		}
		return res, err
	}
	finals, err := b.applyGrade(ctx, req.LUT, req.Color, res.OutputPaths, enc, onProgress)
	removeAll(res.OutputPaths)
	res.OutputPaths = finals
	return res, err
}

func removeAll(paths []string) {
	for _, p := range paths {
		os.Remove(p)
	}
}

// gradeFilter is the ffmpeg -vf chain for a LUT and color adjustments. A
// non-empty cube is copied into dir first: ffmpeg's filter syntax reserves
// ':' '\” and '\\', and a plain name keeps the user's file name out of it.
func gradeFilter(ctx context.Context, dir, cube string, c *backend.ColorAdjust) (string, error) {
	var chain []string
	if cube != "" {
		safe := filepath.Join(dir, "lut.cube")
		if err := copyFile(ctx, cube, safe); err != nil {
			return "", err
		}
		chain = append(chain, "lut3d=file='"+safe+"'")
	}
	return strings.Join(append(chain, colorFilters(c)...), ","), nil
}

// colorFilters turns c into ffmpeg filters that keep high precision: ffmpeg's
// eq filter would drop to 8 bits and band skies. Everything runs in 16-bit RGB
// (exposure and saturation as one color matrix), then sharpening on 10-bit
// YUV. 16-bit is about a third faster than float and loses nothing visible.
func colorFilters(c *backend.ColorAdjust) []string {
	if c.IsZero() {
		return nil
	}
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 4, 64) }
	var out []string
	if c.Exposure != 0 || c.Warmth != 0 || c.Saturation != 0 || c.Contrast != 0 || c.Vibrance != 0 {
		out = append(out, "format=gbrp16le")
	}
	if c.Warmth != 0 {
		// 6500 K is neutral; lower is warmer. pl=1 keeps the brightness.
		out = append(out, "colortemperature=temperature="+f(6500-2500*c.Warmth)+":pl=1")
	}
	if c.Exposure != 0 || c.Saturation != 0 {
		// gain is the exposure in stops; Rec.709 luma weights keep the
		// brightness while scaling the color.
		g, s := math.Pow(2, c.Exposure), 1+c.Saturation
		lr, lg, lb := 0.2126*(1-s), 0.7152*(1-s), 0.0722*(1-s)
		out = append(out, fmt.Sprintf("colorchannelmixer=rr=%s:rg=%s:rb=%s:gr=%s:gg=%s:gb=%s:br=%s:bg=%s:bb=%s",
			f(g*(lr+s)), f(g*lg), f(g*lb), f(g*lr), f(g*(lg+s)), f(g*lb), f(g*lr), f(g*lg), f(g*(lb+s))))
	}
	if c.Contrast != 0 {
		k := 0.1 * c.Contrast // an S-curve: shadows down and highlights up
		out = append(out, fmt.Sprintf("curves=all='0/0 0.25/%s 0.75/%s 1/1'", f(0.25-k), f(0.75+k)))
	}
	if c.Vibrance != 0 {
		out = append(out, "vibrance=intensity="+f(c.Vibrance))
	}
	if c.Sharpness != 0 {
		out = append(out, "format=yuv420p10le", "unsharp=5:5:"+f(1.5*c.Sharpness))
	}
	return out
}

// applyGrade writes one final file per intermediate, reporting stage "lut"
// (or "color" when only color adjustments apply).
func (b *LUTBackend) applyGrade(ctx context.Context, cube string, color *backend.ColorAdjust, inters []string, enc lutEncoder, onProgress backend.ProgressFunc) ([]string, error) {
	tmp, err := os.MkdirTemp("", "gyroflow-lut-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	vf, err := gradeFilter(ctx, tmp, cube, color)
	if err != nil {
		return nil, err
	}
	stage := "lut"
	if cube == "" {
		stage = "color"
	}

	files := make([]backend.FileProgress, len(inters))
	for i, p := range inters {
		files[i] = backend.FileProgress{Name: finalName(p, enc.ext), Total: b.frameCount(ctx, p), State: backend.FilePending}
	}
	report := func() {
		if onProgress == nil {
			return
		}
		p := backend.Progress{Stage: stage, Files: append([]backend.FileProgress(nil), files...)}
		for _, f := range files {
			p.Frame += f.Frame
			p.Total += f.Total
		}
		if p.Total > 0 {
			p.Percent = float64(p.Frame) / float64(p.Total) * 100
		}
		onProgress(p)
	}

	var finals []string
	for i, inter := range inters {
		final := filepath.Join(filepath.Dir(inter), files[i].Name)
		partial := strings.TrimSuffix(final, enc.ext) + ".partial" + enc.ext
		files[i].State = backend.FileRendering
		report()
		// Hardware decoding frees the CPU for the filters; ffmpeg falls back
		// to software when it is unavailable.
		args := append([]string{"-v", "error", "-nostats", "-progress", "pipe:1", "-y", "-hwaccel", "videotoolbox", "-i", inter,
			"-map", "0:v:0", "-map", "0:a?", "-map_metadata", "0",
			"-vf", vf}, enc.args...)
		args = append(args, "-c:a", "copy", partial)
		cmd := exec.CommandContext(ctx, b.ffmpeg, args...)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return finals, err
		}
		var stderr tailBuffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			return finals, err
		}
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), "frame="); ok {
				if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
					files[i].Frame = min(n, files[i].Total)
					if files[i].Total > 0 {
						files[i].Percent = float64(files[i].Frame) / float64(files[i].Total) * 100
					}
					report()
				}
			}
		}
		if err := cmd.Wait(); err != nil {
			os.Remove(partial)
			return finals, fmt.Errorf("applying the LUT/color to %s failed: %v: %s", final, err, tail(stderr.String(), 500))
		}
		if err := os.Rename(partial, final); err != nil {
			os.Remove(partial)
			return finals, err
		}
		files[i].State, files[i].Frame, files[i].Percent = backend.FileDone, files[i].Total, 100
		report()
		finals = append(finals, final)
	}
	return finals, nil
}

// finalName drops the intermediate marker: clip_stabilized_lutsrc.mp4 →
// clip_stabilized.mp4 (or .mov for ProRes).
func finalName(inter, ext string) string {
	base := stem(filepath.Base(inter))
	return strings.TrimSuffix(base, lutIntermediateSuffix) + ext
}

// frameCount is the video frame count of path, 0 when unknown.
func (b *LUTBackend) frameCount(ctx context.Context, path string) int {
	out, err := exec.CommandContext(ctx, b.ffprobe(), "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=nb_frames", "-of", "csv=p=0", path).Output()
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

// Preview writes a before/after JPEG of one source frame at second at, with
// the LUT and color applied to the right half, to out. The
// frame is the unstabilized source: it shows color, not framing.
func (b *LUTBackend) Preview(ctx context.Context, input string, at float64, cube string, color *backend.ColorAdjust, out string) error {
	if b.ffmpeg == "" {
		return fmt.Errorf("previews need ffmpeg: install it with `brew install ffmpeg`")
	}
	if err := color.Validate(); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "gyroflow-preview-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	vf, err := gradeFilter(ctx, tmp, cube, color)
	if err != nil {
		return err
	}
	after := "[b]"
	if vf != "" {
		after = "[b]" + vf + ","
	} else {
		after = "[b]null,"
	}
	graph := "[0:v]scale=960:-2,format=yuv420p10le,split[a][b];" + after + "format=yuv420p[g];[a]format=yuv420p[o];[o][g]hstack"
	cmd := exec.CommandContext(ctx, b.ffmpeg, "-v", "error", "-y", "-ss", strconv.FormatFloat(at, 'f', 2, 64), "-i", input,
		"-frames:v", "1", "-filter_complex", graph, "-q:v", "3", out)
	if o, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("preview failed: %v: %s", err, tail(string(o), 500))
	}
	return nil
}
