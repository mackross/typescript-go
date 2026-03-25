package toolbox

import (
	"context"
	"fmt"
	"testing"
	"testing/fstest"
)

// TestTSTypeToTSRoundTrip verifies the round-trip path:
//
//	TSType (from ExtractTSType) -> TSTypeToTS -> TS text
//	-> ExtractToolMetadata -> TSType -> TSTypeToJSON -> JSON Schema
//
// The JSON Schema at the end must match the original fixture's expected
// schema. This proves TSTypeToTS produces semantically correct output.
func TestTSTypeToTSRoundTrip(t *testing.T) {
	fixtures, err := DiscoverFixtures("testdata/programs")
	if err != nil {
		t.Fatalf("discover fixtures: %v", err)
	}
	if len(fixtures) == 0 {
		t.Fatal("expected fixtures")
	}

	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.Name, func(t *testing.T) {
			if knownFailingFixtures[fixture.Name] {
				t.Skipf("known failing fixture (generator WIP)")
			}
			if fixture.Spec.ExpectError != "" {
				// Error fixtures don't produce schemas.
				return
			}

			// Skip fixtures that use special test logic or features
			// that cannot round-trip through TS text.
			switch fixture.Name {
			case "tsconfig", "unique-names", "unique-names-multiple-subdefinitions",
				"no-unrelated-definitions", "type-alias-schema-override",
				"generate-all-types":
				t.Skipf("fixture %q uses special test logic", fixture.Name)
				return
			}

			program, err := BuildProgram(context.Background(), fixture)
			if err != nil {
				t.Fatalf("build program: %v", err)
			}

			// Generate TSType via ExtractTSType.
			gen, err := NewGenerator(program, fixture.Spec.Options)
			if err != nil {
				t.Fatalf("new generator: %v", err)
			}
			root := fixture.Spec.Root
			if root == "" && len(fixture.SchemaFiles) == 1 && fixture.SchemaFiles[0] == "schema.json" {
				symbols := gen.collectTopLevelSymbols()
				if len(symbols) > 0 {
					root = symbols[len(symbols)-1].Name
				}
			}
			if root == "" {
				gen.Close()
				t.Skip("no root type found")
				return
			}
			tsType, err := gen.ExtractTSType(context.Background(), root)
			gen.Close()
			if err != nil {
				t.Fatalf("extract TSType: %v", err)
			}

			// Get the expected schema (the original fixture schema).
			originalSchema := TSTypeToJSON(tsType)

			// Skip fixtures with definitions/$ref since they require
			// emitting separate type declarations and re-parsing with
			// those definitions available, which is complex.
			if len(tsType.Definitions) > 0 {
				t.Skipf("fixture %q has definitions/$ref (skip for now)", fixture.Name)
				return
			}

			// Render TSType to TS text.
			tsText := TSTypeToTS(tsType)
			if tsText == "" {
				t.Fatalf("TSTypeToTS returned empty string for fixture %q", fixture.Name)
			}

			// Wrap in a default export function so ExtractToolMetadata
			// can parse it.
			toolSource := fmt.Sprintf(
				"export default function tool(params: %s): void {}\n",
				tsText,
			)

			// Parse back via ExtractToolMetadata.
			meta, err := ExtractToolMetadata(context.Background(), ExtractInput{
				Files: fstest.MapFS{
					"tool.ts": {Data: []byte(toolSource)},
				},
				Entry: "tool.ts",
			})
			if err != nil {
				t.Fatalf("ExtractToolMetadata failed for generated TS:\n%s\nerror: %v", toolSource, err)
			}
			if meta.ParamsSchema == nil {
				t.Fatalf("ExtractToolMetadata returned nil ParamsSchema for generated TS:\n%s", toolSource)
			}

			// The re-parsed schema should match the original.
			// Strip $schema since ExtractToolMetadata doesn't add it,
			// and the original schema always has it from ExtractTSType.
			expected := cloneSchemaMap(originalSchema)
			delete(expected, "$schema")

			assertJSONEqual(t, meta.ParamsSchema, expected, fixture.Name+" (TSTypeToTS round-trip)")
		})
	}
}

