package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
)

// parsedMetadata mirrors only the fields of gyroflow `--export-metadata 2`
// (parsed metadata) that the compact summary needs. The exact JSON paths and
// real sample values were pinned against the committed fixture
// testdata/metadata_sample.json. Unused bulk arrays (image orientations,
// gravity vectors, the full raw_imu/quaternions payloads) are deliberately
// not retained - json.Unmarshal decodes raw_imu/quaternions transiently,
// but only counts and the last IMU timestamp are kept.
type parsedMetadata struct {
	DetectedSource   string `json:"detected_source"`
	CameraIdentifier struct {
		Brand       string `json:"brand"`
		Model       string `json:"model"`
		LensModel   string `json:"lens_model"`
		LensInfo    string `json:"lens_info"`
		FPS         int    `json:"fps"` // milli-fps: 59940 => 59.940
		VideoWidth  int    `json:"video_width"`
		VideoHeight int    `json:"video_height"`
		Identifier  string `json:"identifier"`
	} `json:"camera_identifier"`
	RawIMU []struct {
		TimestampMS float64   `json:"timestamp_ms"`
		Gyro        []float64 `json:"gyro"`
	} `json:"raw_imu"`
	// Keyed by timestamp in microseconds.
	Quaternions map[string]json.RawMessage `json:"quaternions"`
	// LensProfile was null in the pinned real export. Decoded as RawMessage so
	// a JSON-string value (if a future real sample carries one) can be surfaced
	// without assuming an object shape that has not been pinned to reality.
	LensProfile json.RawMessage `json:"lens_profile"`
}

// parseMetadataSummary maps a gyroflow `--export-metadata 2` JSON document to
// the compact backend.MetadataSummary using only the pinned field paths.
// frame_count and duration_s are telemetry-derived proxies (len(quaternions)
// and last raw_imu timestamp) - gyroflow's metadata export carries no direct
// frame-count or duration field; this is documented on backend.MetadataSummary.
func parseMetadataSummary(raw []byte) (backend.MetadataSummary, error) {
	var m parsedMetadata
	if err := json.Unmarshal(raw, &m); err != nil {
		return backend.MetadataSummary{}, fmt.Errorf("parsing gyroflow metadata: %w", err)
	}

	ci := m.CameraIdentifier
	lens := ci.LensModel
	if lens == "" {
		lens = ci.LensInfo
	}

	// has_gyro: derived, never assume a boolean field exists. True only when
	// the IMU stream is present AND its samples actually carry gyro readings.
	hasGyro := false
	for _, s := range m.RawIMU {
		if len(s.Gyro) > 0 {
			hasGyro = true
			break
		}
	}

	durationS := 0.0
	if n := len(m.RawIMU); n > 0 {
		durationS = m.RawIMU[n-1].TimestampMS / 1000.0
	}
	frameCount := len(m.Quaternions)

	// Cameras like the DJI O4 Pro embed orientation quaternions instead of raw
	// gyro samples: raw_imu is empty and quaternions run at IMU rate, not per frame.
	if !hasGyro && len(m.Quaternions) > 0 {
		hasGyro = true
		durationS = quaternionSpanS(m.Quaternions)
		frameCount = int(math.Round(durationS * float64(ci.FPS) / 1000.0))
	}

	return backend.MetadataSummary{
		Camera: backend.CameraInfo{
			Make:       ci.Brand,
			Model:      ci.Model,
			Identifier: ci.Identifier,
		},
		DetectedSource:  m.DetectedSource,
		Lens:            lens,
		HasGyro:         hasGyro,
		Width:           ci.VideoWidth,
		Height:          ci.VideoHeight,
		FPS:             float64(ci.FPS) / 1000.0,
		DurationS:       durationS,
		FrameCount:      frameCount,
		DetectedProfile: detectedProfile(m.LensProfile),
	}, nil
}

// quaternionSpanS returns the time covered by the quaternion timestamps.
func quaternionSpanS(q map[string]json.RawMessage) float64 {
	first, last := math.Inf(1), math.Inf(-1)
	for k := range q {
		if ts, err := strconv.ParseFloat(k, 64); err == nil {
			first, last = math.Min(first, ts), math.Max(last, ts)
		}
	}
	if first > last {
		return 0
	}
	return (last - first) / 1e6
}

// detectedProfile resolves .lens_profile to a string. The pinned real export
// confirmed only the null case (→ ""). A JSON-string value is unquoted; any
// other non-null shape is left "" rather than guessing an unpinned schema.
func detectedProfile(rm json.RawMessage) string {
	trimmed := bytes.TrimSpace(rm)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	var s string
	if err := json.Unmarshal(trimmed, &s); err == nil {
		return s
	}
	var profile struct {
		Brand string `json:"camera_brand"`
		Model string `json:"camera_model"`
	}
	if err := json.Unmarshal(trimmed, &profile); err == nil && profile.Brand+profile.Model != "" {
		return strings.TrimSpace(profile.Brand + " " + profile.Model)
	}
	// Present but neither null nor a JSON string (e.g. an object): unpinned
	// shape - emit "" but log so a degraded probe is visible (mirrors the
	// LoadFromDir skipped-count logging style in internal/lens).
	preview := trimmed
	if len(preview) > 80 {
		preview = preview[:80]
	}
	log.Printf("metadata: lens_profile present but not null/string (unpinned shape), emitting \"\": %s", preview)
	return ""
}
