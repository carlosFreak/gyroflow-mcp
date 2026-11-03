package backend

import "fmt"

// smoothingMethods are the verified algorithm names (gyroflow@v1.6.3
// src/core/smoothing/{default_algo,plain,fixed,none}.rs get_name()).
var smoothingMethods = map[string]bool{
	"Default": true, "Plain 3D": true, "Fixed camera": true, "No smoothing": true,
}

// smoothingParamsByMethod MUST have an entry for every key in smoothingMethods (init() enforces this).
// smoothingParamsByMethod lists which SmoothingConfig params are valid per
// method (verified per-algorithm get_parameters_json).
var smoothingParamsByMethod = map[string]map[string]bool{
	"Default": {"smoothness": true, "per_axis": true, "smoothness_pitch": true,
		"smoothness_yaw": true, "smoothness_roll": true, "max_smoothness": true,
		"alpha_0_1s": true, "trim_range_only": true},
	"Plain 3D":     {"time_constant": true, "trim_range_only": true},
	"Fixed camera": {"roll": true, "pitch": true, "yaw": true},
	"No smoothing": {},
}

// keyframeTypes is the verified KeyframeType set (gyroflow@v1.6.3
// src/core/keyframes.rs L26-54 define_keyframes!).
var keyframeTypes = map[string]bool{
	"Fov": true, "VideoRotation": true, "ZoomingSpeed": true, "ZoomingCenterX": true,
	"ZoomingCenterY": true, "MaxZoom": true, "AdditionalRotationX": true,
	"AdditionalRotationY": true, "AdditionalRotationZ": true, "AdditionalTranslationX": true,
	"AdditionalTranslationY": true, "AdditionalTranslationZ": true, "BackgroundMargin": true,
	"BackgroundFeather": true, "LockHorizonAmount": true, "LockHorizonRoll": true,
	"LensCorrectionStrength": true, "LightRefractionCoeff": true,
	"SmoothingParamTimeConstant": true, "SmoothingParamTimeConstant2": true,
	"SmoothingParamSmoothness": true, "SmoothingParamPitch": true,
	"SmoothingParamRoll": true, "SmoothingParamYaw": true, "VideoSpeed": true,
}

var keyframeEasings = map[string]bool{
	"NoEasing": true, "EaseIn": true, "EaseOut": true, "EaseInOut": true, "": true,
}

func init() {
	for m := range smoothingMethods {
		if _, ok := smoothingParamsByMethod[m]; !ok {
			panic("smoothingParamsByMethod missing entry for method: " + m)
		}
	}
}

// ParseAspectRatio parses an output.aspect_ratio such as "16:9".
func ParseAspectRatio(s string) (w, h int, err error) {
	if n, scanErr := fmt.Sscanf(s, "%d:%d", &w, &h); scanErr != nil || n != 2 || w <= 0 || h <= 0 {
		return 0, 0, fmt.Errorf("output.aspect_ratio %q invalid; use W:H with positive integers, e.g. 16:9, 9:16, 1:1", s)
	}
	return w, h, nil
}

// FitAspectRatio returns the largest even-sized frame of aspect aw:ah that
// fits inside a srcW x srcH source, e.g. 3840x2880 at 16:9 → 3840x2160.
func FitAspectRatio(srcW, srcH, aw, ah int) (w, h int) {
	if srcW*ah > srcH*aw { // source is wider: keep the height
		h, w = srcH, srcH*aw/ah
	} else {
		w, h = srcW, srcW*ah/aw
	}
	return w &^ 1, h &^ 1
}

// rng checks that v (if non-nil) lies in [lo, hi], returning a named error on violation.
func rng(name string, v *float64, lo, hi float64) error {
	if v == nil {
		return nil
	}
	if *v < lo || *v > hi {
		return fmt.Errorf("%s = %v out of range [%v, %v]", name, *v, lo, hi)
	}
	return nil
}

