package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
)

func TestStabilizeArgvMinimal(t *testing.T) {
	got := stabilizeArgv(backend.StabilizeRequest{Inputs: []string{"clip.mp4"}})
	want := []string{"clip.mp4", "--stdout-progress"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
}

func TestStabilizeArgvWithOptions(t *testing.T) {
	got := stabilizeArgv(backend.StabilizeRequest{
		Inputs:     []string{"clip.mp4"},
		PresetPath: "p.gyroflow",
		GyroFile:   "g.bbl",
		Overwrite:  true,
		Suffix:     "_stab",
	})
	want := []string{
		"clip.mp4", "--stdout-progress",
		"--preset", "p.gyroflow",
		"-g", "g.bbl",
		"-f",
		"-t", "_stab",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
}

func TestExportProjectArgv(t *testing.T) {
	got := exportProjectArgv(backend.ExportRequest{Input: "c.mp4", Kind: backend.ExportProjectGyro})
	want := []string{"c.mp4", "--export-project", "2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestProbeMetadataArgv(t *testing.T) {
	got := probeMetadataArgv("c.mp4", 3, "/tmp/cam.json", "")
	want := []string{"c.mp4", "--export-metadata", "3:/tmp/cam.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestProbeMetadataArgvWithFields(t *testing.T) {
	got := probeMetadataArgv("c.mp4", 1, "/tmp/m.json", `{"original":{"gyroscope":true}}`)
	want := []string{"c.mp4", "--export-metadata", "1:/tmp/m.json",
		"--export-metadata-fields", `{"original":{"gyroscope":true}}`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestStabilizeArgvInlinePresetAndDevices(t *testing.T) {
	got := stabilizeArgv(backend.StabilizeRequest{
		Inputs:           []string{"clip.mp4"},
		PresetJSON:       `{"version":3}`,
		ProcessingDevice: ip2(1),
		RenderingDevice:  "apple m",
		NoGPUDecoding:    true,
		Suffix:           "_v11",
	})
	j := strings.Join(got, " ")
	for _, want := range []string{
		`clip.mp4 --stdout-progress`,
		`--preset {"version":3}`,
		`-b 1`, `-r apple m`, `--no-gpu-decoding`, `-t _v11`,
	} {
		if !strings.Contains(j, want) {
			t.Fatalf("argv %q missing %q", j, want)
		}
	}
}

func TestExportSTMapArgv(t *testing.T) {
	got := exportSTMapArgv(backend.STMapRequest{Input: "c.mp4", Type: 2, OutFolder: "/tmp/st"})
	want := []string{"c.mp4", "--export-stmap", "2:/tmp/st"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func ip2(v int) *int { return &v }
