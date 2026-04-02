package toolbox

import (
	"encoding/json"
	"testing"
)

func TestNewStringLiteralUnion(t *testing.T) {
	t.Parallel()

	t.Run("nil returns nil", func(t *testing.T) {
		got := NewStringLiteralUnion(nil)
		if got != nil {
			t.Fatalf("expected nil, got %v", got)
		}
	})

	t.Run("empty returns nil", func(t *testing.T) {
		got := NewStringLiteralUnion([]string{})
		if got != nil {
			t.Fatalf("expected nil, got %v", got)
		}
	})

	t.Run("single value is literal type", func(t *testing.T) {
		got := NewStringLiteralUnion([]string{"admin"})
		if got == nil {
			t.Fatal("expected non-nil")
		}

		// TS rendering: should be "admin" (quoted literal)
		ts := got.ToTS()
		if ts != `"admin"` {
			t.Fatalf("expected TS %q, got %q", `"admin"`, ts)
		}

		// JSON Schema: single literal collapses to {"type":"string","const":"admin"}
		schema := got.ToJSONSchema()
		if schema["type"] != "string" {
			t.Fatalf("expected type=string, got %v", schema["type"])
		}
		if schema["const"] != "admin" {
			t.Fatalf("expected const=admin, got %v", schema["const"])
		}
	})

	t.Run("multiple values is union type", func(t *testing.T) {
		got := NewStringLiteralUnion([]string{"admin", "personal"})
		if got == nil {
			t.Fatal("expected non-nil")
		}

		// TS rendering: should be "admin" | "personal"
		ts := got.ToTS()
		if ts != `"admin" | "personal"` {
			t.Fatalf("expected TS %q, got %q", `"admin" | "personal"`, ts)
		}

		// JSON Schema: {"type":"string","enum":["admin","personal"]}
		schema := got.ToJSONSchema()
		enumVals, ok := schema["enum"].([]any)
		if !ok {
			t.Fatalf("expected enum in schema, got %v", schema)
		}
		if len(enumVals) != 2 {
			t.Fatalf("expected 2 enum values, got %d", len(enumVals))
		}
		if enumVals[0] != "admin" || enumVals[1] != "personal" {
			t.Fatalf("expected enum=[admin, personal], got %v", enumVals)
		}
	})
}

func TestFuncSignatureAddParam(t *testing.T) {
	t.Parallel()

	// Create a base FuncSignature with one param.
	base := &FuncSignature{
		inner: &tsFuncSig{
			Description: "test function",
			Params: []tsFuncParam{
				{
					Name:        "query",
					Type:        &tsType{Kind: tsTypePrimitive, PrimitiveType: "string"},
					Description: "search query",
					Optional:    false,
				},
			},
		},
	}

	t.Run("adds param to existing signature", func(t *testing.T) {
		unionType := NewStringLiteralUnion([]string{"admin", "personal"})
		extended := base.AddParam("account", unionType, "Account to use", false)

		// Original should be unchanged.
		if len(base.Params()) != 1 {
			t.Fatalf("expected original to have 1 param, got %d", len(base.Params()))
		}

		// Extended should have 2 params.
		params := extended.Params()
		if len(params) != 2 {
			t.Fatalf("expected 2 params, got %d", len(params))
		}
		if params[0].Name() != "query" {
			t.Fatalf("expected first param 'query', got %q", params[0].Name())
		}
		if params[1].Name() != "account" {
			t.Fatalf("expected second param 'account', got %q", params[1].Name())
		}
		if params[1].Description() != "Account to use" {
			t.Fatalf("expected description 'Account to use', got %q", params[1].Description())
		}
		if params[1].Optional() {
			t.Fatal("expected param to be required")
		}
	})

	t.Run("ParamsAsObject includes new param", func(t *testing.T) {
		unionType := NewStringLiteralUnion([]string{"admin", "personal"})
		extended := base.AddParam("account", unionType, "Account to use", false)

		obj := extended.ParamsAsObject()
		if obj == nil {
			t.Fatal("expected non-nil ParamsAsObject")
		}

		props := obj.Properties()
		if len(props) != 2 {
			t.Fatalf("expected 2 properties, got %d", len(props))
		}

		// Check the account property exists with enum in schema.
		schema := obj.ToJSONSchema()
		propsMap, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("expected properties map, got %v", schema)
		}
		acctProp, ok := propsMap["account"].(map[string]any)
		if !ok {
			t.Fatalf("expected account property, got %v", propsMap)
		}
		enumVals, ok := acctProp["enum"].([]any)
		if !ok {
			t.Fatalf("expected enum in account property, got %v", acctProp)
		}
		if len(enumVals) != 2 {
			t.Fatalf("expected 2 enum values, got %d", len(enumVals))
		}

		// Required should include both query and account.
		requiredSet := make(map[string]bool)
		switch r := schema["required"].(type) {
		case []any:
			for _, v := range r {
				requiredSet[v.(string)] = true
			}
		case []string:
			for _, v := range r {
				requiredSet[v] = true
			}
		default:
			t.Fatalf("expected required array, got %T: %v", schema["required"], schema["required"])
		}
		if !requiredSet["query"] {
			t.Fatal("expected 'query' in required")
		}
		if !requiredSet["account"] {
			t.Fatal("expected 'account' in required")
		}
	})

	t.Run("ToJSONSchema includes enum", func(t *testing.T) {
		// Build a sig where the first param is an object (the standard MCP pattern).
		objSig := &FuncSignature{
			inner: &tsFuncSig{
				Params: []tsFuncParam{
					{
						Name: "params",
						Type: &tsType{
							Kind: tsTypeObject,
							Properties: []tsProperty{
								{Name: "query", Schema: &tsType{Kind: tsTypePrimitive, PrimitiveType: "string"}},
							},
							Required: []string{"query"},
						},
					},
				},
			},
		}

		unionType := NewStringLiteralUnion([]string{"a", "b"})
		extended := objSig.AddParam("account", unionType, "pick one", false)

		// ParamsAsObject should show both the original query and the new account param.
		obj := extended.ParamsAsObject()
		if obj == nil {
			t.Fatal("expected non-nil ParamsAsObject")
		}

		schema := obj.ToJSONSchema()
		b, _ := json.Marshal(schema)
		t.Logf("ParamsAsObject schema: %s", b)

		propsMap, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("expected properties, got %v", schema)
		}
		if _, ok := propsMap["account"]; !ok {
			t.Fatal("expected account property in ParamsAsObject schema")
		}
	})

	t.Run("optional param excluded from required", func(t *testing.T) {
		unionType := NewStringLiteralUnion([]string{"x", "y"})
		extended := base.AddParam("opt_param", unionType, "optional", true)

		obj := extended.ParamsAsObject()
		schema := obj.ToJSONSchema()

		switch r := schema["required"].(type) {
		case []any:
			for _, v := range r {
				if v.(string) == "opt_param" {
					t.Fatal("optional param should not be in required")
				}
			}
		case []string:
			for _, v := range r {
				if v == "opt_param" {
					t.Fatal("optional param should not be in required")
				}
			}
		default:
			t.Fatalf("expected required array, got %T: %v", schema["required"], schema["required"])
		}
	})

	t.Run("nil receiver returns nil", func(t *testing.T) {
		var nilSig *FuncSignature
		got := nilSig.AddParam("x", nil, "desc", false)
		if got != nil {
			t.Fatal("expected nil from nil receiver")
		}
	})
}
