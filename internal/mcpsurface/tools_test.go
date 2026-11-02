package mcpsurface

import (
	"context"
	"strings"
	"testing"

	"github.com/carlosFreak/gyroflow-mcp/internal/jobengine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestRenderStartInvalidConfigIsError verifies that an invalid typed config
// surfaces as IsError (validation fires before the job is started) using the
// ToolHandlerFor model - never a transport-level error, always an MCP error result.
func TestRenderStartInvalidConfigIsError(t *testing.T) {
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	eng := jobengine.New(okBackend{}, 1)
	doc := func(_ context.Context) DoctorResult { return DoctorResult{} }
	RegisterTools(srv, eng, doc, nil)

	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, t1, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := c.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	// smoothness=5 is out of range [0.001, 1.0] for method "Default" - must fail validation.
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "render_start",
		Arguments: map[string]any{
			"inputs": []any{"x.mp4"},
			"config": map[string]any{
				"smoothing": map[string]any{
					"method":     "Default",
					"smoothness": 5.0,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true for invalid config, got result: %+v", res.StructuredContent)
	}
	// The error message should mention the offending field.
	var errText strings.Builder
	for _, m := range res.Content {
		if tc, ok := m.(*mcp.TextContent); ok {
			errText.WriteString(tc.Text)
		}
	}
	if !strings.Contains(errText.String(), "smoothness") {
		t.Fatalf("error message should mention 'smoothness'; got: %s", errText.String())
	}
}
