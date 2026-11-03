// Package backend abstracts Gyroflow execution so the v1 CLI backend
// and a future core backend share one interface.
package backend

import "context"

// OutParams mirrors the gyroflow `-p` JSON (exact schema verified in
// internal/schema; kept as a typed map here for backend independence).
type OutParams map[string]any

// SyncParams mirrors the gyroflow `-s` JSON.
type SyncParams map[string]any

// StabilizeRequest carries all parameters for a gyroflow stabilization run.
type StabilizeRequest struct {
	Inputs           []string        // video and/or .gyroflow project files
	PresetPath       string          // optional --preset <path>
	PresetJSON       string          // inline --preset JSON (built from Config by the backend)
	GyroFile         string          // optional -g
	OutParams        OutParams       // optional -p
	SyncParams       SyncParams      // optional -s
	Suffix           string          // optional -t
	Overwrite        bool            // -f
	ProcessingDevice *int            // -b (gyroflow processing/compute device index)
	RenderingDevice  string          // -r (e.g. "nvidia","intel","amd","apple m")
	NoGPUDecoding    bool            // --no-gpu-decoding
	Config           *GyroflowConfig // optional full typed config (serialized to inline --preset, -p, -s)
	LUT              string          // optional validated .cube path, applied with ffmpeg after the render
}

// ExportKind selects which gyroflow project-export variant to perform.
type ExportKind int

const (
	ExportProjectDefault ExportKind = 1
	ExportProjectGyro    ExportKind = 2
	ExportProjectProc    ExportKind = 3
	ExportProjectVideo   ExportKind = 4
)

// ExportRequest carries parameters for a gyroflow project export operation.
type ExportRequest struct {
	Input string
	Kind  ExportKind
	Out   string
}

// ProgressFunc receives parsed progress updates during a render.
type ProgressFunc func(Progress)

// Progress holds a single progress sample emitted by gyroflow --stdout-progress.
//
// Percent, Frame and Total cover the whole job, across all of its files.
// Frame and Total are gyroflow's own counts of container packets, not of output
// frames: a clip with pre-roll packets reports more than it renders (seen:
// 1828/1828 reported, 1799 frames in both the source's display and the output).
type Progress struct {
	Percent float64
	Frame   int
	Total   int
	Stage   string
	// Files holds one entry per input, in render order. Empty until gyroflow
	// reports its queue.
	Files []FileProgress
}

// FileProgress is the progress of one input of a render job.
type FileProgress struct {
	Name    string
	Percent float64
	Frame   int
	Total   int
	State   FileState
}

// FileState describes where one input of a render job is in the queue.
type FileState string

const (
	FilePending   FileState = "pending"
	FileRendering FileState = "rendering"
	FileDone      FileState = "done"
)

// Result is the terminal outcome of a backend operation.
type Result struct {
	OutputPaths []string
	ExitCode    int
	StderrTail  string
}

// CameraInfo is the camera identification extracted from gyroflow parsed
// metadata (--export-metadata type 2, .camera_identifier).
type CameraInfo struct {
	Make       string `json:"make"`
	Model      string `json:"model"`
	Identifier string `json:"identifier"`
}

// MetadataSummary is the compact, structured view of a clip's gyroflow
// metadata. Every field maps to an exact real source pinned against
// `--export-metadata 2` (parsed metadata):
//
//	Make/Model/Identifier .camera_identifier.{brand,model,identifier}
//	DetectedSource        .detected_source
//	Lens                  .camera_identifier.lens_model, else .lens_info
//	HasGyro               derived: .raw_imu non-empty AND elements carry gyro
//	Width/Height          .camera_identifier.{video_width,video_height}
//	FPS                   .camera_identifier.fps / 1000.0 (stored milli-fps)
//	FrameCount            len(.quaternions) - telemetry-derived proxy; on the
//	                      real sample == the render Queue line "Frames:" value
//	DurationS             .raw_imu[-1].timestamp_ms / 1000.0 (telemetry-derived)
//	DetectedProfile       .lens_profile (null on the pinned real sample → "")
//
// .frame_rate (top-level) is intentionally NOT used: it was null in the
// pinned real export. --export-metadata yields no direct frame_count or
// duration field; the telemetry proxies above are the authoritative
// metadata-only values and are documented as derived.
type MetadataSummary struct {
	Camera          CameraInfo `json:"camera"`
	DetectedSource  string     `json:"detected_source"`
	Lens            string     `json:"lens"`
	HasGyro         bool       `json:"has_gyro"`
	Width           int        `json:"width"`
	Height          int        `json:"height"`
	FPS             float64    `json:"fps"`
	DurationS       float64    `json:"duration_s"`
	FrameCount      int        `json:"frame_count"`
	DetectedProfile string     `json:"detected_profile"`
}