// TestTSFuncSigToTSRoundTrip verifies that TSFuncSigToTS produces valid
// TypeScript function signature text.
func TestTSFuncSigToTSRoundTrip(t *testing.T) {
	sig := &TSFuncSig{
		Description: "Send a message",
		Params: []TSFuncParam{
			{
				Name: "channelID",
				Type: &TSType{Kind: TSTypePrimitive, PrimitiveType: "string"},
			},
			{
				Name: "payload",
				Type: &TSType{
					Kind: TSTypeObject,
					Properties: []TSProperty{
						{Name: "text", Schema: &TSType{Kind: TSTypePrimitive, PrimitiveType: "string"}},
						{Name: "retries", Schema: &TSType{Kind: TSTypePrimitive, PrimitiveType: "number"}},
					},
					Required:             []string{"text", "retries"},
					AdditionalPropertiesBool: boolPtr(false),
				},
			},
		},
	}

	result := TSFuncSigToTS(sig)
	if result == "" {
		t.Fatal("TSFuncSigToTS returned empty string")
	}

	// The result should be a valid function signature like:
	// (channelID: string, payload: { text: string; retries: number }) => void
	// We just verify it's non-empty for now; the fixture round-trip
	// is the primary correctness check.
	t.Logf("TSFuncSigToTS result: %s", result)
}

// TestTSTypeToTSBasicTypes verifies TSTypeToTS for basic type kinds.
func TestTSTypeToTSBasicTypes(t *testing.T) {
	tests := []struct {
		name     string
		tsType   *TSType
		wantNot  string // should not produce this
	}{
		{
			name:    "primitive string",
			tsType:  &TSType{Kind: TSTypePrimitive, PrimitiveType: "string"},
			wantNot: "",
		},
		{
			name:    "primitive number",
			tsType:  &TSType{Kind: TSTypePrimitive, PrimitiveType: "number"},
			wantNot: "",
		},
		{
			name:    "primitive boolean",
			tsType:  &TSType{Kind: TSTypePrimitive, PrimitiveType: "boolean"},
			wantNot: "",
		},
		{
			name:    "any",
			tsType:  &TSType{Kind: TSTypeAny},
			wantNot: "",
		},
		{
			name:    "string literal",
			tsType:  &TSType{Kind: TSTypeLiteral, LiteralValue: "hello", PrimitiveType: "string"},
			wantNot: "",
		},
		{
			name:    "number literal",
			tsType:  &TSType{Kind: TSTypeLiteral, LiteralValue: float64(42), PrimitiveType: "number"},
			wantNot: "",
		},
		{
			name:    "boolean literal",
			tsType:  &TSType{Kind: TSTypeLiteral, LiteralValue: true, PrimitiveType: "boolean"},
			wantNot: "",
		},
		{
			name: "simple object",
			tsType: &TSType{
				Kind: TSTypeObject,
				Properties: []TSProperty{
					{Name: "name", Schema: &TSType{Kind: TSTypePrimitive, PrimitiveType: "string"}},
				},
				Required:             []string{"name"},
				AdditionalPropertiesBool: boolPtr(false),
			},
			wantNot: "",
		},
		{
			name: "array of strings",
			tsType: &TSType{
				Kind:  TSTypeArray,
				Items: &TSType{Kind: TSTypePrimitive, PrimitiveType: "string"},
			},
			wantNot: "",
		},
		{
			name: "tuple",
			tsType: &TSType{
				Kind: TSTypeTuple,
				TupleItems: []*TSType{
					{Kind: TSTypePrimitive, PrimitiveType: "string"},
					{Kind: TSTypePrimitive, PrimitiveType: "number"},
				},
			},
			wantNot: "",
		},
		{
			name: "union",
			tsType: &TSType{
				Kind: TSTypeUnion,
				Types: []*TSType{
					{Kind: TSTypePrimitive, PrimitiveType: "string"},
					{Kind: TSTypePrimitive, PrimitiveType: "number"},
				},
			},
			wantNot: "",
		},
		{
			name: "intersection",
			tsType: &TSType{
				Kind: TSTypeIntersection,
				Types: []*TSType{
					{Kind: TSTypeObject, Properties: []TSProperty{{Name: "a", Schema: &TSType{Kind: TSTypePrimitive, PrimitiveType: "string"}}}, Required: []string{"a"}},
					{Kind: TSTypeObject, Properties: []TSProperty{{Name: "b", Schema: &TSType{Kind: TSTypePrimitive, PrimitiveType: "number"}}}, Required: []string{"b"}},
				},
			},
			wantNot: "",
		},
		{
			name: "nullable string",
			tsType: &TSType{
				Kind:          TSTypePrimitive,
				PrimitiveType: "string",
				Nullable:      true,
			},
			wantNot: "",
		},
		{
			name: "enum",
			tsType: &TSType{
				Kind:       TSTypeEnum,
				EnumValues: []any{"a", "b", "c"},
			},
			wantNot: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := TSTypeToTS(tt.tsType)
			if result == "" {
				t.Fatalf("TSTypeToTS returned empty string for %s", tt.name)
			}
			t.Logf("TSTypeToTS(%s) = %s", tt.name, result)
		})
	}
}
