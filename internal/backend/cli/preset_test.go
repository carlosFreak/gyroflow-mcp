package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
)

func f(v float64) *float64 { return &v }

func TestValidateConfigOK(t *testing.T) {
	c := &backend.GyroflowConfig{
		Smoothing:   &backend.SmoothingConfig{Method: "Default", Smoothness: f(0.7)},
		HorizonLock: &backend.HorizonLockConfig{Amount: f(100)},
		Zoom:        &backend.ZoomConfig{AdaptiveZoomWindow: f(-1)},
		Keyframes:   []backend.KeyframeConfig{{Param: "Fov", TimestampMS: 1200, Value: 1.5, Easing: "EaseIn"}},
	}
	if err := validateConfig(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateConfigRejections(t *testing.T) {
	cases := []struct {
		name string
		c    *backend.GyroflowConfig
		want string
	}{
		{"smoothness range", &backend.GyroflowConfig{Smoothing: &backend.SmoothingConfig{Method: "Default", Smoothness: f(2)}}, "smoothness"},
		{"unknown method", &backend.GyroflowConfig{Smoothing: &backend.SmoothingConfig{Method: "Bogus"}}, "method"},
		{"empty method", &backend.GyroflowConfig{Smoothing: &backend.SmoothingConfig{Method: ""}}, "method"},
		{"param/method mismatch", &backend.GyroflowConfig{Smoothing: &backend.SmoothingConfig{Method: "No smoothing", Smoothness: f(0.5)}}, "not valid for method"},
		{"horizon range", &backend.GyroflowConfig{HorizonLock: &backend.HorizonLockConfig{Amount: f(150)}}, "amount"},
		{"readout dir enum", &backend.GyroflowConfig{RollingShutter: &backend.RollingShutterConfig{FrameReadoutDirection: ip(9)}}, "frame_readout_direction"},
		{"keyframe param", &backend.GyroflowConfig{Keyframes: []backend.KeyframeConfig{{Param: "Nope"}}}, "keyframe param"},
		{"keyframe easing", &backend.GyroflowConfig{Keyframes: []backend.KeyframeConfig{{Param: "Fov", Easing: "Bouncy"}}}, "easing"},
		{"fov non-positive", &backend.GyroflowConfig{Stabilization: &backend.StabilizationConfig{FOV: f(0)}}, "fov"},
		{"digital_lens invalid", &backend.GyroflowConfig{Lens: &backend.LensConfig{DigitalLens: "gopro_max"}}, "digital_lens"},
		{"background mode range", &backend.GyroflowConfig{Background: &backend.BackgroundConfig{Mode: ip(9)}}, "background.mode"},
		{"keyframe negative ts", &backend.GyroflowConfig{Keyframes: []backend.KeyframeConfig{{Param: "Fov", TimestampMS: -5}}}, "timestamp_ms"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateConfig(tc.c)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func ip(v int) *int { return &v }

func TestBuildPresetJSONShape(t *testing.T) {
	c := &backend.GyroflowConfig{
		Stabilization: &backend.StabilizationConfig{FOV: f(1.2), LensCorrectionAmount: f(1)},
		Smoothing:     &backend.SmoothingConfig{Method: "Default", Smoothness: f(0.7), PerAxis: bp(true)},
		HorizonLock:   &backend.HorizonLockConfig{Amount: f(80), Roll: f(2)},
		Zoom:          &backend.ZoomConfig{AdaptiveZoomWindow: f(-1)},
		Trim:          &backend.TrimConfig{RangesMS: [][2]float64{{0, 3000}}},
		Lens:          &backend.LensConfig{DigitalLens: "gopro_superview"},
		Keyframes: []backend.KeyframeConfig{
			{Param: "Fov", TimestampMS: 1200, Value: 1.5, Easing: "EaseIn"},
		},
		RawOverrides: map[string]any{"light_refraction_coefficient": 1.33},
	}
	b, err := buildPresetJSON(c)
	if err != nil {
		t.Fatalf("buildPresetJSON: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("result not valid JSON: %v", err)
	}
	if m["version"] != float64(3) || m["title"] != "Gyroflow data file" {
		t.Fatalf("missing version/title: %v", m["version"])
	}
	st := m["stabilization"].(map[string]any)
	if st["fov"] != 1.2 {
		t.Fatalf("fov not mapped: %v", st["fov"])
	}
	if st["method"] != "Default" {
		t.Fatalf("method not mapped: %v", st["method"])
	}
	sp := st["smoothing_params"].([]any)
	got := map[string]float64{}
	for _, e := range sp {
		em := e.(map[string]any)
		got[em["name"].(string)] = em["value"].(float64)
	}
	if got["smoothness"] != 0.7 || got["per_axis"] != 1 {
		t.Fatalf("smoothing_params wrong: %v", got)
	}
	if st["horizon_lock_amount"] != float64(80) {
		t.Fatalf("horizon not mapped: %v", st["horizon_lock_amount"])
	}
	tr := m["trim_ranges_ms"].([]any)[0].([]any)
	if tr[0] != float64(0) || tr[1] != float64(3000) {
		t.Fatalf("trim not mapped: %v", tr)
	}
	cd := m["calibration_data"].(map[string]any)
	if cd["digital_lens"] != "gopro_superview" {
		t.Fatalf("digital_lens not mapped: %v", cd["digital_lens"])
	}
	kf := m["keyframes"].(map[string]any)["Fov"].(map[string]any)
	v, ok := kf["1200000"] // 1200 ms → 1_200_000 µs
	if !ok {
		t.Fatalf("keyframe µs key missing: %v", kf)
	}
	if v.(map[string]any)["value"] != 1.5 || v.(map[string]any)["easing"] != "EaseIn" {
		t.Fatalf("keyframe payload wrong: %v", v)
	}
	if m["light_refraction_coefficient"] != 1.33 {
		t.Fatalf("raw_override not merged: %v", m["light_refraction_coefficient"])
	}
}

func bp(b bool) *bool { return &b }

func TestBuildPresetJSONHorizonRollPaired(t *testing.T) {
	t.Run("amount-only emits roll=0", func(t *testing.T) {
		c := &backend.GyroflowConfig{
			HorizonLock: &backend.HorizonLockConfig{Amount: f(100)},
		}
		b, err := buildPresetJSON(c)
		if err != nil {
			t.Fatalf("buildPresetJSON: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		st := m["stabilization"].(map[string]any)
		if st["horizon_lock_amount"] != float64(100) {
			t.Fatalf("horizon_lock_amount: want 100, got %v", st["horizon_lock_amount"])
		}
		roll, ok := st["horizon_lock_roll"]
		if !ok {
			t.Fatal("horizon_lock_roll absent - gyroflow silently ignores horizon lock without it")
		}
		if roll != float64(0) {
			t.Fatalf("horizon_lock_roll: want 0, got %v", roll)
		}
	})

	t.Run("amount+explicit roll emits given roll", func(t *testing.T) {
		c := &backend.GyroflowConfig{
			HorizonLock: &backend.HorizonLockConfig{Amount: f(80), Roll: f(3)},
		}
		b, err := buildPresetJSON(c)
		if err != nil {
			t.Fatalf("buildPresetJSON: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		st := m["stabilization"].(map[string]any)
		if st["horizon_lock_amount"] != float64(80) {
			t.Fatalf("horizon_lock_amount: want 80, got %v", st["horizon_lock_amount"])
		}
		if st["horizon_lock_roll"] != float64(3) {
			t.Fatalf("horizon_lock_roll: want 3, got %v", st["horizon_lock_roll"])
		}
	})
}

func TestBuildPresetJSONDefaults(t *testing.T) {
	t.Run("boolToNum false produces 0", func(t *testing.T) {
		c := &backend.GyroflowConfig{
			Smoothing: &backend.SmoothingConfig{Method: "Default", TrimRangeOnly: bp(false)},
		}
		b, err := buildPresetJSON(c)
		if err != nil {
			t.Fatalf("buildPresetJSON: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		sp := m["stabilization"].(map[string]any)["smoothing_params"].([]any)
		got := map[string]float64{}
		for _, e := range sp {
			em := e.(map[string]any)
			got[em["name"].(string)] = em["value"].(float64)
		}
		if got["trim_range_only"] != 0 {
			t.Fatalf("expected trim_range_only=0, got %v", got["trim_range_only"])
		}
	})

	t.Run("empty easing defaults to NoEasing", func(t *testing.T) {
		c := &backend.GyroflowConfig{
			Keyframes: []backend.KeyframeConfig{
				{Param: "Fov", TimestampMS: 500, Value: 1.0, Easing: ""},
			},
		}
		b, err := buildPresetJSON(c)
		if err != nil {
			t.Fatalf("buildPresetJSON: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		kf := m["keyframes"].(map[string]any)["Fov"].(map[string]any)
		entry := kf["500000"].(map[string]any)
		if entry["easing"] != "NoEasing" {
			t.Fatalf("expected easing=NoEasing, got %v", entry["easing"])
		}
	})

	t.Run("RawOverrides object merges into stabilization (not replaces)", func(t *testing.T) {
		c := &backend.GyroflowConfig{
			Smoothing:    &backend.SmoothingConfig{Method: "Default"},
			RawOverrides: map[string]any{"stabilization": map[string]any{"extra": float64(1)}},
		}
		b, err := buildPresetJSON(c)
		if err != nil {
			t.Fatalf("buildPresetJSON: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		st := m["stabilization"].(map[string]any)
		if st["method"] != "Default" {
			t.Fatalf("stabilization.method lost after deepMerge: %v", st["method"])
		}
		if st["extra"] != float64(1) {
			t.Fatalf("stabilization.extra not merged in: %v", st["extra"])
		}
	})
}