// ValidateGyroflowConfig enforces every verified range/enum and method/param
// compatibility. Errors are phrased so an LLM can self-correct in one turn.
func ValidateGyroflowConfig(c *GyroflowConfig) error {
	if c == nil {
		return nil
	}
	if s := c.Smoothing; s != nil {
		if !smoothingMethods[s.Method] {
			return fmt.Errorf("smoothing.method %q invalid; valid: Default, Plain 3D, Fixed camera, No smoothing", s.Method)
		}
		valid := smoothingParamsByMethod[s.Method]
		set := map[string]bool{}
		if s.Smoothness != nil {
			set["smoothness"] = true
		}
		if s.PerAxis != nil {
			set["per_axis"] = true
		}
		if s.SmoothnessPitch != nil {
			set["smoothness_pitch"] = true
		}
		if s.SmoothnessYaw != nil {
			set["smoothness_yaw"] = true
		}
		if s.SmoothnessRoll != nil {
			set["smoothness_roll"] = true
		}
		if s.MaxSmoothness != nil {
			set["max_smoothness"] = true
		}
		if s.Alpha01s != nil {
			set["alpha_0_1s"] = true
		}
		if s.TrimRangeOnly != nil {
			set["trim_range_only"] = true
		}
		if s.TimeConstant != nil {
			set["time_constant"] = true
		}
		if s.Roll != nil {
			set["roll"] = true
		}
		if s.Pitch != nil {
			set["pitch"] = true
		}
		if s.Yaw != nil {
			set["yaw"] = true
		}
		for k := range set {
			if !valid[k] {
				return fmt.Errorf("smoothing param %q not valid for method %q", k, s.Method)
			}
		}
		for _, e := range []struct {
			n string
			v *float64
		}{{"smoothness", s.Smoothness}, {"smoothness_pitch", s.SmoothnessPitch},
			{"smoothness_yaw", s.SmoothnessYaw}, {"smoothness_roll", s.SmoothnessRoll}} {
			if err := rng("smoothing."+e.n, e.v, 0.001, 1.0); err != nil {
				return err
			}
		}
		if err := rng("smoothing.time_constant", s.TimeConstant, 0.01, 10.0); err != nil {
			return err
		}
		if err := rng("smoothing.roll", s.Roll, -180, 180); err != nil {
			return err
		}
		if err := rng("smoothing.pitch", s.Pitch, -90, 90); err != nil {
			return err
		}
		if err := rng("smoothing.yaw", s.Yaw, -180, 180); err != nil {
			return err
		}
	}
	if h := c.HorizonLock; h != nil {
		if err := rng("horizon_lock.amount", h.Amount, 0, 100); err != nil {
			return err
		}
	}
	if s := c.Stabilization; s != nil {
		if err := rng("stabilization.lens_correction_amount", s.LensCorrectionAmount, 0, 1); err != nil {
			return err
		}
		if s.FOV != nil && *s.FOV <= 0 {
			return fmt.Errorf("stabilization.fov = %v must be > 0", *s.FOV)
		}
	}
	if r := c.RollingShutter; r != nil && r.FrameReadoutDirection != nil {
		if d := *r.FrameReadoutDirection; d < 0 || d > 3 {
			return fmt.Errorf("rolling_shutter.frame_readout_direction = %d invalid; 0 TopToBottom,1 BottomToTop,2 LeftToRight,3 RightToLeft", d)
		}
	}
	if b := c.Background; b != nil && b.Mode != nil {
		if m := *b.Mode; m < 0 || m > 3 {
			return fmt.Errorf("background.mode = %d invalid; 0 Solid,1 Repeat,2 Mirror,3 MarginFeather", m)
		}
	}
	if l := c.Lens; l != nil && l.DigitalLens != "" &&
		l.DigitalLens != "gopro_superview" && l.DigitalLens != "gopro_hyperview" {
		return fmt.Errorf("lens.digital_lens %q invalid; valid: gopro_superview, gopro_hyperview (or empty)", l.DigitalLens)
	}
	if o := c.Output; o != nil && o.AspectRatio != nil {
		if _, _, err := ParseAspectRatio(*o.AspectRatio); err != nil {
			return err
		}
		if o.OutputWidth != nil || o.OutputHeight != nil {
			return fmt.Errorf("output.aspect_ratio and output.output_width/output_height are exclusive; set one or the other")
		}
	}
	for i, k := range c.Keyframes {
		if !keyframeTypes[k.Param] {
			return fmt.Errorf("keyframe param %q (index %d) invalid; see gyroflow://capabilities for the valid KeyframeType set", k.Param, i)
		}
		if !keyframeEasings[k.Easing] {
			return fmt.Errorf("keyframe easing %q (index %d) invalid; valid: NoEasing, EaseIn, EaseOut, EaseInOut", k.Easing, i)
		}
		if k.TimestampMS < 0 {
			return fmt.Errorf("keyframe timestamp_ms %v (index %d) must be >= 0", k.TimestampMS, i)
		}
	}
	return nil
}
