package gyroflow

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// SandboxResult describes the sandbox inspection outcome for a gyroflow binary.
type SandboxResult struct {
	// Sandboxed is true when the com.apple.security.app-sandbox entitlement is present
	// (App Store build). Such builds cannot read arbitrary files without Full Disk Access.
	Sandboxed bool
	// AppStore is true when the binary is signed by Apple Mac OS Application Signing.
	AppStore bool
	// StagingAvailable is true when the sandbox still grants read/write on
	// Gyroflow's Application Support folder, which staging relies on.
	StagingAvailable bool
	// Advice is a non-empty actionable message when Sandboxed is true.
	Advice string
}

// stagingEntitlement is the home-relative path exception in the App Store build.
const stagingEntitlement = "/Library/Application Support/Gyroflow/"

const stagingAdvice = "App Store build detected: clips are copied to Gyroflow's Application Support folder, rendered there and moved back. Clips on another disk need free space on the internal disk for the copy and the render. export_project and export_stmap need the DMG build (`brew install --cask gyroflow`)."

// StagingRoot returns the work folder used with the sandboxed build.
func StagingRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", "Gyroflow", "gyroflow-mcp"), nil
}

// classifyEntitlements returns true when the codesign output contains the
// sandbox entitlement key. Pinned to the real captured string from the App Store
// build: "[Key] com.apple.security.app-sandbox".
func classifyEntitlements(codesignOutput string) bool {
	return strings.Contains(codesignOutput, "com.apple.security.app-sandbox")
}

// classifyAuthority returns true when the codesign output contains the App Store
// signing authority. Pinned to the real captured string:
// "Authority=Apple Mac OS Application Signing".
func classifyAuthority(codesignOutput string) bool {
	return strings.Contains(codesignOutput, "Authority=Apple Mac OS Application Signing")
}

const sandboxAdvice = "gyroflow could not read the input. Most common cause on macOS: the installed Gyroflow is the sandboxed App Store build (run gyroflow_doctor; install the DMG build via `brew install --cask gyroflow`). Also ensure the path is absolute and the file exists."

// appBundlePath derives the .app bundle directory from a binary path.
// For a path containing a *.app component (e.g.
// /Applications/Gyroflow.app/Contents/MacOS/gyroflow), it returns the bundle
// root (e.g. /Applications/Gyroflow.app). If no .app component is found
// (Homebrew symlinks, Linux, etc.) it returns the original path unchanged.
func appBundlePath(binPath string) string {
	dir := binPath
	for {
		base := filepath.Base(dir)
		if strings.HasSuffix(base, ".app") {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root without finding a .app component.
			return binPath
		}
		dir = parent
	}
}

// InspectSandbox runs codesign on the binary (or its .app bundle) and returns
// its sandbox status.  On non-darwin platforms it always returns the zero
// SandboxResult with a nil error.  A non-nil error means codesign was present
// but failed (binary unsigned, permissions, etc.) - the caller should treat
// this as "could not verify" rather than "not sandboxed".
func InspectSandbox(ctx context.Context, binPath string) (SandboxResult, error) {
	if runtime.GOOS != "darwin" {
		return SandboxResult{}, nil
	}
	target := appBundlePath(binPath)
	out, err := exec.CommandContext(ctx,
		"codesign", "--display", "--entitlements", "-", "--verbose", target,
	).CombinedOutput()
	if err != nil {
		return SandboxResult{}, fmt.Errorf("codesign %s: %w", target, err)
	}
	s := string(out)
	sandboxed := classifyEntitlements(s)
	appStore := classifyAuthority(s)
	staging := sandboxed && strings.Contains(s, stagingEntitlement)
	var advice string
	switch {
	case staging:
		advice = stagingAdvice
	case sandboxed:
		advice = sandboxAdvice
	}
	return SandboxResult{Sandboxed: sandboxed, AppStore: appStore, StagingAvailable: staging, Advice: advice}, nil
}