// MetadataResult is what ProbeMetadata returns: a small structured Summary
// plus the on-disk path to the FULL raw exported JSON. The multi-MB raw
// export is NEVER carried in this struct or inlined into an MCP response -
// callers surface RawPath so a client can fetch the blob deliberately.
type MetadataResult struct {
	Summary MetadataSummary `json:"summary"`
	RawPath string          `json:"raw_path"`
}

// GyroflowConfig is the backend-independent, ergonomic typed view of Gyroflow's
// full persisted .gyroflow v3 configuration. It is the MCP-facing shape; the
// CLI backend (internal/backend/cli/preset.go) maps it to the exact .gyroflow
// JSON. Pointer fields distinguish "unset" from a zero value so only
// user-specified parameters are emitted. Every field is verified against
// gyroflow/gyroflow@v1.6.3.
type GyroflowConfig struct {
	Stabilization  *StabilizationConfig  `json:"stabilization,omitempty"`
	Smoothing      *SmoothingConfig      `json:"smoothing,omitempty"`
	HorizonLock    *HorizonLockConfig    `json:"horizon_lock,omitempty"`
	Zoom           *ZoomConfig           `json:"zoom,omitempty"`
	RollingShutter *RollingShutterConfig `json:"rolling_shutter,omitempty"`
	Trim           *TrimConfig           `json:"trim,omitempty"`
	Speed          *SpeedConfig          `json:"speed,omitempty"`
	Lens           *LensConfig           `json:"lens,omitempty"`
	Rotation       *RotationConfig       `json:"rotation,omitempty"`
	Background     *BackgroundConfig     `json:"background,omitempty"`
	Keyframes      []KeyframeConfig      `json:"keyframes,omitempty"`
	Output         *OutputConfig         `json:"output,omitempty"`
	Sync           *SyncConfig           `json:"sync,omitempty"`
	// RawOverrides is an advanced escape hatch: a JSON object merged LAST over
	// the generated .gyroflow preset, for Gyroflow fields not yet typed here.
	RawOverrides map[string]any `json:"raw_overrides,omitempty"`
}

// StabilizationConfig → .gyroflow stabilization{} (non-smoothing scalars).
type StabilizationConfig struct {
	FOV                   *float64    `json:"fov,omitempty"`                    // >0, default 1.0
	LensCorrectionAmount  *float64    `json:"lens_correction_amount,omitempty"` // 0..1
	AdditionalRotation    *[3]float64 `json:"additional_rotation,omitempty"`    // yaw,pitch,roll deg
	AdditionalTranslation *[3]float64 `json:"additional_translation,omitempty"` // px
	FrameOffset           *int        `json:"frame_offset,omitempty"`
}

