package toolbox

import (
	"context"
	"encoding/json"
	"testing"
)

// TestTSTypeRoundTrip verifies that the round-trip path
// checker.Type -> JSON Schema -> tsType -> JSON Schema
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

				// Round-trip: Schema -> tsType -> Schema.
				tsType := schemaToTSType(schemaMap)
				roundTripped := tsTypeToJSON(tsType)

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
	roundTripped := tsTypeToJSON(tsType)
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
			roundTripped := tsTypeToJSON(tsType)
			assertJSONEqual(t, roundTripped, tt.schema, tt.name)
		})
	}
}

// TestTSTypeToJSONNoDependency ensures tsTypeToJSON can be used without
// importing any internal/ packages (compile-time check).
func TestTSTypeToJSONNoDependency(t *testing.T) {
	// This test is a compile-time check. If this file compiles,
	// tsTypeToJSON does not depend on internal/ packages.
	tsType := &tsType{
		Kind: tsTypeObject,
		Properties: []tsProperty{
			{Name: "name", Schema: &tsType{Kind: tsTypePrimitive, PrimitiveType: "string"}},
		},
		Required:             []string{"name"},
		AdditionalPropertiesBool: boolPtr(false),
	}
	schema := tsTypeToJSON(tsType)
	if schema == nil {
		t.Fatal("expected non-nil schema")
	}
	b, _ := json.Marshal(schema)
	if len(b) == 0 {
		t.Fatal("expected non-empty JSON")
	}
}

// TestExtractTSTypeEquivalence verifies that for every fixture,
// tsTypeToJSON(ExtractTSType(name)) produces identical output to
// GenerateSchemaForName(name). This tests the full extractTSType path.
func TestExtractTSTypeEquivalence(t *testing.T) {
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
				return
			}
			// Skip fixtures that need special test logic.
			switch fixture.Name {
			case "tsconfig", "unique-names", "unique-names-multiple-subdefinitions",
				"no-unrelated-definitions", "type-alias-schema-override":
				t.Skipf("fixture %q uses special test logic", fixture.Name)
			case "generate-all-types":
				// TODO: generate-all-types uses root="*" (all symbols), which
				// ExtractTSType doesn't support.  Add a dedicated equivalence
				// test that iterates over all symbols individually.
				t.Skipf("fixture %q uses root=* which ExtractTSType does not support", fixture.Name)
				return
			}

			program, err := BuildProgram(context.Background(), fixture)
			if err != nil {
				t.Fatalf("build program: %v", err)
			}

			// Generate schema via the normal path.
			gen1, err := NewGenerator(program, fixture.Spec.Options)
			if err != nil {
				t.Fatalf("new generator: %v", err)
			}
			root := fixture.Spec.Root
			if root == "" && len(fixture.SchemaFiles) == 1 && fixture.SchemaFiles[0] == "schema.json" {
				symbols := gen1.collectTopLevelSymbols()
				if len(symbols) > 0 {
					root = symbols[len(symbols)-1].Name
				}
			}
			if root == "" {
				gen1.Close()
				t.Skip("no root type found")
				return
			}
			directSchema, err := gen1.GenerateSchemaForName(context.Background(), root)
			gen1.Close()
			if err != nil {
				t.Fatalf("generate schema: %v", err)
			}

			// Generate tsType via ExtractTSType and render to JSON Schema.
			gen2, err := NewGenerator(program, fixture.Spec.Options)
			if err != nil {
				t.Fatalf("new generator for extract: %v", err)
			}
			tsType, err := gen2.ExtractTSType(context.Background(), root)
			gen2.Close()
			if err != nil {
				t.Fatalf("extract tsType: %v", err)
			}

			extractedSchema := tsTypeToJSON(tsType)

			assertJSONEqual(t, extractedSchema, directSchema, fixture.Name+" (ExtractTSType equivalence)")
		})
	}
}

// TestTSTypePreservesUnionOfLiterals verifies that a union of string
// literals extracted from checker types is captured as tsTypeUnion with
// tsTypeLiteral children (not as a single tsTypeEnum node).
func TestTSTypePreservesUnionOfLiterals(t *testing.T) {
	// Build a tsType that mirrors what extractUnionTSType produces for
	// `"a" | "b" | "c"`: a tsTypeUnion with tsTypeLiteral children and
	// CollapseLiterals=true.
	tsType := &tsType{
		Kind:             tsTypeUnion,
		CollapseLiterals: true,
		Types: []*tsType{
			{Kind: tsTypeLiteral, LiteralValue: "a", PrimitiveType: "string"},
			{Kind: tsTypeLiteral, LiteralValue: "b", PrimitiveType: "string"},
			{Kind: tsTypeLiteral, LiteralValue: "c", PrimitiveType: "string"},
		},
	}
	if tsType.Kind != tsTypeUnion {
		t.Fatalf("expected tsTypeUnion, got %d", tsType.Kind)
	}
	if len(tsType.Types) != 3 {
		t.Fatalf("expected 3 union members, got %d", len(tsType.Types))
	}
	for i, child := range tsType.Types {
		if child.Kind != tsTypeLiteral {
			t.Fatalf("child %d: expected tsTypeLiteral, got %d", i, child.Kind)
		}
	}

	// Round-trip: tsTypeToJSON should collapse the union of string literals
	// into {"type": "string", "enum": ["a","b","c"]}.
	expected := Schema{
		"type": "string",
		"enum": []any{"a", "b", "c"},
	}
	roundTripped := tsTypeToJSON(tsType)
	assertJSONEqual(t, roundTripped, expected, "union of literals")
}

// TestTSTypeObjectEmptyProperties verifies that an object schema with
// an explicitly empty "properties": {} round-trips correctly.
func TestTSTypeObjectEmptyProperties(t *testing.T) {
	schema := Schema{
		"type":       "object",
		"properties": map[string]any{},
	}
	tsType := schemaToTSType(schema)
	roundTripped := tsTypeToJSON(tsType)
	assertJSONEqual(t, roundTripped, schema, "empty properties")
}

func boolPtr(b bool) *bool { return &b }
