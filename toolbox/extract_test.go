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
	if meta.ParamsType.ToTS() != sigParams[0].Type().ToTS() {
		t.Fatal("expected ParamsType.ToTS() to match Sig.Params()[0].Type().ToTS()")
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

func TestExtractToolMetadataJSDocTags(t *testing.T) {
	t.Parallel()

	meta, err := toolbox.ExtractToolMetadata(context.Background(), toolbox.ExtractInput{
		Files: fstest.MapFS{
			"main.ts": {
				Data: []byte(`/** Options for the operation */
interface Options {
  /** Only include items after this date */
  from: string;
  /** Only include items before this date */
  to: string;
}

/**
 * Perform a calculation with metadata.
 * @accessMode readOnly
 * @idempotent
 * @param a - The first number
 * @param b - The second number
 * @param options - Filtering options
 */
export default function(a: number, b: number, options: Options): number {
  return a + b;
}
`),
			},
		},
		Entry: "main.ts",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sig := meta.Sig
	if sig == nil {
		t.Fatal("expected Sig to be populated")
	}

	// Check description.
	if got := sig.Description(); got != "Perform a calculation with metadata." {
		t.Fatalf("expected description %q, got %q", "Perform a calculation with metadata.", got)
	}

	// Check tags.
	tags := sig.Tags()
	expectedTags := []toolbox.JSDocTag{
		{Name: "accessMode", Text: "readOnly"},
		{Name: "idempotent", Text: ""},
		{Name: "param", Text: "a - The first number"},
		{Name: "param", Text: "b - The second number"},
		{Name: "param", Text: "options - Filtering options"},
	}
	if len(tags) != len(expectedTags) {
		t.Fatalf("expected %d tags, got %d: %v", len(expectedTags), len(tags), tags)
	}
	for i, exp := range expectedTags {
		if tags[i].Name != exp.Name || tags[i].Text != exp.Text {
			t.Fatalf("tag[%d]: expected {%q, %q}, got {%q, %q}", i, exp.Name, exp.Text, tags[i].Name, tags[i].Text)
		}
	}

	// Check param descriptions.
	params := sig.Params()
	if len(params) != 3 {
		t.Fatalf("expected 3 params, got %d", len(params))
	}
	wantDescs := []string{"The first number", "The second number", "Filtering options"}
	for i, want := range wantDescs {
		if got := params[i].Description(); got != want {
			t.Fatalf("param[%d] (%s) description: expected %q, got %q", i, params[i].Name(), want, got)
		}
	}

	// Check nested type descriptions via JSON Schema.
	optionsSchema := params[2].Type().ToJSONSchema()
	props, ok := optionsSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties in options schema, got %v", optionsSchema)
	}
	for _, propCase := range []struct {
		name string
		desc string
	}{
		{"from", "Only include items after this date"},
		{"to", "Only include items before this date"},
	} {
		propMap, ok := props[propCase.name].(map[string]any)
		if !ok {
			t.Fatalf("expected %q property in options schema", propCase.name)
		}
		desc, _ := propMap["description"].(string)
		if desc != propCase.desc {
			t.Fatalf("property %q: expected description %q, got %q", propCase.name, propCase.desc, desc)
		}
	}

}

func TestCombinedParamsType(t *testing.T) {
	t.Parallel()

	meta, err := toolbox.ExtractToolMetadata(context.Background(), toolbox.ExtractInput{
		Files: fstest.MapFS{
			"main.ts": {
				Data: []byte(`/** Options for filtering */
interface FilterOptions {
  /** Only include items after this date */
  from: string;
  /** Only include items before this date */
  to: string;
}

/**
 * Search with multiple parameters.
 * @param query - The search query
 * @param limit - Maximum results to return
 * @param offset - Number of results to skip
 * @param filters - Optional filter criteria
 */
export default function(query: string, limit: number, offset?: number, filters?: FilterOptions): string {
  return query;
}
`),
			},
		},
		Entry: "main.ts",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sig := meta.Sig
	if sig == nil {
		t.Fatal("expected Sig to be populated")
	}

	// Verify params count.
	params := sig.Params()
	if len(params) != 4 {
		t.Fatalf("expected 4 params, got %d", len(params))
	}

	// Verify optionality.
	if params[0].Optional() {
		t.Fatal("expected params[0] (query) to not be optional")
	}
	if params[1].Optional() {
		t.Fatal("expected params[1] (limit) to not be optional")
	}
	if !params[2].Optional() {
		t.Fatal("expected params[2] (offset) to be optional")
	}
	if !params[3].Optional() {
		t.Fatal("expected params[3] (filters) to be optional")
	}

	// Verify CombinedParamsType JSON Schema.
	combined := sig.CombinedParamsType()
	if combined == nil {
		t.Fatal("expected CombinedParamsType to be non-nil")
	}
	schema := combined.ToJSONSchema()

	schemaType, _ := schema["type"].(string)
	if schemaType != "object" {
		t.Fatalf("expected object schema type, got %q", schemaType)
	}

	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties in schema, got %v", schema)
	}
	for _, name := range []string{"query", "limit", "offset", "filters"} {
		if _, ok := props[name]; !ok {
			t.Fatalf("expected %q property in schema", name)
		}
	}

	// Verify required list (tsTypeToJSON emits []string, not []any).
	requiredRaw, hasRequired := schema["required"]
	if !hasRequired {
		t.Fatalf("expected required in schema, got %v", schema)
	}
	var requiredList []string
	switch r := requiredRaw.(type) {
	case []string:
		requiredList = r
	case []any:
		for _, v := range r {
			requiredList = append(requiredList, v.(string))
		}
	default:
		t.Fatalf("unexpected required type %T", requiredRaw)
	}
	requiredSet := map[string]bool{}
	for _, r := range requiredList {
		requiredSet[r] = true
	}
	if !requiredSet["query"] || !requiredSet["limit"] {
		t.Fatalf("expected required to contain query and limit, got %v", requiredList)
	}
	if requiredSet["offset"] || requiredSet["filters"] {
		t.Fatalf("expected required to not contain offset or filters, got %v", requiredList)
	}

	// Verify query description.
	queryProp, _ := props["query"].(map[string]any)
	if queryProp == nil {
		t.Fatal("expected query property to be a map")
	}
	if desc, _ := queryProp["description"].(string); desc != "The search query" {
		t.Fatalf("expected query description %q, got %q", "The search query", desc)
	}

	// Verify filters references FilterOptions via $ref.
	filtersProp, _ := props["filters"].(map[string]any)
	if filtersProp == nil {
		t.Fatal("expected filters property to be a map")
	}

	// The filters property should use a $ref to definitions.
	// Resolve through definitions to verify nested properties.
	ref, hasRef := filtersProp["$ref"].(string)
	var filterProps map[string]any
	if hasRef && ref == "#/definitions/FilterOptions" {
		defs, _ := schema["definitions"].(map[string]any)
		if defs == nil {
			t.Fatal("expected definitions in schema for $ref resolution")
		}
		defSchema, _ := defs["FilterOptions"].(map[string]any)
		if defSchema == nil {
			t.Fatal("expected FilterOptions in definitions")
		}
		filterProps, _ = defSchema["properties"].(map[string]any)
	} else {
		// Inline properties (fallback).
		filterProps, _ = filtersProp["properties"].(map[string]any)
	}
	if filterProps == nil {
		t.Fatalf("expected nested properties for filters, got %v", filtersProp)
	}
	for _, propCase := range []struct {
		name string
		desc string
	}{
		{"from", "Only include items after this date"},
		{"to", "Only include items before this date"},
	} {
		propMap, ok := filterProps[propCase.name].(map[string]any)
		if !ok {
			t.Fatalf("expected %q property in filters schema", propCase.name)
		}
		desc, _ := propMap["description"].(string)
		if desc != propCase.desc {
			t.Fatalf("property %q: expected description %q, got %q", propCase.name, propCase.desc, desc)
		}
	}

	// Verify ToTS renders something reasonable.
	tsText := combined.ToTS()
	if tsText == "" || tsText == "any" {
		t.Fatalf("expected non-trivial ToTS output, got %q", tsText)
	}
}
