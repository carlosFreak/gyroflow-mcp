package mcpsurface

// capabilitiesDoc returns the static Markdown reference for every render_start
// config field. It is served as the gyroflow://capabilities MCP resource so an
// LLM can translate natural-language requests into a correctly typed config
// without guessing. Every field, range, and enum is cross-checked against the
// corresponding validator (internal/backend/validate.go) and struct definitions
// (internal/backend/backend.go) - contradictions between this doc and the
// validator are treated as bugs.
//
// The string is a raw literal: no backticks may appear inside it.
func capabilitiesDoc() string {
	return `# Gyroflow render_start Config Reference

All fields verified against gyroflow/gyroflow@v1.6.3.
Pass as the "config" argument to render_start. Every sub-object is optional;
omit entire sections you do not need. Pointer fields distinguish "unset" from
zero, so only the fields you supply override the video defaults.

> Ranges and enum values in this document must match internal/backend/validate.go, which is the authoritative source of truth.

---

## smoothing

Controls the camera-path smoothing algorithm and its strength.

### smoothing.method (string, required when smoothing block is present)

Exact string values:

  "Default"        adaptive velocity-based; use smoothness and per_axis params
  "Plain 3D"       first-order low-pass filter; use time_constant
  "Fixed camera"   locks output to a fixed orientation; use roll/pitch/yaw
  "No smoothing"   pass-through, no path stabilization

### Default method parameters

  smoothness         float   0.001..1.0   global smoothing strength
                                          higher = smoother path + more crop
  per_axis           bool    -            enable per-axis smoothness overrides
  smoothness_pitch   float   0.001..1.0   pitch smoothness (requires per_axis)
  smoothness_yaw     float   0.001..1.0   yaw smoothness (requires per_axis)
  smoothness_roll    float   0.001..1.0   roll smoothness (requires per_axis)
  max_smoothness     float   -            cap on computed smoothness, default 1.0
  alpha_0_1s         float   -            velocity filter alpha at 0.1 s, default 0.1
  trim_range_only    bool    -            smooth only within trim ranges

### Plain 3D method parameters

  time_constant      float   0.01..10.0 s   low-pass time constant in seconds
  trim_range_only    bool    -              smooth only within trim ranges

### Fixed camera method parameters

  roll               float   -180..180 deg   fixed roll angle
  pitch              float   -90..90 deg     fixed pitch angle
  yaw                float   -180..180 deg   fixed yaw angle

---

## horizon_lock

Keeps the horizon level in the output frame.

  horizon_lock.amount            float   0..100   levelling strength; 0 = off, 100 = full level
  horizon_lock.roll              float   deg      tilt offset applied after levelling
  horizon_lock.use_gravity_vectors   bool   -     use gravity-vector integration
  horizon_lock.integration_method    int    -     gravity integration method index

Note: horizon_lock is active only when amount is greater than approximately 0.

---

## zoom

Controls digital zoom / cropping to eliminate stabilization borders.

  zoom.adaptive_zoom_window   float   -1 / 0 / >0 seconds

    -1   static crop: fixed zoom level, no animation (least distraction)
     0   no zoom: full field of view is preserved; black/repeated borders may appear
    >0   dynamic-zoom smoothing window in seconds; default 4.0
         the window controls how fast the crop adapts to motion

  To MINIMISE cropping use -1 (static crop) - or 0 for no zoom at all (full FOV, but shake shows as black borders). Positive values (default 4.0) apply dynamic zoom and crop MORE, not less.

  zoom.max_zoom               float   percent, active when >50, default 130
                                      maximum allowed zoom level percentage
  zoom.max_zoom_iterations    int     -         optimizer iterations, default 5
  zoom.center_offset          [2]float -         [x,y] fractional offset of zoom centre
  zoom.method                 int     -         adaptive_zoom_method index, default 1

---

## rolling_shutter

Corrects rolling-shutter distortion (CMOS jello effect).

  rolling_shutter.frame_readout_time         float   ms   full-frame readout duration
  rolling_shutter.frame_readout_direction    int     0..3

    0   TopToBottom    (most cameras)
    1   BottomToTop
    2   LeftToRight
    3   RightToLeft

---

## trim

Restricts rendering to one or more time windows.

  trim.ranges_ms   [[float, float], ...]   list of [start_ms, end_ms] pairs in milliseconds
                   e.g. [[0, 5000], [10000, 20000]] renders seconds 0-5 and 10-20

---

## speed

Video playback speed remapping.

  speed.video_speed              float   default 1.0   playback speed multiplier; 1.0 = normal
  speed.affects_smoothing        bool    default true   smoothing adapts to the new speed
  speed.affects_zooming          bool    default true   zoom adapts to the new speed
  speed.affects_zooming_limit    bool    default true   zoom limit adapts to the new speed

---

## lens

Digital lens profile selection.

  lens.digital_lens   string   "" | "gopro_superview" | "gopro_hyperview"

    ""                  no digital-lens correction (default)
    "gopro_superview"   un-distort GoPro SuperView digital lens
    "gopro_hyperview"   un-distort GoPro HyperView digital lens

---

## rotation

  rotation.video_rotation   float   deg   input video rotation offset in degrees

---

## stabilization (non-smoothing scalars)

  stabilization.fov                     float      >0      field-of-view multiplier, default 1.0
  stabilization.lens_correction_amount  float      0..1    lens-distortion correction strength
  stabilization.additional_rotation     [3]float   deg     [yaw, pitch, roll] offset rotation
  stabilization.additional_translation  [3]float   px      [x, y, z] translation offset
  stabilization.frame_offset            int        -       gyro-to-video frame offset

---

## background

Background fill when borders are not cropped away.

  background.mode           int       0..3   fill mode:
                                             0 Solid (use background.color)
                                             1 Repeat (edge-extend)
                                             2 Mirror
                                             3 MarginFeather
  background.margin         float     -      margin size
  background.margin_feather float     -      feather amount (mode 3)
  background.color          [4]float  0..1   RGBA solid fill colour (mode 0)

---

## keyframes

Animate any parameter over time. Each entry in the keyframes list:

  keyframes[].param          string   required   KeyframeType name (see list below)
  keyframes[].timestamp_ms   float    required   timestamp in MILLISECONDS
                                                 (converted to microseconds internally)
  keyframes[].value          float    required   parameter value at this keyframe
  keyframes[].easing         string   optional   easing function (default NoEasing)

### Valid KeyframeType values

Fov
VideoRotation
ZoomingSpeed
ZoomingCenterX
ZoomingCenterY
MaxZoom
AdditionalRotationX
AdditionalRotationY
AdditionalRotationZ
AdditionalTranslationX
AdditionalTranslationY
AdditionalTranslationZ
BackgroundMargin
BackgroundFeather
LockHorizonAmount
LockHorizonRoll
LensCorrectionStrength
LightRefractionCoeff
SmoothingParamTimeConstant
SmoothingParamTimeConstant2
SmoothingParamSmoothness
SmoothingParamPitch
SmoothingParamRoll
SmoothingParamYaw
VideoSpeed

### Valid easing values

  NoEasing   (default, linear)
  EaseIn
  EaseOut
  EaseInOut

---

## output (typed path, routes to gyroflow -p map)

config.output is the preferred typed path for output parameters.
All non-nil fields are routed by routeOutput into the -p (output params) map.

  output.codec                   string   e.g. "H.264/AVC"     output video codec
  output.codec_options           string   -                    codec option string
  output.bitrate                 float    Mbps                 target bitrate in megabits/second
  output.use_gpu                 bool     -                    GPU-accelerated encode
  output.audio                   bool     -                    include audio in output
  output.pixel_format            string   e.g. "yuv420p"       output pixel format
  output.output_width            int      px                   output frame width
  output.output_height           int      px                   output frame height
  output.aspect_ratio            string   e.g. "16:9", "1:1"   output aspect ratio; render_start sets output_width/height
                                                               to the largest frame of that ratio inside the source
                                                               (3840x2880 at 16:9 → 3840x2160). Exclusive with
                                                               output_width/output_height; inputs of one call must
                                                               share a resolution.
  output.audio_codec             string   e.g. "aac"           audio codec
  output.interpolation           string   e.g. "Lanczos4"      pixel interpolation algorithm
  output.keyframe_distance       int      frames               IDR keyframe interval
  output.pad_with_black          bool     -                    pad borders with black instead of crop
  output.preserve_other_tracks   bool     -                    copy non-video/audio streams
  output.export_trims_separately bool     -                    one file per trim range
  output.encoder_options         string   -                    raw encoder option string
  output.metadata_comment        string   -                    embed comment into output metadata

---

## sync (typed path, routes to gyroflow -s map)

config.sync is the preferred typed path for synchronization parameters.
All non-nil fields are routed by routeSync into the -s (sync params) map.

  sync.initial_offset          float   ms        initial gyro-to-video offset
  sync.initial_offset_inv      bool    -         invert the initial offset sign
  sync.search_size             float   ms        sync-search window size
  sync.calc_initial_fast       bool    -         fast initial offset estimation
  sync.max_sync_points         int     -         maximum optical-flow sync points
  sync.every_nth_frame         int     frames    run optical flow every N frames
  sync.time_per_syncpoint      float   s         seconds of video per sync point
  sync.of_method               int     -         optical-flow method index
  sync.offset_method           int     -         offset-computation method index
  sync.pose_method             int     -         pose-estimation method index
  sync.auto_sync_points        bool    -         auto-determine sync point count
  sync.processing_resolution   int     px        resolution height for OF processing

---

## Parameter routing and precedence

render_start provides two parallel paths for output and sync configuration:

### Typed path (preferred): config.output and config.sync

  Use these strongly-typed structs. prepareStabilize routes each non-nil field
  into the -p or -s map via routeOutput / routeSync. Typed keys OVERLAY any raw
  keys supplied in the same map (typed path wins on conflict).

### Raw escape hatch: out_params and sync_params

  Accepted as raw JSON maps passed directly to gyroflow -p and -s.
  Use only for keys not yet exposed in the typed structs. When both typed and
  raw are supplied for the same map key, the typed config value takes precedence
  because routeOutput / routeSync write into the map after the raw values are
  applied.

### raw_overrides (deep-merge, last wins)

  A JSON object deep-merged LAST over the generated .gyroflow preset (after
  buildPresetJSON and any preset_path base merge). Use for preset fields not yet
  surfaced in any typed struct. Overrides everything built from config.* fields.

### Precedence summary (later step wins)

  1. preset_path on-disk base (if supplied)
  2. buildPresetJSON output - derived from config.* typed fields
  3. config.raw_overrides deep-merge over the preset
  4. -p map: raw out_params keys first, then config.output keys overlay
  5. -s map: raw sync_params keys first, then config.sync keys overlay

---

All fields verified against gyroflow/gyroflow@v1.6.3.
`
}
