package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Errorist79/gyroflow-mcp/internal/backend"
)

// staleJobAge is how old an abandoned job directory must be before startup
// removes it. Long enough that a render in another server process is never hit.
const staleJobAge = 48 * time.Hour

// ErrStagingUnsupported is returned for operations whose result would keep
// referencing the temporary staged copy of the input.
var ErrStagingUnsupported = errors.New("not available with the sandboxed App Store build of Gyroflow: the output references a temporary copy of the clip; install the DMG build (`brew install --cask gyroflow`) to use it")

// StagingBackend implements backend.Backend for the sandboxed App Store build.
// That build can only access its own Application Support folder, so each job
// clones its inputs into root, renders there, and moves the outputs back.
type StagingBackend struct {
	inner *CLIBackend
	root  string
}

// NewStaging creates a StagingBackend working under root, which must be inside
// a folder the sandbox can read and write. Abandoned job directories are pruned.
func NewStaging(bin, root string) *StagingBackend {
	pruneStaleJobs(root, staleJobAge)
	return &StagingBackend{inner: New(bin), root: root}
}

func pruneStaleJobs(root string, age time.Duration) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "job-") {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > age {
			os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}

func (s *StagingBackend) newJobDir() (string, error) {
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return "", fmt.Errorf("creating work folder: %w", err)
	}
	return os.MkdirTemp(s.root, "job-")
}

func (s *StagingBackend) Stabilize(ctx context.Context, req backend.StabilizeRequest, onProgress backend.ProgressFunc) (*backend.Result, error) {
	req, err := prepareStabilize(req)
	if err != nil {
		return nil, err
	}
	req.Config = nil // consumed by prepareStabilize; the inner run must not redo it
	if req, err = resolveStabilizeReq(req); err != nil {
		return nil, err
	}

	suffix := req.Suffix
	if suffix == "" {
		suffix = "_stabilized"
	}
	destDir, err := requestedOutputFolder(req.OutParams)
	if err != nil {
		return nil, err
	}
	if err := s.checkInputs(req, destDir, suffix); err != nil {
		return nil, err
	}

	job, err := s.newJobDir()
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(job)

	staged := req
	staged.Inputs = make([]string, len(req.Inputs))
	for i, in := range req.Inputs {
		if staged.Inputs[i], err = stageFile(ctx, in, job); err != nil {
			return nil, err
		}
	}
	if req.GyroFile != "" {
		if staged.GyroFile, err = stageFile(ctx, req.GyroFile, job); err != nil {
			return nil, err
		}
	}
	outDir := filepath.Join(job, "out")
	if err := os.Mkdir(outDir, 0o755); err != nil {
		return nil, err
	}
	staged.OutParams = backend.OutParams{}
	for k, v := range req.OutParams {
		staged.OutParams[k] = v
	}
	// Gyroflow fails with a misleading "No such file or directory" under the
	// sandbox unless the output folder is explicit.
	staged.OutParams["output_folder"] = outDir + string(os.PathSeparator)

	res, err := s.inner.Stabilize(ctx, staged, onProgress)
	if err != nil {
		return res, err
	}
	res.OutputPaths, err = moveOutputs(ctx, outDir, req, destDir, suffix)
	return res, err
}

// checkInputs rejects what cannot work before any file is copied or rendered.
func (s *StagingBackend) checkInputs(req backend.StabilizeRequest, destDir, suffix string) error {
	seen := map[string]bool{}
	var clone, render int64
	for _, in := range req.Inputs {
		if strings.EqualFold(filepath.Ext(in), ".gyroflow") {
			return fmt.Errorf("%s: %w", in, ErrStagingUnsupported)
		}
		base := filepath.Base(in)
		if seen[strings.ToLower(base)] {
			return fmt.Errorf("two inputs are named %s: render them in separate jobs", base)
		}
		seen[strings.ToLower(base)] = true

		info, err := os.Stat(in)
		if err != nil {
			return err
		}
		render += info.Size() // the output is roughly the size of the input
		if !sameDevice(in, s.root) {
			clone += info.Size() // on the same APFS volume a clone takes no space
		}

		if !req.Overwrite {
			dir := destDir
			if dir == "" {
				dir = filepath.Dir(in)
			}
			matches, err := filepath.Glob(filepath.Join(globEscape(dir), globEscape(stem(base)+suffix)+".*"))
			if err != nil {
				return err
			}
			if len(matches) > 0 {
				return fmt.Errorf("output already exists: %s (set overwrite or use another suffix)", matches[0])
			}
		}
	}
	free, ok := freeBytes(filepath.Dir(s.root))
	if need := clone + render; ok && uint64(need) > free {
		return fmt.Errorf("not enough space on the internal disk: need about %.1f GB in %s, %.1f GB free", float64(need)/1e9, s.root, float64(free)/1e9)
	}
	return nil
}

