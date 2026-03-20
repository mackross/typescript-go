package toolbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestFixtureHarness(t *testing.T) {
	fixtures, err := DiscoverFixtures("testdata/programs")
	if err != nil {
		t.Fatalf("discover fixtures: %v", err)
	}
	if len(fixtures) == 0 {
		t.Fatal("expected copied schema fixtures")
	}

	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.Name, func(t *testing.T) {
			if fixture.Spec.ExpectError != "" {
				_, err := GenerateFixture(context.Background(), fixture)
				if err == nil {
					t.Fatalf("expected %q to fail with %q", fixture.Name, fixture.Spec.ExpectError)
				}
				if !strings.Contains(err.Error(), fixture.Spec.ExpectError) {
					t.Fatalf("expected %q error to contain %q, got %v", fixture.Name, fixture.Spec.ExpectError, err)
				}
				return
			}
			if fixture.Name == "tsconfig" {
				testTsconfigFixture(t, fixture)
				return
			}
			if fixture.Name == "unique-names" || fixture.Name == "unique-names-multiple-subdefinitions" {
				testUniqueNamesFixture(t, fixture)
				return
			}
			if fixture.Name == "no-unrelated-definitions" {
				testNoUnrelatedDefinitions(t, fixture)
				return
			}
			if fixture.Name == "type-alias-schema-override" {
				testTypeAliasSchemaOverride(t, fixture)
				return
			}

			result, err := GenerateFixture(context.Background(), fixture)
			if err != nil {
				t.Fatalf("generate fixture: %v", err)
			}

			expected := expectedSchemasForFixture(t, fixture)
			if len(expected) != len(result.Schemas) {
				t.Fatalf("expected %d schemas, got %d", len(expected), len(result.Schemas))
			}

			for name, expectedSchema := range expected {
				actualSchema, ok := result.Schemas[name]
				if !ok {
					t.Fatalf("missing generated schema %q", name)
				}
				assertJSONEqual(t, actualSchema, expectedSchema, name)
			}
		})
	}
}

func testTypeAliasSchemaOverride(t *testing.T, fixture Fixture) {
	t.Helper()
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
	actual, err := gen.GenerateSchemaForName(context.Background(), fixture.Spec.Root)
	if err != nil {
		t.Fatalf("generate schema: %v", err)
	}
	expected := readCanonicalJSON(t, filepath.Join(fixture.Path, "schema.json"))
	assertJSONEqual(t, actual, expected, "schema.json")
}

func testTsconfigFixture(t *testing.T, fixture Fixture) {
	t.Helper()
	program, err := BuildProgram(context.Background(), fixture)
	if err != nil {
		t.Fatalf("build program: %v", err)
	}
	gen, err := NewGenerator(program, fixture.Spec.Options)
	if err != nil {
		t.Fatalf("new generator: %v", err)
	}
	defer gen.Close()

	got := gen.GetSymbols("IncludedAlways")
	if len(got) == 0 {
		t.Fatal("expected IncludedAlways symbol")
	}
	if len(gen.GetSymbols("IncludedOnlyByTsConfig")) == 0 {
		t.Fatal("expected IncludedOnlyByTsConfig symbol")
	}
	if len(gen.GetSymbols("Excluded")) != 0 {
		t.Fatal("did not expect Excluded symbol")
	}
}

func testNoUnrelatedDefinitions(t *testing.T, fixture Fixture) {
	t.Helper()
	program, err := BuildProgram(context.Background(), fixture)
	if err != nil {
		t.Fatalf("build program: %v", err)
	}
	gen, err := NewGenerator(program, fixture.Spec.Options)
	if err != nil {
		t.Fatalf("new generator: %v", err)
	}
	defer gen.Close()

	check := func(symbolName, expectedFile string) {
		t.Helper()
		actual, err := gen.GenerateSchemaForName(context.Background(), symbolName)
		if err != nil {
			t.Fatalf("generate %s: %v", symbolName, err)
		}
		expected := readCanonicalJSON(t, filepath.Join(fixture.Path, expectedFile))
		assertJSONEqual(t, actual, expected, expectedFile)
	}

	check("MyObject", "schema.MyObject.json")
	check("MyOtherObject", "schema.MyOtherObject.json")

	gen.SetSchemaOverride("SomeOtherDefinition", Schema{"type": "string"})
	check("MyOtherObject", "schema.MyOtherObjectWithOverride.json")

	gen.SetSchemaOverride("UnrelatedDefinition", Schema{"type": "string"})
	actual, err := gen.GenerateProgramSchema()
	if err != nil {
		t.Fatalf("generate program schema: %v", err)
	}
	expected := readCanonicalJSON(t, filepath.Join(fixture.Path, "schema.program.json"))
	assertJSONEqual(t, actual, expected, "schema.program.json")
}

func testUniqueNamesFixture(t *testing.T, fixture Fixture) {
	t.Helper()
	program, err := BuildProgram(context.Background(), fixture)
	if err != nil {
		t.Fatalf("build program: %v", err)
	}
	gen, err := NewGenerator(program, fixture.Spec.Options)
	if err != nil {
		t.Fatalf("new generator: %v", err)
	}
	defer gen.Close()

	symbols := gen.GetSymbols("MyObject")
	if len(symbols) == 0 {
		t.Fatal("expected MyObject symbols")
	}
	expected := expectedSchemasForFixture(t, fixture)
	if len(expected) != len(symbols) {
		t.Fatalf("expected %d schemas, got %d", len(expected), len(symbols))
	}
	for _, ref := range symbols {
		actual, err := gen.generateSchemaForSymbol(ref.Symbol, true)
		if err != nil {
			t.Fatalf("generate %s: %v", ref.Name, err)
		}
		key := "schema." + ref.Name + ".json"
		if ref.Name == "MyObject" && len(symbols) == 1 {
			key = "schema.json"
		}
		expectedSchema, ok := expected[key]
		if !ok {
			t.Fatalf("missing expected schema %q", key)
		}
		assertJSONEqual(t, actual, expectedSchema, key)
	}
}

func expectedSchemasForFixture(t *testing.T, fixture Fixture) map[string]any {
	t.Helper()
	out := map[string]any{}
	for _, file := range fixture.SchemaFiles {
		out[file] = readCanonicalJSON(t, filepath.Join(fixture.Path, file))
	}
	return out
}

func readCanonicalJSON(t *testing.T, path string) any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return canonicalizeJSON(v)
}

func assertJSONEqual(t *testing.T, actual, expected any, label string) {
	t.Helper()
	a := canonicalizeJSON(actual)
	e := canonicalizeJSON(expected)
	if !equalJSON(a, e) {
		ab, _ := json.MarshalIndent(a, "", "  ")
		eb, _ := json.MarshalIndent(e, "", "  ")
		t.Fatalf("%s mismatch\nactual:\n%s\nexpected:\n%s", label, string(ab), string(eb))
	}
}

func canonicalizeJSON(v any) any {
	return canonicalizeJSONKey("", v)
}

func canonicalizeJSONKey(key string, v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out[k] = canonicalizeJSONKey(k, x[k])
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = canonicalizeJSONKey("", e)
		}
		if key == "required" || key == "enum" || key == "type" {
			sort.Slice(out, func(i, j int) bool {
				return jsonValueOrder(out[i]) < jsonValueOrder(out[j])
			})
		}
		return out
	case []string:
		out := make([]string, len(x))
		copy(out, x)
		if key == "required" || key == "enum" || key == "type" {
			sort.Strings(out)
		}
		return out
	default:
		return v
	}
}

func jsonValueOrder(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func equalJSON(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}
