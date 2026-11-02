package cli

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
)

// progressRe matches the raw --stdout-progress lines emitted by gyroflow.
// Real format verified from testdata/stdout_progress_sample.txt:
//
//	[<jobid hex>] Rendering progress: <frame>/<total> frames (<pct>%) ETA <eta>s
//
// Key distinguishing feature: NO leading timestamp/level prefix (log lines
// have "HH:MM:SS [LEVEL]" prefix; progress lines are raw).
// Capture groups: (1) frame, (2) total, (3) percent, (4) ETA seconds.
var progressRe = regexp.MustCompile(`^\[[0-9a-fA-F]+\]\s+Rendering progress:\s+(\d+)/(\d+)\s+frames\s+\(([0-9.]+)%\)\s+ETA\s+([0-9.]+)s`)

func parseProgressLine(line string) (backend.Progress, bool) {
	m := progressRe.FindStringSubmatch(line)
	if m == nil {
		return backend.Progress{}, false
	}
	fr, _ := strconv.Atoi(m[1])
	tot, _ := strconv.Atoi(m[2])
	pct, _ := strconv.ParseFloat(m[3], 64)
	eta, _ := strconv.ParseFloat(m[4], 64)
	return backend.Progress{Percent: pct, Frame: fr, Total: tot, ETA: eta, Stage: "render"}, true
}

// queueOutputRe extracts the RESOLVED output path from gyroflow's
// --stdout-progress "Queue:" entry line. Real format pinned verbatim from
// testdata/stdout_progress_sample.txt line 20 (real DMG build, ANSI codes in
// the line prefix only - never inside the path region):
//
//	Queue line format: [<jobid>] file://<src> -> <abs-out>, <W>x<H> <fps>fps | <codec> <bitrate>, Frames: <n>, Status: Rendering
//
// The leading "file://" is the Queue-entry marker: it distinguishes this from
// the OpenCL "for (W, H, S) -> (W, H, S), key:" lines that also contain
// " -> " and a comma. Capture group 1 is the output path, taken between
// " -> " and the ", <W>x<H> <fps>fps" tail. This is authoritative - it
// reflects -t suffix / out_params / container changes with no naming guess.
var queueOutputRe = regexp.MustCompile(`file://\S* -> (.+?), \d+x\d+ [0-9.]+fps`)

// parseQueueOutputPath returns the resolved output path from a gyroflow
// Queue-entry line, or ("", false) for any other line.
func parseQueueOutputPath(line string) (string, bool) {
	m := queueOutputRe.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	p := strings.TrimSpace(m[1])
	if p == "" {
		return "", false
	}
	return p, true
}
