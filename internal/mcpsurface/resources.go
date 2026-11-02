package mcpsurface

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/carlosFreak/gyroflow-mcp/internal/backend"
	"github.com/carlosFreak/gyroflow-mcp/internal/lens"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// lensUnavailableText is returned by the lens resources when the index could
// not be loaded (e.g. the server started offline). It mirrors the actionable
// message used by the find_lens_profile tool so an LLM gets consistent advice.
const lensUnavailableText = "lens database unavailable (offline at startup); retry when online or run gyroflow_doctor"

// lensProfileSummary is one entry of the lens://profiles listing.
type lensProfileSummary struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	Display string `json:"display"`
}

// RegisterResources registers the lens:// and project:// resources on s.
//
// Resource API pinned via `go doc` (go-sdk v1.6.0):
//
//	Server.AddResource(*Resource, ResourceHandler)
//	Server.AddResourceTemplate(*ResourceTemplate, ResourceHandler)
//	ResourceHandler = func(context.Context, *ReadResourceRequest) (*ReadResourceResult, error)
//	ReadResourceRequest = ServerRequest[*ReadResourceParams]; ReadResourceParams.URI string
//	ReadResourceResult{ Contents []*ResourceContents }
//	ResourceContents{ URI, MIMEType, Text string; Blob []byte }
//	ResourceNotFoundError(uri string) error
//
// idx may be nil when the lens DB failed to load; the lens resources then
// return the actionable lensUnavailableText instead of crashing.
func RegisterResources(s *mcp.Server, idx *lens.Index, be backend.Backend) {
	// lens://profiles - JSON summary of every indexed profile.
	s.AddResource(&mcp.Resource{
		Name:        "lens-profiles",
		URI:         "lens://profiles",
		MIMEType:    "application/json",
		Description: "Index of all known Gyroflow lens profiles (id, path, display).",
	}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		if idx == nil {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
				URI:      req.Params.URI,
				MIMEType: "text/plain",
				Text:     lensUnavailableText,
			}}}, nil
		}
		profiles := idx.Profiles()
		summaries := make([]lensProfileSummary, 0, len(profiles))
		for _, p := range profiles {
			summaries = append(summaries, lensProfileSummary{
				ID:      p.ID,
				Path:    p.Path,
				Display: p.Display,
			})
		}
		b, err := json.Marshal(summaries)
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI:      req.Params.URI,
			MIMEType: "application/json",
			Text:     string(b),
		}}}, nil
	})

	// lens://profile/{id} - raw JSON of a single profile by id.
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "lens-profile",
		URITemplate: "lens://profile/{id}",
		MIMEType:    "application/json",
		Description: "Raw JSON of a single Gyroflow lens profile, addressed by its identifier.",
	}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		uri := req.Params.URI
		if idx == nil {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		id := strings.TrimPrefix(uri, "lens://profile/")
		hit, ok := idx.ByID(id)
		if !ok {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		data, err := os.ReadFile(hit.Path)
		if err != nil {
			// Any read failure is mapped to ResourceNotFoundError per the MCP
			// resource model (a URI either resolves to content or it does not).
			// This intentionally collapses EACCES/transient IO into "not found"
			// rather than leaking a raw backend error string to the client.
			return nil, mcp.ResourceNotFoundError(uri)
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI:      uri,
			MIMEType: "application/json",
			Text:     string(data),
		}}}, nil
	})

	// gyroflow://capabilities - static Markdown reference of every render_start
	// config field (meaning, unit, range, enum values, parameter routing rules).
	// Serves as the LLM's authoritative source for translating natural-language
	// requests into a typed config without guessing.
	s.AddResource(&mcp.Resource{
		Name:        "gyroflow-capabilities",
		URI:         "gyroflow://capabilities",
		MIMEType:    "text/markdown",
		Description: "Complete reference of every render_start config field: meaning, unit, range, enum values, and when to use it. Consult this to translate a natural-language request into a typed config.",
	}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI:      req.Params.URI,
			MIMEType: "text/markdown",
			Text:     capabilitiesDoc(),
		}}}, nil
	})

	// project://{path}/metadata - compact gyroflow metadata summary plus the
	// on-disk path to the full raw export. The multi-MB raw blob is never
	// inlined into the resource body, so callers never have to parse the
	// raw multi-MB export directly.
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "project-metadata",
		URITemplate: "project://{path}/metadata",
		MIMEType:    "application/json",
		Description: "Compact gyroflow metadata summary (camera, lens, gyro, resolution, fps, duration, frame_count) + raw_path to the full export.",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		uri := req.Params.URI
		// Strip the scheme and the trailing "/metadata" to recover the path.
		// Absolute paths yield "project:///abs/path/file.mp4/metadata".
		rest := strings.TrimPrefix(uri, "project://")
		path := strings.TrimSuffix(rest, "/metadata")
		if path == "" || path == rest {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		// Resolve and existence-check before hitting the backend so a bad path
		// maps to ResourceNotFoundError (consistent with lens://profile/{id})
		// instead of leaking a raw backend error string.
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		if _, err := os.Stat(abs); err != nil {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		r, err := be.ProbeMetadata(ctx, abs, backend.ProbeOptions{})
		if err != nil {
			return nil, err
		}
		// Marshal the compact result {summary, raw_path}; never the raw blob.
		b, err := json.Marshal(r)
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI:      uri,
			MIMEType: "application/json",
			Text:     string(b),
		}}}, nil
	})
}