// SmoothingConfig → stabilization.method + stabilization.smoothing_params[].
// Method-specific params; validateConfig rejects params not valid for Method.
type SmoothingConfig struct {
	Method          string   `json:"method"`                     // Default | Plain 3D | Fixed camera | No smoothing
	Smoothness      *float64 `json:"smoothness,omitempty"`       // Default, 0.001..1.0
	PerAxis         *bool    `json:"per_axis,omitempty"`         // Default
	SmoothnessPitch *float64 `json:"smoothness_pitch,omitempty"` // Default, 0.001..1.0
	SmoothnessYaw   *float64 `json:"smoothness_yaw,omitempty"`   // Default, 0.001..1.0
	SmoothnessRoll  *float64 `json:"smoothness_roll,omitempty"`  // Default, 0.001..1.0
	MaxSmoothness   *float64 `json:"max_smoothness,omitempty"`   // Default, default 1.0
	Alpha01s        *float64 `json:"alpha_0_1s,omitempty"`       // Default, default 0.1
	TrimRangeOnly   *bool    `json:"trim_range_only,omitempty"`  // Default & Plain 3D
	TimeConstant    *float64 `json:"time_constant,omitempty"`    // Plain 3D, 0.01..10.0 s
	Roll            *float64 `json:"roll,omitempty"`             // Fixed camera, -180..180
	Pitch           *float64 `json:"pitch,omitempty"`            // Fixed camera, -90..90
	Yaw             *float64 `json:"yaw,omitempty"`              // Fixed camera, -180..180
}

// HorizonLockConfig → stabilization.horizon_lock{}.
type HorizonLockConfig struct {
	Amount            *float64 `json:"amount,omitempty"` // 0..100
	Roll              *float64 `json:"roll,omitempty"`   // deg
	UseGravityVectors *bool    `json:"use_gravity_vectors,omitempty"`
	IntegrationMethod *int     `json:"integration_method,omitempty"`
}

// ZoomConfig → stabilization adaptive zoom fields.
type ZoomConfig struct {
	AdaptiveZoomWindow *float64    `json:"adaptive_zoom_window,omitempty"` // -1 static / 0 off / >0 sec
	MaxZoom            *float64    `json:"max_zoom,omitempty"`             // % , active >50, default 130
	MaxZoomIterations  *int        `json:"max_zoom_iterations,omitempty"`  // default 5
	CenterOffset       *[2]float64 `json:"center_offset,omitempty"`
	Method             *int        `json:"method,omitempty"` // adaptive_zoom_method, default 1
}

// RollingShutterConfig → stabilization rolling shutter fields.
type RollingShutterConfig struct {
	FrameReadoutTime      *float64 `json:"frame_readout_time,omitempty"`      // ms
	FrameReadoutDirection *int     `json:"frame_readout_direction,omitempty"` // 0 TtB,1 BtT,2 LtR,3 RtL
}

// TrimConfig → trim ranges.
type TrimConfig struct {
	RangesMS [][2]float64 `json:"ranges_ms,omitempty"` // [[start_ms,end_ms],...]
}

// SpeedConfig → video speed fields.
type SpeedConfig struct {
	VideoSpeed          *float64 `json:"video_speed,omitempty"` // 1.0 = normal
	AffectsSmoothing    *bool    `json:"affects_smoothing,omitempty"`
	AffectsZooming      *bool    `json:"affects_zooming,omitempty"`
	AffectsZoomingLimit *bool    `json:"affects_zooming_limit,omitempty"`
}

// LensConfig → lens / digital lens fields.
type LensConfig struct {
	DigitalLens string `json:"digital_lens,omitempty"` // "" | gopro_superview | gopro_hyperview
}

// RotationConfig → video rotation.
type RotationConfig struct {
	VideoRotation *float64 `json:"video_rotation,omitempty"` // deg → video_info.rotation
}

// BackgroundConfig → background fill fields.
type BackgroundConfig struct {
	Mode          *int        `json:"mode,omitempty"` // 0 Solid,1 Repeat,2 Mirror,3 MarginFeather
	Margin        *float64    `json:"margin,omitempty"`
	MarginFeather *float64    `json:"margin_feather,omitempty"`
	Color         *[4]float64 `json:"color,omitempty"` // RGBA
}

