package mcpsurface

import (
	"fmt"
	"math"
	"strings"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
	"github.com/carlosFreak/gyroflow-mcp/internal/jobengine"
)

const progressBarWidth = 24

// progressBar renders a job as text for clients to show verbatim. A running job
// with several files gets one line for the current file and one for the total:
//
//	[██████████░░░░░░░░░░░░░░] 44.8% · 121/270 frames · file 1/2: b.mp4
//	[███████░░░░░░░░░░░░░░░░░] 31.0% · 121/390 frames · total
func progressBar(j jobengine.Job) string {
	if j.State == jobengine.StateCompleted {
		return bar(100) + " · completed"
	}
	if j.State != jobengine.StateRunning {
		return bar(j.Progress.Percent) + " · " + string(j.State)
	}
	stage := ""
	if j.Progress.Stage == "lut" {
		stage = " · applying LUT"
	}
	total := bar(j.Progress.Percent) + frames(j.Progress.Frame, j.Progress.Total)
	if len(j.Progress.Files) < 2 {
		return total + stage
	}
	for i, f := range j.Progress.Files {
		if f.State == backend.FileRendering {
			return fmt.Sprintf("%s%s · file %d/%d: %s%s\n%s · total",
				bar(f.Percent), frames(f.Frame, f.Total), i+1, len(j.Progress.Files), f.Name, stage, total)
		}
	}
	return total + " · total" + stage
}

// bar renders one bar and its percentage, clamping percent to 0-100.
func bar(percent float64) string {
	percent = math.Max(0, math.Min(100, percent))
	filled := int(percent / 100 * progressBarWidth)
	return fmt.Sprintf("[%s%s] %.1f%%",
		strings.Repeat("█", filled), strings.Repeat("░", progressBarWidth-filled), percent)
}

// frames renders a frame counter, or nothing while the total is unknown.
func frames(frame, total int) string {
	if total <= 0 {
		return ""
	}
	return fmt.Sprintf(" · %d/%d frames", frame, total)
}
