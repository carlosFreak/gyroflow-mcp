package lut

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
)

// cube returns a valid identity LUT of size 2.
func cube(title string) string {
	return "TITLE \"" + title + "\"\nLUT_3D_SIZE 2\n# comment\n0 0 0\n1 0 0\n0 1 0\n1 1 0\n0 0 1\n1 0 1\n0 1 1\n1 1 1\n"
}

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestValidate(t *testing.T) {
	dir := t.TempDir()
	if err := Validate(write(t, dir, "ok.cube", cube("ok"))); err != nil {
		t.Fatalf("valid LUT rejected: %v", err)
	}
	for name, body := range map[string]string{
		"short.cube":  "LUT_3D_SIZE 2\n0 0 0\n",
		"1d.cube":     "LUT_1D_SIZE 2\n0 0 0\n1 1 1\n",
		"nosize.cube": "0 0 0\n",
		"junk.cube":   "LUT_3D_SIZE 2\nhello world !\n",
		"lut.txt":     cube("txt"),
	} {
		if err := Validate(write(t, dir, name, body)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestAddListResolveSuggest(t *testing.T) {
	src := t.TempDir()
	lib := &Library{Dir: filepath.Join(t.TempDir(), "LUTs")} // does not exist yet

	if e, err := lib.List(); err != nil || len(e) != 0 {
		t.Fatalf("missing folder should be an empty library: %v %v", e, err)
	}

	mine := write(t, src, "My Look.cube", cube("mine"))
	e, err := lib.Add(mine, "Sony A7S3")
	if err != nil {
		t.Fatal(err)
	}
	if e.Name != "My Look" || len(e.Cameras) != 1 {
		t.Fatalf("added entry = %+v", e)
	}
	// Adding the same file again only records another camera.
	if _, err := lib.Add(mine, "Sony FX3"); err != nil {
		t.Fatal(err)
	}
	// A different file with the same name is refused.
	if _, err := lib.Add(write(t, t.TempDir(), "My Look.cube", cube("other")), ""); err == nil {
		t.Fatal("expected a name clash error")
	}
	write(t, lib.Dir, "DJI O4 D-Log M to Rec.709.cube", cube("dji"))

	entries, err := lib.List()
	if err != nil || len(entries) != 2 {
		t.Fatalf("List = %+v, %v", entries, err)
	}

	if s := Suggest(entries, "Sony", "FX3"); s == nil || s.Name != "My Look" {
		t.Fatalf("tagged camera not suggested: %+v", s)
	}
	if s := Suggest(entries, "DJI", "O4P"); s == nil || !strings.HasPrefix(s.Name, "DJI O4") {
		t.Fatalf("file-name alias not suggested: %+v", s)
	}
	if s := Suggest(entries, "GoPro", "HERO12"); s != nil {
		t.Fatalf("unexpected suggestion %+v", s)
	}

	for _, v := range []string{"My Look", "my look.cube", mine} {
		if p, err := lib.Resolve(v); err != nil || p == "" {
			t.Errorf("Resolve(%q) = %q, %v", v, p, err)
		}
	}
	if _, err := lib.Resolve("missing"); err == nil {
		t.Error("expected an error for an unknown name")
	}
}

func TestDownloadURL(t *testing.T) {
	if u := DownloadURL("DJI", "O4P"); !strings.Contains(u, "o4-air-unit") {
		t.Fatalf("O4P = %q", u)
	}
	if u := DownloadURL("DJI", "Mini 4 Pro"); u != "https://www.dji.com/es/lut" {
		t.Fatalf("other DJI = %q", u)
	}
	if u := DownloadURL("GoPro", "HERO12"); u != "" {
		t.Fatalf("non-DJI = %q", u)
	}
}

func TestLooks(t *testing.T) {
	lib := &Library{Dir: filepath.Join(t.TempDir(), "LUTs")}
	if len(lib.Looks()) != 0 {
		t.Fatal("expected no looks")
	}
	if err := lib.SaveLook("Dron soleado", backend.ColorAdjust{Saturation: 0.2, Warmth: 0.3}); err != nil {
		t.Fatal(err)
	}
	if err := lib.SaveLook("dron SOLEADO", backend.ColorAdjust{Saturation: 0.1}); err != nil {
		t.Fatal(err)
	}
	if err := lib.SaveLook("bad", backend.ColorAdjust{Exposure: 9}); err == nil {
		t.Fatal("out-of-range look saved")
	}
	c, err := lib.Look("Dron Soleado")
	if err != nil || c.Saturation != 0.1 || c.Warmth != 0 || len(lib.Looks()) != 1 {
		t.Fatalf("look = %+v, %v, looks %v", c, err, lib.Looks())
	}
}