// KeyframeConfig is one keyframe. Param ∈ verified KeyframeType set;
// Easing ∈ {NoEasing,EaseIn,EaseOut,EaseInOut}. Serializer converts
// TimestampMS → µs (i64) and groups by Param.
type KeyframeConfig struct {
	Param       string  `json:"param"`
	TimestampMS float64 `json:"timestamp_ms"`
	Value       float64 `json:"value"`
	Easing      string  `json:"easing,omitempty"` // default NoEasing
}

// OutputConfig → -p / output{}.
type OutputConfig struct {
	Codec                 *string  `json:"codec,omitempty"`
	CodecOptions          *string  `json:"codec_options,omitempty"`
	Bitrate               *float64 `json:"bitrate,omitempty"` // Mbps
	UseGPU                *bool    `json:"use_gpu,omitempty"`
	Audio                 *bool    `json:"audio,omitempty"`
	PixelFormat           *string  `json:"pixel_format,omitempty"`
	OutputWidth           *int     `json:"output_width,omitempty"`
	OutputHeight          *int     `json:"output_height,omitempty"`
	AspectRatio           *string  `json:"aspect_ratio,omitempty"` // "W:H"; render_start turns it into output_width/height per source size
	AudioCodec            *string  `json:"audio_codec,omitempty"`
	Interpolation         *string  `json:"interpolation,omitempty"`
	KeyframeDistance      *int     `json:"keyframe_distance,omitempty"`
	PadWithBlack          *bool    `json:"pad_with_black,omitempty"`
	PreserveOtherTracks   *bool    `json:"preserve_other_tracks,omitempty"`
	ExportTrimsSeparately *bool    `json:"export_trims_separately,omitempty"`
	EncoderOptions        *string  `json:"encoder_options,omitempty"`
	MetadataComment       *string  `json:"metadata_comment,omitempty"` // → metadata.comment
}

// SyncConfig → -s / synchronization{}.
type SyncConfig struct {
	InitialOffset        *float64 `json:"initial_offset,omitempty"`
	InitialOffsetInv     *bool    `json:"initial_offset_inv,omitempty"`
	SearchSize           *float64 `json:"search_size,omitempty"`
	CalcInitialFast      *bool    `json:"calc_initial_fast,omitempty"`
	MaxSyncPoints        *int     `json:"max_sync_points,omitempty"`
	EveryNthFrame        *int     `json:"every_nth_frame,omitempty"`
	TimePerSyncpoint     *float64 `json:"time_per_syncpoint,omitempty"`
	OfMethod             *int     `json:"of_method,omitempty"`
	OffsetMethod         *int     `json:"offset_method,omitempty"`
	PoseMethod           *int     `json:"pose_method,omitempty"`
	AutoSyncPoints       *bool    `json:"auto_sync_points,omitempty"`
	ProcessingResolution *int     `json:"processing_resolution,omitempty"` // CLI-accepted; UNVERIFIED at core struct
}

// ProbeOptions tunes ProbeMetadata. Zero value == kind 2 (parsed), no field
// filter - byte-identical to v1 behavior.
type ProbeOptions struct {
	Kind   int    // 0/2 parsed (default), 1 full, 3 camera
	Fields string // optional --export-metadata-fields JSON
}

// STMapRequest carries parameters for --export-stmap (Type 1 single frame,
// 2 all frames; verified gyroflow 1.6.3 --help).
type STMapRequest struct {
	Input     string
	Type      int
	OutFolder string
}

// Backend is the single seam between the job engine and Gyroflow.
type Backend interface {
	Stabilize(ctx context.Context, req StabilizeRequest, onProgress ProgressFunc) (*Result, error)
	ExportProject(ctx context.Context, req ExportRequest) (*Result, error)
	// ProbeMetadata runs `--export-metadata` on input with the given opts
	// (zero opts == kind 2 parsed, no field filter, byte-identical to v1).
	// It writes the full raw JSON to a stable path and returns a compact
	// MetadataSummary plus that path. It never returns the raw blob.
	ProbeMetadata(ctx context.Context, input string, opts ProbeOptions) (*MetadataResult, error)
	ExportSTMap(ctx context.Context, req STMapRequest) (*Result, error)
}
