// Command gyroflow-mcp is an MCP server that drives the Gyroflow CLI.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
	"github.com/carlosFreak/gyroflow-mcp/internal/backend/cli"
	"github.com/carlosFreak/gyroflow-mcp/internal/gyroflow"
	"github.com/carlosFreak/gyroflow-mcp/internal/jobengine"
	"github.com/carlosFreak/gyroflow-mcp/internal/lens"
	"github.com/carlosFreak/gyroflow-mcp/internal/lut"
	"github.com/carlosFreak/gyroflow-mcp/internal/mcpsurface"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// version is set at release time by scripts/release.sh.
var version = "dev"

// lensSyncTimeout bounds the startup lens-DB download so an unreachable or
// hanging GitHub can never block server startup. On timeout we degrade to a
// cached index (if any) or a nil index.
const lensSyncTimeout = 30 * time.Second

// loadLensIndex tries to sync the gyroflow lens-profile database into a cache
// directory. It NEVER blocks startup on failure: an offline / failed sync
// degrades to a nil index, and the lens tool/resources surface an actionable
// "unavailable" message instead of crashing.
func loadLensIndex(ctx context.Context) *lens.Index {
	base, err := os.UserCacheDir()
	if err != nil {
		log.Printf("lens: cannot resolve user cache dir: %v - lens features disabled", err)
		return nil
	}
	cacheDir := filepath.Join(base, "gyroflow-mcp", "lens_profiles")

	ctx, cancel := context.WithTimeout(ctx, lensSyncTimeout)
	defer cancel()
	idx, err := lens.Sync(ctx, cacheDir)
	if err != nil {
		// Offline or download failure: fall back to whatever is already cached.
		if cached, lerr := lens.LoadFromDir(cacheDir); lerr == nil && cached.Len() > 0 {
			log.Printf("lens: sync failed (%v) - using %d cached profiles", err, cached.Len())
			return cached
		}
		log.Printf("lens: sync failed and no cache (%v) - lens features return 'unavailable'", err)
		return nil
	}
	return idx
}

// loadLensIndexFn is a seam so tests can substitute a hermetic, offline lens
// loader. Production (main()) uses the real loadLensIndex by default.
var loadLensIndexFn = loadLensIndex

// newBackend staged-renders with the sandboxed App Store build and drives the
// binary directly otherwise.
func newBackend(ctx context.Context, bin string) backend.Backend {
	if sr, err := gyroflow.InspectSandbox(ctx, bin); err == nil && sr.StagingAvailable {
		if root, err := gyroflow.StagingRoot(); err == nil {
			return cli.NewStaging(bin, root)
		}
	}
	return cli.New(bin)
}

func buildServer() *mcp.Server {
	srv := mcp.NewServer(
		&mcp.Implementation{Name: "gyroflow-mcp", Version: version},
		&mcp.ServerOptions{Instructions: mcpsurface.ServerInstructions()},
	)

	info, err := gyroflow.Detect(context.Background())
	if err != nil {
		log.Printf("gyroflow not detected: %v - running in detect-only mode", err)
		return srv
	}

	ffmpeg := cli.FindFFmpeg()
	if ffmpeg == "" {
		log.Printf("ffmpeg not found - LUTs disabled (brew install ffmpeg)")
	}
	be := cli.NewLUT(newBackend(context.Background(), info.Path), ffmpeg)
	eng := jobengine.New(be, 1)

	doc := func(ctx context.Context) mcpsurface.DoctorResult {
		inf, err := gyroflow.Detect(ctx)
		if err != nil {
			return mcpsurface.DoctorResult{
				Found:  false,
				Usable: false,
				Advice: "gyroflow not found: " + err.Error(),
			}
		}
		sr, sandboxErr := gyroflow.InspectSandbox(ctx, inf.Path)
		advice := sr.Advice
		if sandboxErr != nil {
			advice = "could not verify code signature (" + sandboxErr.Error() + ") - if this is the App Store build the CLI will fail; prefer the DMG build"
		}
		return mcpsurface.DoctorResult{
			Found:     true,
			Usable:    sandboxErr == nil && (!sr.Sandboxed || sr.StagingAvailable),
			Path:      inf.Path,
			Version:   inf.Version,
			Sandboxed: sr.Sandboxed,
			AppStore:  sr.AppStore,
			Advice:    advice,
		}.WithFFmpeg(ffmpeg)
	}
	idx := loadLensIndexFn(context.Background())
	var opts []mcpsurface.ToolOption
	if ffmpeg != "" {
		opts = append(opts, mcpsurface.WithLUTs(&lut.Library{Dir: lut.DefaultDir()}))
	}
	mcpsurface.RegisterTools(srv, eng, doc, idx, opts...)
	mcpsurface.RegisterResources(srv, idx, be)
	mcpsurface.RegisterPrompts(srv)

	return srv
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(version)
		return
	}
	srv := buildServer()
	if err := srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("gyroflow-mcp server failed: %v", err)
	}
}
