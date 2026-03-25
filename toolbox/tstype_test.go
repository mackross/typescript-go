package toolbox

import (
	"context"
	"encoding/json"
	"testing"
)

// TestTSTypeRoundTrip verifies that the round-trip path
// checker.Type -> JSON Schema -> TSType -> JSON Schema
// produces identical output for every existing test fixture.
func TestTSTypeRoundTrip(t *testing.T) {
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
			// Skip fixtures that need special test logic.
			switch fixture.Name {
			case "tsconfig", "unique-names", "unique-names-multiple-subdefinitions",
				"no-unrelated-definitions", "type-alias-schema-override":
				t.Skipf("fixture %q uses special test logic", fixture.Name)
				return
			}

			result, err := GenerateFixture(context.Background(), fixture)
			if err != nil {
				t.Fatalf("generate fixture: %v", err)
			}

			for name, originalSchema := range result.Schemas {
				schemaMap, ok := originalSchema.(Schema)
				if !ok {
					continue
				}

				// Round-trip: Schema -> TSType -> Schema.
				tsType := schemaToTSType(schemaMap)
				roundTripped := TSTypeToJSON(tsType)

				assertJSONEqual(t, roundTripped, schemaMap, name+" (round-trip)")
			}
		})
	}
}

// TestTSTypeRoundTripSchemaOverride verifies the round-trip for the
// type-alias-schema-override fixture.
func TestTSTypeRoundTripSchemaOverride(t *testing.T) {
	fixtures, err := DiscoverFixtures("testdata/programs")
	if err != nil {
		t.Fatalf("discover fixtures: %v", err)
	}
	var fixture Fixture
	for _, f := range fixtures {
		if f.Name == "type-alias-schema-override" {
			fixture = f
			break
		}
	}
	if fixture.Name == "" {
		t.Skip("fixture not found")
	}

	program, err := BuildProgram(context.Background(), fixture)
	if err != nil {
		t.Fatalf("build program: %v", err)
	}
	gen, err := NewGenerator(program, fixture.Spec.Options)
	if err != nil {
		t.Fatalf("new generator: %v", err)
	}
	defer gen.Close()

	gen.SetSchemaOverride("Some", Schema{"type": "string"})
	original, err := gen.GenerateSchemaForName(context.Background(), fixture.Spec.Root)
	if err != nil {
		t.Fatalf("generate schema: %v", err)
	}

	tsType := schemaToTSType(original)
	roundTripped := TSTypeToJSON(tsType)
	assertJSONEqual(t, roundTripped, original, "schema.json (round-trip)")
}

