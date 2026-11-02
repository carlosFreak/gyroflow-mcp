// Package cli implements the v1 Backend by executing the gyroflow binary.
package cli

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
)

// stabilizeArgv builds the gyroflow argument vector. Flags are taken from
// the verified `gyroflow --help` usage block. JSON for -p/-s is
// marshaled from typed maps; schema types are defined in internal/schema but
// not yet enforced at argv construction.
func stabilizeArgv(r backend.StabilizeRequest) []string {
	a := append([]string{}, r.Inputs...)
	a = append(a, "--stdout-progress")
	switch {
	case r.PresetJSON != "":
		a = append(a, "--preset", r.PresetJSON)
	case r.PresetPath != "":
		a = append(a, "--preset", r.PresetPath)
	}
	if r.GyroFile != "" {
		a = append(a, "-g", r.GyroFile)
	}
	if r.Overwrite {
		a = append(a, "-f")
	}
	if r.Suffix != "" {
		a = append(a, "-t", r.Suffix)
	}
	if r.ProcessingDevice != nil {
		a = append(a, "-b", strconv.Itoa(*r.ProcessingDevice))
	}
	if r.RenderingDevice != "" {
		a = append(a, "-r", r.RenderingDevice)
	}
	if r.NoGPUDecoding {
		a = append(a, "--no-gpu-decoding")
	}
	if len(r.OutParams) > 0 {
		// Ignoring error: OutParams is map[string]any sourced from MCP JSON - always marshallable.
		b, _ := json.Marshal(r.OutParams)
		a = append(a, "-p", string(b))
	}
	if len(r.SyncParams) > 0 {
		// Ignoring error: SyncParams is map[string]any sourced from MCP JSON - always marshallable.
		b, _ := json.Marshal(r.SyncParams)
		a = append(a, "-s", string(b))
	}
	return a
}

// exportProjectArgv builds the gyroflow argument vector for project export.
// Flag verified against testdata/help_output.txt: --export-project <1-4>.
func exportProjectArgv(r backend.ExportRequest) []string {
	return []string{r.Input, "--export-project", strconv.Itoa(int(r.Kind))}
}

// probeMetadataArgv builds the gyroflow argument vector for metadata probing.
// Flags verified against testdata/help_output.txt:
//
//	--export-metadata <type>:<path>        (1=full, 2=parsed, 3=camera)
//	--export-metadata-fields <json>        optional field selector
func probeMetadataArgv(input string, kind int, outPath, fields string) []string {
	a := []string{input, "--export-metadata", fmt.Sprintf("%d:%s", kind, outPath)}
	if fields != "" {
		a = append(a, "--export-metadata-fields", fields)
	}
	return a
}

// exportSTMapArgv builds the gyroflow argument vector for ST map export.
// Flag verified against gyroflow 1.6.3 --help:
//
//	--export-stmap <type>:<folder>   (1=single frame, 2=all frames)
func exportSTMapArgv(r backend.STMapRequest) []string {
	return []string{r.Input, "--export-stmap", fmt.Sprintf("%d:%s", r.Type, r.OutFolder)}
}
