package toolbox_test

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/microsoft/typescript-go/toolbox"
)

func TestExtractToolMetadata(t *testing.T) {
	t.Parallel()

	meta, err := toolbox.ExtractToolMetadata(context.Background(), toolbox.ExtractInput{
		Files: fstest.MapFS{
			"tool.ts": {
				Data: []byte(`/**
 * List all users in the Google Workspace domain
 */
export default async function tool(params: { query: string; limit?: number }, ctx: unknown) {
  return [];
}
`),
			},
		},
		Entry: "tool.ts",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.Description != "List all users in the Google Workspace domain" {
		t.Fatalf("expected description %q, got %q", "List all users in the Google Workspace domain", meta.Description)
	}
	if meta.ParamsSchema == nil {
		t.Fatal("expected params schema")
	}
	schemaType, _ := meta.ParamsSchema["type"].(string)
	if schemaType != "object" {
		t.Fatalf("expected object schema type, got %q", schemaType)
	}
	props, _ := meta.ParamsSchema["properties"].(map[string]any)
	if props == nil {
		t.Fatal("expected properties in params schema")
	}
	if _, ok := props["query"]; !ok {
		t.Fatal("expected 'query' property in params schema")
	}
	if _, ok := props["limit"]; !ok {
		t.Fatal("expected 'limit' property in params schema")
	}

	// Sig (facade) checks.
	if meta.Sig == nil {
		t.Fatal("expected Sig to be populated")
	}
	sigParams := meta.Sig.Params()
	if len(sigParams) < 1 {
		t.Fatalf("expected at least 1 param in Sig, got %d", len(sigParams))
	}
	if sigParams[0].Name() != "params" {
		t.Fatalf("expected Sig.Params()[0].Name() to be %q, got %q", "params", sigParams[0].Name())
	}
	if sigParams[0].Type() == nil {
		t.Fatal("expected Sig.Params()[0].Type() to be non-nil")
	}
	if !sigParams[0].Type().IsObject() {
		t.Fatal("expected Sig.Params()[0].Type().IsObject() to be true")
	}

	// ParamsType should be non-nil and wrap the same inner type.
	if meta.ParamsType == nil {
		t.Fatal("expected ParamsType to be non-nil")
	}
	if meta.ParamsType.Inner() != sigParams[0].Type().Inner() {
		t.Fatal("expected ParamsType.Inner() to match Sig.Params()[0].Type().Inner()")
	}
}

func TestExtractToolMetadataNoJSDoc(t *testing.T) {
	t.Parallel()

	meta, err := toolbox.ExtractToolMetadata(context.Background(), toolbox.ExtractInput{
		Files: fstest.MapFS{
			"tool.ts": {
				Data: []byte(`export default async function tool(params: { name: string }, ctx: unknown) {
  return params.name;
}
`),
			},
		},
		Entry: "tool.ts",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.Description != "" {
		t.Fatalf("expected empty description, got %q", meta.Description)
	}
	if meta.ParamsSchema == nil {
		t.Fatal("expected params schema")
	}
}

func TestExtractToolMetadataRecordStringNeverParams(t *testing.T) {
	t.Parallel()

	meta, err := toolbox.ExtractToolMetadata(context.Background(), toolbox.ExtractInput{
		Files: fstest.MapFS{
			"tool.ts": {
				Data: []byte(`/**
 * List Google Workspace users
 */
export default async function tool(params: Record<string, never>, ctx: unknown) {
  return [];
}
`),
			},
		},
		Entry: "tool.ts",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.Description != "List Google Workspace users" {
		t.Fatalf("expected description, got %q", meta.Description)
	}
}

func TestExtractToolMetadataNoDefaultExport(t *testing.T) {
	t.Parallel()

	_, err := toolbox.ExtractToolMetadata(context.Background(), toolbox.ExtractInput{
		Files: fstest.MapFS{
			"tool.ts": {
				Data: []byte(`export function tool(params: { name: string }, ctx: unknown) {
  return params.name;
}
`),
			},
		},
		Entry: "tool.ts",
	})
	if err == nil {
		t.Fatal("expected error for missing default export")
	}
}