// moveOutputs delivers the rendered files and returns their final paths.
// Gyroflow picks the extension from the codec, so outputs are matched by stem.
func moveOutputs(ctx context.Context, outDir string, req backend.StabilizeRequest, destDir, suffix string) ([]string, error) {
	entries, err := os.ReadDir(outDir)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, in := range req.Inputs {
		want := stem(filepath.Base(in)) + suffix
		dir := destDir
		if dir == "" {
			dir = filepath.Dir(in)
		}
		found := false
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || strings.HasSuffix(name, ".tmp") || !strings.HasPrefix(name, want) {
				continue
			}
			dst := filepath.Join(dir, name)
			if err := moveFile(ctx, filepath.Join(outDir, name), dst, req.Overwrite); err != nil {
				return paths, err
			}
			paths = append(paths, dst)
			found = true
		}
		if !found {
			return paths, fmt.Errorf("gyroflow finished but produced no output for %s", in)
		}
	}
	return paths, nil
}

func (s *StagingBackend) ProbeMetadata(ctx context.Context, input string, opts backend.ProbeOptions) (*backend.MetadataResult, error) {
	absIn, err := absInput(input)
	if err != nil {
		return nil, err
	}
	job, err := s.newJobDir()
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(job)
	staged, err := stageFile(ctx, absIn, job)
	if err != nil {
		return nil, err
	}
	// Keyed by the original path: one export per clip, however often it is probed.
	metaDir := filepath.Join(s.root, "metadata")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(absIn))
	rawPath := filepath.Join(metaDir, "gyroflow-meta-"+hex.EncodeToString(sum[:])+".json")
	return s.inner.probeTo(ctx, staged, rawPath, opts)
}

func (s *StagingBackend) ExportProject(context.Context, backend.ExportRequest) (*backend.Result, error) {
	return nil, ErrStagingUnsupported
}

func (s *StagingBackend) ExportSTMap(context.Context, backend.STMapRequest) (*backend.Result, error) {
	return nil, ErrStagingUnsupported
}

func requestedOutputFolder(p backend.OutParams) (string, error) {
	v, ok := p["output_folder"]
	if !ok {
		return "", nil
	}
	dir, ok := v.(string)
	if !ok || dir == "" {
		return "", fmt.Errorf("out_params.output_folder must be a folder path")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return "", fmt.Errorf("output folder not found: %s", abs)
	}
	return abs, nil
}

func stem(name string) string { return strings.TrimSuffix(name, filepath.Ext(name)) }

// globEscape quotes the glob metacharacters filepath.Match understands.
func globEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`).Replace(s)
}

// stageFile clones src into dir, falling back to a plain copy across volumes.
func stageFile(ctx context.Context, src, dir string) (string, error) {
	dst := filepath.Join(dir, filepath.Base(src))
	if err := exec.CommandContext(ctx, "cp", "-c", src, dst).Run(); err == nil {
		return dst, nil
	}
	if err := copyFile(ctx, src, dst); err != nil {
		return "", fmt.Errorf("copying %s to the work folder: %w", src, err)
	}
	return dst, nil
}

// moveFile moves src to dst, copying when they are on different volumes. A
// partial copy is never left at dst, and dst is only replaced when overwrite is set.
func moveFile(ctx context.Context, src, dst string, overwrite bool) error {
	if !overwrite {
		if _, err := os.Lstat(dst); err == nil {
			return fmt.Errorf("output already exists: %s", dst)
		}
	}
	err := os.Rename(src, dst)
	if err == nil || !errors.Is(err, syscall.EXDEV) {
		return err
	}
	tmp := dst + ".tmp"
	if err := copyFile(ctx, src, tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

func copyFile(ctx context.Context, src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, ctxReader{ctx, in}); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
