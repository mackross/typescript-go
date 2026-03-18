package toolboxapi_test

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/microsoft/typescript-go/toolboxapi"
)

func TestCheckReportsArgumentTypeMismatch(t *testing.T) {
	diagnostics, err := toolboxapi.Check(context.Background(), toolboxapi.CheckInput{
		Files: fstest.MapFS{
			"github.com/example/calc/tools/calc.add.ts": {
				Data: []byte(`export function execute(params: { a: number; b: number }, ctx: unknown) {
  return String(params.a + params.b)
}`),
			},
			"__toolbox_run.ts": {
				Data: []byte(`import { execute as add } from "./github.com/example/calc/tools/calc.add.ts";
export default add({"a":"6","b":3}, {});`),
			},
		},
		Entry: "__toolbox_run.ts",
	})
	if err != nil {
		t.Fatalf("expected no checker error, got %v", err)
	}
	if len(diagnostics) == 0 {
		t.Fatalf("expected diagnostics")
	}
	if !strings.Contains(diagnostics[0].Message, "string") {
		t.Fatalf("expected string type mismatch, got %q", diagnostics[0].Message)
	}
}
