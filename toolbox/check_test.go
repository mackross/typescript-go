package toolbox_test

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/microsoft/typescript-go/toolbox"
)

var checkTestFiles = fstest.MapFS{
	"github.com/example/calc/tools/calc.add.ts": {
		Data: []byte(`export function execute(params: { a: number; b: number }, ctx: unknown) {
  return String(params.a + params.b)
}`),
	},
}

func TestCheckReportsArgumentTypeMismatch(t *testing.T) {
	files := fstest.MapFS{}
	for k, v := range checkTestFiles {
		files[k] = v
	}
	files["__toolbox_run.ts"] = &fstest.MapFile{
		Data: []byte(`import { execute as add } from "./github.com/example/calc/tools/calc.add.ts";
export default add({"a":"6","b":3}, {});`),
	}

	diagnostics, session, err := toolbox.Check(context.Background(), toolbox.CheckInput{
		Files: files,
		Entry: "__toolbox_run.ts",
	}, nil)
	if err != nil {
		t.Fatalf("expected no checker error, got %v", err)
	}
	defer session.Close()
	if len(diagnostics) == 0 {
		t.Fatalf("expected diagnostics")
	}
	if !strings.Contains(diagnostics[0].Message, "string") {
		t.Fatalf("expected string type mismatch, got %q", diagnostics[0].Message)
	}
}

func TestCheckSessionReuse(t *testing.T) {
	// First call with bad args — should produce diagnostics.
	badFiles := fstest.MapFS{}
	for k, v := range checkTestFiles {
		badFiles[k] = v
	}
	badFiles["__toolbox_run.ts"] = &fstest.MapFile{
		Data: []byte(`import { execute as add } from "./github.com/example/calc/tools/calc.add.ts";
export default add({"a":"not_a_number","b":3}, {});`),
	}

	diagnostics, session, err := toolbox.Check(context.Background(), toolbox.CheckInput{
		Files: badFiles,
		Entry: "__toolbox_run.ts",
	}, nil)
	if err != nil {
		t.Fatalf("first check: %v", err)
	}
	defer session.Close()
	if len(diagnostics) == 0 {
		t.Fatal("expected diagnostics for bad args")
	}

	// Second call with correct args, reusing session — should pass.
	goodFiles := fstest.MapFS{}
	for k, v := range checkTestFiles {
		goodFiles[k] = v
	}
	goodFiles["__toolbox_run.ts"] = &fstest.MapFile{
		Data: []byte(`import { execute as add } from "./github.com/example/calc/tools/calc.add.ts";
export default add({"a":6,"b":3}, {});`),
	}

	diagnostics, session, err = toolbox.Check(context.Background(), toolbox.CheckInput{
		Files: goodFiles,
		Entry: "__toolbox_run.ts",
	}, session)
	if err != nil {
		t.Fatalf("second check: %v", err)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("expected no diagnostics for good args, got %v", diagnostics)
	}
}

func TestReplCellCheck_ReportsRuntimeNamespaceDeclarations(t *testing.T) {
	files := fstest.MapFS{
		"__toolbox_run.ts": &fstest.MapFile{
			Data: []byte(`export {};
declare namespace TypesOnly { export const value: number }
namespace RuntimeNS { export const value = 1 }
module RuntimeModule { export const value = 2 }
`),
		},
	}

	result, session, err := toolbox.ReplCellCheck(context.Background(), toolbox.CheckInput{
		Files: files,
		Entry: "__toolbox_run.ts",
	}, nil)
	if err != nil {
		t.Fatalf("ReplCellCheck: %v", err)
	}
	defer session.Close()

	if len(result.UnsupportedSyntax) != 2 {
		t.Fatalf("unsupported syntax len = %d, want 2", len(result.UnsupportedSyntax))
	}
	if result.UnsupportedSyntax[0].Kind != "namespace" {
		t.Fatalf("first kind = %q, want namespace", result.UnsupportedSyntax[0].Kind)
	}
	if result.UnsupportedSyntax[1].Kind != "module" {
		t.Fatalf("second kind = %q, want module", result.UnsupportedSyntax[1].Kind)
	}
}

func BenchmarkCheck(b *testing.B) {
	files := fstest.MapFS{
		"tools/calc.add.ts": &fstest.MapFile{Data: []byte(`
export const params = { a: { type: "number" }, b: { type: "number" } };
export default function tool(params: { a: number; b: number }) {
  return String(params.a + params.b);
}
`)},
		"__toolbox_run.ts": &fstest.MapFile{Data: []byte(`
import tool from "./tools/calc.add.ts";
export default await tool({"a": 7, "b": 4});
`)},
	}

	input := toolbox.CheckInput{
		Files: files,
		Entry: "__toolbox_run.ts",
	}

	b.Run("Cold", func(b *testing.B) {
		for range b.N {
			_, session, err := toolbox.Check(context.Background(), input, nil)
			if err != nil {
				b.Fatal(err)
			}
			session.Close()
		}
	})

	b.Run("Warm", func(b *testing.B) {
		_, session, err := toolbox.Check(context.Background(), input, nil)
		if err != nil {
			b.Fatal(err)
		}
		defer session.Close()

		b.ResetTimer()
		for range b.N {
			_, session, err = toolbox.Check(context.Background(), input, session)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}
