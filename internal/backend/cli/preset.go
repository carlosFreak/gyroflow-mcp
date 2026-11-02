package cli

import (
	"encoding/json"
	"math"
	"strconv"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
)

// validateConfig delegates to backend.ValidateGyroflowConfig - single source of truth.
func validateConfig(c *backend.GyroflowConfig) error {
	return backend.ValidateGyroflowConfig(c)
}

// boolToNum: Gyroflow smoothing params are numeric; bools serialize as 0/1
// (verified set_parameter uses value>0.1 for per_axis).
func boolToNum(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// buildPresetJSON serializes a GyroflowConfig into a .gyroflow v3 project
// object (gyroflow@v1.6.3 src/core/lib.rs L1166-1234). RawOverrides is deep-
// merged last. The result is passed to gyroflow as an inline --preset arg.
// Precondition: c must be non-nil (callers invoke it only after validateConfig confirms a non-nil config).
func buildPresetJSON(c *backend.GyroflowConfig) ([]byte, error) {
	root := map[string]any{
		"title":   "Gyroflow data file",
		"version": 3,
	}
	stab := map[string]any{}
	if s := c.Stabilization; s != nil {
		if s.FOV != nil {
			stab["fov"] = *s.FOV
		}
		if s.LensCorrectionAmount != nil {
			stab["lens_correction_amount"] = *s.LensCorrectionAmount
		}
		if s.AdditionalRotation != nil {
			stab["additional_rotation"] = s.AdditionalRotation
		}
		if s.AdditionalTranslation != nil {
			stab["additional_translation"] = s.AdditionalTranslation
		}
		if s.FrameOffset != nil {
			stab["frame_offset"] = *s.FrameOffset
		}
	}
	if s := c.Smoothing; s != nil {
		stab["method"] = s.Method
		var sp []map[string]any
		add := func(n string, v float64) { sp = append(sp, map[string]any{"name": n, "value": v}) }
		if s.Smoothness != nil {
			add("smoothness", *s.Smoothness)
		}
		if s.PerAxis != nil {
			add("per_axis", boolToNum(*s.PerAxis))
		}
		if s.SmoothnessPitch != nil {
			add("smoothness_pitch", *s.SmoothnessPitch)
		}
		if s.SmoothnessYaw != nil {
			add("smoothness_yaw", *s.SmoothnessYaw)
		}
		if s.SmoothnessRoll != nil {
			add("smoothness_roll", *s.SmoothnessRoll)
		}
		if s.MaxSmoothness != nil {
			add("max_smoothness", *s.MaxSmoothness)
		}
		if s.Alpha01s != nil {
			add("alpha_0_1s", *s.Alpha01s)
		}
		if s.TrimRangeOnly != nil {
			add("trim_range_only", boolToNum(*s.TrimRangeOnly))
		}
		if s.TimeConstant != nil {
			add("time_constant", *s.TimeConstant)
		}
		if s.Roll != nil {
			add("roll", *s.Roll)
		}
		if s.Pitch != nil {
			add("pitch", *s.Pitch)
		}
		if s.Yaw != nil {
			add("yaw", *s.Yaw)
		}
		if sp != nil {
			stab["smoothing_params"] = sp
		}
	}
	if h := c.HorizonLock; h != nil {
		if h.Amount != nil {
			stab["horizon_lock_amount"] = *h.Amount
			// gyroflow's importer (src/core/lib.rs nested if-let) applies horizon
			// lock only when horizon_lock_roll is ALSO present; always pair it.
			roll := 0.0
			if h.Roll != nil {
				roll = *h.Roll
			}
			stab["horizon_lock_roll"] = roll
		} else if h.Roll != nil {
			stab["horizon_lock_roll"] = *h.Roll
		}
		if h.UseGravityVectors != nil {
			stab["use_gravity_vectors"] = *h.UseGravityVectors
		}
		if h.IntegrationMethod != nil {
			stab["horizon_lock_integration_method"] = *h.IntegrationMethod
		}
	}
	if z := c.Zoom; z != nil {
		if z.AdaptiveZoomWindow != nil {
			stab["adaptive_zoom_window"] = *z.AdaptiveZoomWindow
		}
		if z.MaxZoom != nil {
			stab["max_zoom"] = *z.MaxZoom
		}
		if z.MaxZoomIterations != nil {
			stab["max_zoom_iterations"] = *z.MaxZoomIterations
		}
		if z.CenterOffset != nil {
			stab["adaptive_zoom_center_offset"] = z.CenterOffset
		}
		if z.Method != nil {
			stab["adaptive_zoom_method"] = *z.Method
		}
	}
	if r := c.RollingShutter; r != nil {
		if r.FrameReadoutTime != nil {
			stab["frame_readout_time"] = *r.FrameReadoutTime
		}
		if r.FrameReadoutDirection != nil {
			stab["frame_readout_direction"] = *r.FrameReadoutDirection
		}
	}
	if s := c.Speed; s != nil {
		if s.VideoSpeed != nil {
			stab["video_speed"] = *s.VideoSpeed
		}
		if s.AffectsSmoothing != nil {
			stab["video_speed_affects_smoothing"] = *s.AffectsSmoothing
		}
		if s.AffectsZooming != nil {
			stab["video_speed_affects_zooming"] = *s.AffectsZooming
		}
		if s.AffectsZoomingLimit != nil {
			stab["video_speed_affects_zooming_limit"] = *s.AffectsZoomingLimit
		}
	}
	if len(stab) > 0 {
		root["stabilization"] = stab
	}
	if t := c.Trim; t != nil && len(t.RangesMS) > 0 {
		root["trim_ranges_ms"] = t.RangesMS
	}
	if r := c.Rotation; r != nil && r.VideoRotation != nil {
		root["video_info"] = map[string]any{"rotation": *r.VideoRotation}
	}
	if b := c.Background; b != nil {
		if b.Mode != nil {
			root["background_mode"] = *b.Mode
		}
		if b.Margin != nil {
			root["background_margin"] = *b.Margin
		}
		if b.MarginFeather != nil {
			root["background_margin_feather"] = *b.MarginFeather
		}
		if b.Color != nil {
			root["background"] = b.Color
		}
	}
	if l := c.Lens; l != nil && l.DigitalLens != "" {
		root["calibration_data"] = map[string]any{"digital_lens": l.DigitalLens}
	}
	if len(c.Keyframes) > 0 {
		kf := map[string]map[string]any{}
		for _, k := range c.Keyframes {
			us := strconv.FormatInt(int64(math.Round(k.TimestampMS*1000)), 10)
			easing := k.Easing
			if easing == "" {
				easing = "NoEasing"
			}
			if kf[k.Param] == nil {
				kf[k.Param] = map[string]any{}
			}
			kf[k.Param][us] = map[string]any{"id": 0, "value": k.Value, "easing": easing}
		}
		root["keyframes"] = kf
	}
	deepMerge(root, c.RawOverrides)
	return json.Marshal(root)
}

// deepMerge recursively merges src into dst (objects merge; scalars/arrays
// replace). Used for RawOverrides applied last.
func deepMerge(dst map[string]any, src map[string]any) {
	for k, v := range src {
		if sv, ok := v.(map[string]any); ok {
			if dv, ok := dst[k].(map[string]any); ok {
				deepMerge(dv, sv)
				continue
			}
		}
		// Non-object src values are aliased, not deep-copied (safe here: build is followed immediately by Marshal with no further mutation).
		dst[k] = v
	}
}
