package cli

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
)

// TestParseProgressLine verifies the real gyroflow --stdout-progress format
// captured in testdata/stdout_progress_sample.txt (real DMG build, non-sandboxed).
// Real format: [<jobid hex>] Rendering progress: <frame>/<total> frames (<pct>%) ETA <eta>s
func TestParseProgressLine(t *testing.T) {
	// Positive cases: verbatim lines from testdata/stdout_progress_sample.txt,
	// pinned to exact field values (frame/total/percent).
	positive := []struct {
		line    string
		frame   int
		total   int
		percent float64
	}{
		{"[0f271661] Rendering progress: 1/1384 frames (0.1%) ETA 2347.0s", 1, 1384, 0.1},
		{"[0f271661] Rendering progress: 10/1384 frames (0.7%) ETA 1439.9s", 10, 1384, 0.7},
		{"[0f271661] Rendering progress: 18/1384 frames (1.3%) ETA 835.1s", 18, 1384, 1.3},
	}

	for _, tc := range positive {
		p, ok := parseProgressLine(tc.line)
		if !ok {
			t.Fatalf("expected line to parse: %q", tc.line)
		}
		if p.Frame != tc.frame || p.Total != tc.total || p.Percent != tc.percent || p.JobID != "0f271661" {
			t.Fatalf("line %q: got %+v, want frame=%d total=%d percent=%v",
				tc.line, p, tc.frame, tc.total, tc.percent)
		}
	}

	// Negative cases: read every non-progress line from the committed real fixture
	// WITH real ANSI escape codes intact, and assert parseProgressLine returns false.
	// Fixture is at testdata/stdout_progress_sample.txt (repo root);
	// from this package dir: ../../../testdata/stdout_progress_sample.txt.
	f, err := os.Open("../../../testdata/stdout_progress_sample.txt")
	if err != nil {
		t.Fatalf("opening fixture: %v", err)
	}
	defer f.Close()

	var nonProgressLines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if progressRe.FindStringSubmatch(line) == nil {
			nonProgressLines = append(nonProgressLines, line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scanning fixture: %v", err)
	}
	if len(nonProgressLines) == 0 {
		t.Fatal("fixture contained no non-progress lines - negative cases cannot be verified")
	}

	for _, line := range nonProgressLines {
		if _, ok := parseProgressLine(line); ok {
			t.Fatalf("non-progress fixture line should not parse: %q", line)
		}
	}
}

// TestParseQueueOutputPath pins the output-path parse to the REAL gyroflow
// Queue-entry line in the committed fixture (testdata/stdout_progress_sample.txt,
// real DMG build). It also asserts the OpenCL "... -> (...)" lines that also
// contain " -> " and a comma do NOT false-match.
func TestParseQueueOutputPath(t *testing.T) {
	const wantPath = "/home/user/Documents/personal-projects/gyro-mcp/testdata/sample_gyro_stabilized.mp4"

	f, err := os.Open("../../../testdata/stdout_progress_sample.txt")
	if err != nil {
		t.Fatalf("opening fixture: %v", err)
	}
	defer f.Close()

	var got []string
	openclLines := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if p, ok := parseQueueOutputPath(line); ok {
			got = append(got, p)
		}
		// OpenCL init lines: " -> (W, H, S), key:" - must NOT be parsed as a path.
		if _, ok := parseQueueOutputPath(line); ok && !contains(line, "file://") {
			openclLines++
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scanning fixture: %v", err)
	}
	if openclLines != 0 {
		t.Fatalf("OpenCL ' -> (...)' lines false-matched as output paths (%d)", openclLines)
	}
	if len(got) != 1 || got[0] != wantPath {
		t.Fatalf("parsed Queue output paths = %v, want exactly [%q]", got, wantPath)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// TestFallbackOutputPaths verifies the no-Queue-line fallback derivation:
// default suffix "_stabilized" (confirmed by real gyroflow render) and explicit Suffix override.
func TestFallbackOutputPaths(t *testing.T) {
	got := fallbackOutputPaths(backend.StabilizeRequest{
		Inputs: []string{"/abs/dir/sample_gyro.mp4"},
	})
	if len(got) != 1 || got[0] != "/abs/dir/sample_gyro_stabilized.mp4" {
		t.Fatalf("default-suffix fallback = %v, want [/abs/dir/sample_gyro_stabilized.mp4]", got)
	}
	got = fallbackOutputPaths(backend.StabilizeRequest{
		Inputs: []string{"/abs/dir/a.mp4", "/abs/dir/b.mov"},
		Suffix: "_stab",
	})
	want := []string{"/abs/dir/a_stab.mp4", "/abs/dir/b_stab.mov"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("suffix-override fallback = %v, want %v", got, want)
	}
}

// Lines captured from a real two-file render (gyroflow 1.6.3); paths shortened.
// Files render in Queue order, not input order.
func TestProgressTrackerTwoFiles(t *testing.T) {
	tr := newProgressTracker()
	tr.queueLine("22:04:43 [INFO] - [179020fa] file:///w/my%20clip.mp4 -> /w/out/my clip_stabilized.mp4, 3840x2880 59.940fps | H.265/HEVC 124 Mbps, Frames: 270, Status: Rendering")
	tr.queueLine("22:04:43 [INFO] - [46aab8f2] file:///w/a.mp4 -> /w/out/a_stabilized.mp4, 3840x2880 59.940fps | H.265/HEVC 124 Mbps, Frames: 120, Status: Queued")
	tr.queueLine("22:04:43 [INFO] - [46aab8f2] file:///w/a.mp4 -> /w/out/a_stabilized.mp4, 3840x2880 59.940fps | H.265/HEVC 124 Mbps, Frames: 120, Status: Queued")

	sample := func(line string) backend.Progress {
		t.Helper()
		s, ok := parseProgressLine(line)
		if !ok {
			t.Fatalf("line did not parse: %q", line)
		}
		return tr.sample(s)
	}

	p := sample("[179020fa] Rendering progress: 121/270 frames (44.8%) ETA 7.0s")
	if len(p.Files) != 2 || p.Files[0].Name != "my clip.mp4" || p.Files[1].Name != "a.mp4" {
		t.Fatalf("files = %+v", p.Files)
	}
	if p.Files[0].State != backend.FileRendering || p.Files[1].State != backend.FilePending {
		t.Fatalf("states = %+v", p.Files)
	}
	if p.Frame != 121 || p.Total != 390 {
		t.Fatalf("job frames = %d/%d, want 121/390", p.Frame, p.Total)
	}

	sample("[179020fa] Rendering progress: 270/270 frames (100.0%) ETA 1.2s")
	p = sample("[46aab8f2] Rendering progress: 30/120 frames (25.0%) ETA 9.0s")
	if f := p.Files[0]; f.State != backend.FileDone || f.Percent != 100 || f.Frame != 270 {
		t.Fatalf("first file was not completed: %+v", f)
	}
	if p.Frame != 300 || p.Total != 390 {
		t.Fatalf("job frames = %d/%d, want 300/390", p.Frame, p.Total)
	}

	p = tr.finish()
	if p.Percent != 100 || p.Files[1].State != backend.FileDone {
		t.Fatalf("after finish: %+v", p)
	}
}

// A progress line with no announced Queue entry still counts as a file.
func TestProgressTrackerWithoutQueueLine(t *testing.T) {
	tr := newProgressTracker()
	p := tr.sample(progressSample{JobID: "abc", Frame: 1, Total: 2, Percent: 50})
	if p.Percent != 50 || p.Frame != 1 || p.Total != 2 || len(p.Files) != 1 {
		t.Fatalf("got %+v", p)
	}
}
