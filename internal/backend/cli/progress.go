package cli

import (
	"net/url"
	"path"
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
// The ETA is deliberately not captured: gyroflow's estimate is far off for most
// of a render (20 minutes reported for a job that took 2).
// Capture groups: (1) job id, (2) frame, (3) total, (4) percent.
var progressRe = regexp.MustCompile(`^\[([0-9a-fA-F]+)\]\s+Rendering progress:\s+(\d+)/(\d+)\s+frames\s+\(([0-9.]+)%\)\s+ETA\s+[0-9.]+s`)

// progressSample is one parsed --stdout-progress line.
type progressSample struct {
	JobID   string
	Frame   int
	Total   int
	Percent float64
}

func parseProgressLine(line string) (progressSample, bool) {
	m := progressRe.FindStringSubmatch(line)
	if m == nil {
		return progressSample{}, false
	}
	fr, _ := strconv.Atoi(m[2])
	tot, _ := strconv.Atoi(m[3])
	pct, _ := strconv.ParseFloat(m[4], 64)
	return progressSample{JobID: m[1], Frame: fr, Total: tot, Percent: pct}, true
}

// queueEntryRe extracts the job id, source and frame count from a Queue line:
//
//   - [179020fa] file:///dir/b.mp4 -> /dir/out/b_stabilized.mp4, 3840x2880 59.940fps | H.265/HEVC 124 Mbps, Frames: 270, Status: Rendering
var queueEntryRe = regexp.MustCompile(`\[([0-9a-fA-F]+)\] file://(\S*) -> .*, Frames: (\d+)`)

// progressTracker turns gyroflow's per-file samples into whole-job progress.
//
// gyroflow renders one file at a time because stabilizeArgv never passes -j, so
// a sample for another job id means the previous file ended.
type progressTracker struct {
	files []backend.FileProgress
	index map[string]int
}

func newProgressTracker() *progressTracker {
	return &progressTracker{index: map[string]int{}}
}

// add registers a file once, keeping the order gyroflow queued them in, which
// is the order it renders them in and not necessarily the order of the inputs.
func (t *progressTracker) add(id, name string, total int) int {
	if i, ok := t.index[id]; ok {
		return i
	}
	t.index[id] = len(t.files)
	t.files = append(t.files, backend.FileProgress{Name: name, Total: total, State: backend.FilePending})
	return len(t.files) - 1
}

// queueLine records the file a Queue line announces, if line is one.
func (t *progressTracker) queueLine(line string) {
	m := queueEntryRe.FindStringSubmatch(line)
	if m == nil {
		return
	}
	name := path.Base(m[2])
	if decoded, err := url.PathUnescape(name); err == nil {
		name = decoded
	}
	total, _ := strconv.Atoi(m[3])
	t.add(m[1], name, total)
}

// sample applies one progress line and returns the updated job progress.
func (t *progressTracker) sample(s progressSample) backend.Progress {
	cur := t.add(s.JobID, s.JobID, s.Total)
	for i := range t.files {
		if i != cur && t.files[i].State == backend.FileRendering {
			t.finishFile(i)
		}
	}
	f := &t.files[cur]
	f.State, f.Frame, f.Total, f.Percent = backend.FileRendering, s.Frame, s.Total, s.Percent
	return t.snapshot()
}

// finish marks every file done, once gyroflow has exited successfully.
func (t *progressTracker) finish() backend.Progress {
	for i := range t.files {
		t.finishFile(i)
	}
	return t.snapshot()
}

func (t *progressTracker) finishFile(i int) {
	f := &t.files[i]
	f.State, f.Frame, f.Percent = backend.FileDone, f.Total, 100
}

// snapshot copies the files, because each Progress outlives this call.
func (t *progressTracker) snapshot() backend.Progress {
	p := backend.Progress{Stage: "render", Files: append([]backend.FileProgress(nil), t.files...)}
	for _, f := range t.files {
		p.Frame += f.Frame
		p.Total += f.Total
	}
	if p.Total > 0 {
		p.Percent = float64(p.Frame) / float64(p.Total) * 100
	}
	return p
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