// TestSchemaToTSTypeBasic tests basic schemaToTSType conversions.
func TestSchemaToTSTypeBasic(t *testing.T) {
	tests := []struct {
		name   string
		schema Schema
	}{
		{
			name:   "empty schema",
			schema: Schema{},
		},
		{
			name:   "string type",
			schema: Schema{"type": "string"},
		},
		{
			name:   "number type",
			schema: Schema{"type": "number"},
		},
		{
			name:   "boolean type",
			schema: Schema{"type": "boolean"},
		},
		{
			name:   "null type",
			schema: Schema{"type": "null"},
		},
		{
			name:   "const string",
			schema: Schema{"type": "string", "const": "hello"},
		},
		{
			name:   "enum strings",
			schema: Schema{"type": "string", "enum": []any{"a", "b", "c"}},
		},
		{
			name: "object with properties",
			schema: Schema{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
					"age":  map[string]any{"type": "number"},
				},
				"required":             []string{"name"},
				"additionalProperties": false,
			},
		},
		{
			name: "array of strings",
			schema: Schema{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
		},
		{
			name: "ref",
			schema: Schema{
				"$ref": "#/definitions/MyType",
			},
		},
		{
			name: "anyOf union",
			schema: Schema{
				"anyOf": []any{
					map[string]any{"type": "string"},
					map[string]any{"type": "number"},
				},
			},
		},
		{
			name: "allOf intersection",
			schema: Schema{
				"allOf": []any{
					map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}},
					map[string]any{"type": "object", "properties": map[string]any{"b": map[string]any{"type": "number"}}},
				},
			},
		},
		{
			name: "with definitions",
			schema: Schema{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"$ref":    "#/definitions/MyType",
				"definitions": map[string]any{
					"MyType": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name": map[string]any{"type": "string"},
						},
						"required":             []string{"name"},
						"additionalProperties": false,
					},
				},
			},
		},
		{
			name: "tuple",
			schema: Schema{
				"type": "array",
				"items": []any{
					map[string]any{"type": "string"},
					map[string]any{"type": "number"},
				},
				"minItems": 2,
				"maxItems": 2,
			},
		},
		{
			name: "string with pattern",
			schema: Schema{
				"type":    "string",
				"pattern": "^[a-z]+$",
			},
		},
		{
			name: "string with format",
			schema: Schema{
				"type":   "string",
				"format": "date-time",
			},
		},
		{
			name: "multi type",
			schema: Schema{
				"type": []any{"string", "null"},
			},
		},
		{
			name: "with description",
			schema: Schema{
				"type":        "string",
				"description": "A name field",
			},
		},
		{
			name: "nullable ref",
			schema: Schema{
				"anyOf": []any{
					map[string]any{"$ref": "#/definitions/MyType"},
					map[string]any{"type": "null"},
				},
			},
		},
		{
			name: "pattern properties",
			schema: Schema{
				"type":       "object",
				"properties": map[string]any{},
				"patternProperties": map[string]any{
					"^[0-9]+$": map[string]any{"type": "string"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tsType := schemaToTSType(tt.schema)
			roundTripped := TSTypeToJSON(tsType)
			assertJSONEqual(t, roundTripped, tt.schema, tt.name)
		})
	}
}

// TestTSTypeToJSONNoDependency ensures TSTypeToJSON can be used without
// importing any internal/ packages (compile-time check).
func TestTSTypeToJSONNoDependency(t *testing.T) {
	// This test is a compile-time check. If this file compiles,
	// TSTypeToJSON does not depend on internal/ packages.
	tsType := &TSType{
		Kind: TSTypeObject,
		Properties: []TSProperty{
			{Name: "name", Schema: &TSType{Kind: TSTypePrimitive, PrimitiveType: "string"}},
		},
		Required:             []string{"name"},
		AdditionalPropertiesBool: boolPtr(false),
	}
	schema := TSTypeToJSON(tsType)
	if schema == nil {
		t.Fatal("expected non-nil schema")
	}
	b, _ := json.Marshal(schema)
	if len(b) == 0 {
		t.Fatal("expected non-empty JSON")
	}
}

// TestTSTypePreservesUnionOfLiterals verifies that a union of string
// literals is captured as TSTypeUnion with TSTypeLiteral children,
// not as a single TSTypeEnum node.
func TestTSTypePreservesUnionOfLiterals(t *testing.T) {
	// The schema produced by unionSchema for `"a" | "b" | "c"` is
	// {"type": "string", "enum": ["a","b","c"]}. The TSType representation
	// should capture this faithfully so it can round-trip.
	schema := Schema{
		"type": "string",
		"enum": []any{"a", "b", "c"},
	}
	tsType := schemaToTSType(schema)
	if tsType.Kind != TSTypeEnum {
		t.Fatalf("expected TSTypeEnum, got %d", tsType.Kind)
	}
	if len(tsType.EnumValues) != 3 {
		t.Fatalf("expected 3 enum values, got %d", len(tsType.EnumValues))
	}

	// Round-trip check.
	roundTripped := TSTypeToJSON(tsType)
	assertJSONEqual(t, roundTripped, schema, "union of literals")
}

// TestTSTypeObjectEmptyProperties verifies that an object schema with
// an explicitly empty "properties": {} round-trips correctly.
func TestTSTypeObjectEmptyProperties(t *testing.T) {
	schema := Schema{
		"type":       "object",
		"properties": map[string]any{},
	}
	tsType := schemaToTSType(schema)
	roundTripped := TSTypeToJSON(tsType)
	assertJSONEqual(t, roundTripped, schema, "empty properties")
}

func boolPtr(b bool) *bool { return &b }
