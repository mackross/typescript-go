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

	// FuncSig checks.
	if meta.FuncSig == nil {
		t.Fatal("expected FuncSig to be populated")
	}
	if len(meta.FuncSig.Params) < 1 {
		t.Fatalf("expected at least 1 param in FuncSig, got %d", len(meta.FuncSig.Params))
	}
	if meta.FuncSig.Params[0].Name != "params" {
		t.Fatalf("expected FuncSig.Params[0].Name to be %q, got %q", "params", meta.FuncSig.Params[0].Name)
	}
	if meta.FuncSig.Params[0].Type == nil {
		t.Fatal("expected FuncSig.Params[0].Type to be non-nil")
	}
	if meta.FuncSig.Params[0].Type.Kind != toolbox.TSTypeObject {
		t.Fatalf("expected FuncSig.Params[0].Type.Kind to be TSTypeObject, got %v", meta.FuncSig.Params[0].Type.Kind)
	}

	// ParamsTSType should match FuncSig.Params[0].Type.
	if meta.ParamsTSType == nil {
		t.Fatal("expected ParamsTSType to be non-nil")
	}
	if meta.ParamsTSType != meta.FuncSig.Params[0].Type {
		t.Fatal("expected ParamsTSType to be the same pointer as FuncSig.Params[0].Type")
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
