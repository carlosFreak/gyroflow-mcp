// Package lut manages the user's folder of .cube color LUTs: listing them,
// validating new ones, adding the user's own and suggesting one per camera.
package lut

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// DirEnv overrides the LUT folder.
const DirEnv = "GYROFLOW_MCP_LUT_DIR"

// None is the render_start value for "render without a LUT".
const None = "none"

// camerasFile records which camera each added LUT is for, keyed by file name.
const camerasFile = "cameras.json"

// maxCubeBytes bounds a .cube read; a 65-point LUT is about 7 MB.
const maxCubeBytes = 64 << 20

// DefaultDir is $GYROFLOW_MCP_LUT_DIR, else ~/Movies/LUTs.
func DefaultDir() string {
	if d := os.Getenv(DirEnv); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "LUTs"
	}
	return filepath.Join(home, "Movies", "LUTs")
}

// Library is a folder of .cube files. The folder is created on first Add.
type Library struct {
	Dir string
}

// Entry is one LUT of the library.
type Entry struct {
	Name    string   `json:"name"`
	Path    string   `json:"path"`
	Cameras []string `json:"cameras,omitempty"`
}

// List returns the library's .cube files sorted by name. A missing folder is
// an empty library.
func (l *Library) List() ([]Entry, error) {
	files, err := os.ReadDir(l.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cams := l.cameras()
	var out []Entry
	for _, f := range files {
		if f.IsDir() || !strings.EqualFold(filepath.Ext(f.Name()), ".cube") {
			continue
		}
		out = append(out, Entry{
			Name:    strings.TrimSuffix(f.Name(), filepath.Ext(f.Name())),
			Path:    filepath.Join(l.Dir, f.Name()),
			Cameras: cams[f.Name()],
		})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// Resolve turns a render_start lut value into a validated .cube path: a path
// to a file, or the name of a LUT in the library (with or without .cube).
func (l *Library) Resolve(value string) (string, error) {
	if strings.ContainsRune(value, filepath.Separator) {
		abs, err := filepath.Abs(value)
		if err != nil {
			return "", err
		}
		return abs, Validate(abs)
	}
	entries, err := l.List()
	if err != nil {
		return "", err
	}
	want := strings.TrimSuffix(value, filepath.Ext(value))
	if !strings.EqualFold(filepath.Ext(value), ".cube") {
		want = value
	}
	for _, e := range entries {
		if strings.EqualFold(e.Name, want) {
			return e.Path, Validate(e.Path)
		}
	}
	return "", fmt.Errorf("no LUT named %q in %s; pass a path to a .cube file or one of the names list_luts returns", value, l.Dir)
}

// Add validates src and copies it into the library, recording camera (e.g.
// "DJI O4P") so Suggest offers it for that camera's clips. Adding a file the
// library already holds only records the camera.
func (l *Library) Add(src, camera string) (Entry, error) {
	if err := Validate(src); err != nil {
		return Entry{}, err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return Entry{}, err
	}
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		return Entry{}, fmt.Errorf("creating LUT folder: %w", err)
	}
	name := filepath.Base(src)
	if !strings.EqualFold(filepath.Ext(name), ".cube") {
		name = strings.TrimSuffix(name, filepath.Ext(name)) + ".cube"
	}
	dst := filepath.Join(l.Dir, name)
	if existing, err := os.ReadFile(dst); err == nil {
		if !bytes.Equal(existing, data) {
			return Entry{}, fmt.Errorf("the LUT folder already has a different %s; rename your file and add it again", name)
		}
	} else if err := os.WriteFile(dst, data, 0o644); err != nil {
		return Entry{}, err
	}
	if camera = strings.TrimSpace(camera); camera != "" {
		if err := l.tagCamera(name, camera); err != nil {
			return Entry{}, err
		}
	}
	return Entry{Name: strings.TrimSuffix(name, filepath.Ext(name)), Path: dst, Cameras: l.cameras()[name]}, nil
}

func (l *Library) cameras() map[string][]string {
	m := map[string][]string{}
	if b, err := os.ReadFile(filepath.Join(l.Dir, camerasFile)); err == nil {
		_ = json.Unmarshal(b, &m) // a damaged file only loses the suggestions
	}
	return m
}

func (l *Library) tagCamera(name, camera string) error {
	m := l.cameras()
	for _, c := range m[name] {
		if strings.EqualFold(c, camera) {
			return nil
		}
	}
	m[name] = append(m[name], camera)
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(l.Dir, camerasFile), b, 0o644)
}

// modelAliases are words a LUT file name uses for a camera model that
// gyroflow reports differently.
var modelAliases = map[string][]string{
	"o4p": {"o4"},
	"o4":  {"o4"},
}

// Suggest returns the entry for this camera: one tagged with it when added,
// else one whose file name mentions the model. nil when none matches.
func Suggest(entries []Entry, make, model string) *Entry {
	camera := strings.TrimSpace(make + " " + model)
	if strings.TrimSpace(model) == "" {
		return nil
	}
	for i, e := range entries {
		for _, c := range e.Cameras {
			if strings.EqualFold(c, camera) {
				return &entries[i]
			}
		}
	}
	words := append([]string{strings.ToLower(model)}, modelAliases[strings.ToLower(model)]...)
	for i, e := range entries {
		name := " " + normalize(e.Name) + " "
		for _, w := range words {
			if len(w) >= 2 && strings.Contains(name, " "+normalize(w)+" ") {
				return &entries[i]
			}
		}
	}
	return nil
}

// normalize lowercases s and turns every run of non-alphanumerics into a space.
func normalize(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			space = false
		} else if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

// DownloadURL is where the maker publishes LUTs for this camera, or "" when
// unknown (every non-DJI camera).
func DownloadURL(make, model string) string {
	if !strings.EqualFold(strings.TrimSpace(make), "DJI") {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "o4p", "o4":
		return "https://www.dji.com/es/downloads/softwares/o4-air-unit-dlog-to-rec709"
	}
	return "https://www.dji.com/es/lut"
}

// Validate checks that path is a readable 3D .cube LUT whose table has
// LUT_3D_SIZE³ entries, so a broken file fails before a render starts.
func Validate(path string) error {
	if !strings.EqualFold(filepath.Ext(path), ".cube") {
		return fmt.Errorf("%s is not a .cube LUT", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, maxCubeBytes))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	size, rows := 0, 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		switch strings.ToUpper(fields[0]) {
		case "LUT_3D_SIZE":
			if len(fields) != 2 {
				return fmt.Errorf("%s: malformed LUT_3D_SIZE", path)
			}
			n, err := strconv.Atoi(fields[1])
			if err != nil || n < 2 || n > 256 {
				return fmt.Errorf("%s: LUT_3D_SIZE must be 2-256", path)
			}
			size = n
			continue
		case "LUT_1D_SIZE":
			return fmt.Errorf("%s is a 1D LUT; only 3D LUTs are supported", path)
		case "TITLE", "DOMAIN_MIN", "DOMAIN_MAX", "LUT_3D_INPUT_RANGE":
			continue
		}
		if len(fields) != 3 {
			return fmt.Errorf("%s: unexpected line %q", path, line)
		}
		for _, v := range fields {
			if _, err := strconv.ParseFloat(v, 64); err != nil {
				return fmt.Errorf("%s: unexpected line %q", path, line)
			}
		}
		rows++
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if size == 0 {
		return fmt.Errorf("%s has no LUT_3D_SIZE", path)
	}
	if want := size * size * size; rows != want {
		return fmt.Errorf("%s has %d table rows, LUT_3D_SIZE %d needs %d", path, rows, size, want)
	}
	return nil
}
