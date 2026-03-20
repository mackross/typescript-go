package toolbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/checker"
)

func TestFunctionSignatureWithNestedArgCanBePrintedForSDK(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	source := `export function sendMessage(
  channelID: string,
  payload: {
    message: {
      text: string;
      metadata?: {
        urgent: boolean;
      };
    };
    retries: number;
  },
  mode?: "fast" | "slow",
): string {
  return channelID + payload.message.text + mode;
}`
	if err := os.WriteFile(filepath.Join(dir, "main.ts"), []byte(source), 0o644); err != nil {
		t.Fatalf("write main.ts: %v", err)
	}

	program, err := buildProgramForFixture(Fixture{
		Name: "sdk-signature",
		Path: dir,
	})
	if err != nil {
		t.Fatalf("build program: %v", err)
	}

	gen, err := NewGenerator(program, DefaultOptions())
	if err != nil {
		t.Fatalf("new generator: %v", err)
	}
	defer gen.Close()

	file := program.GetSourceFile(filepath.Join(dir, "main.ts"))
	if file == nil {
		t.Fatal("expected main.ts source file")
	}
	exports := gen.checker.GetExportsOfModule(file.AsNode().Symbol())
	var fnSymbol *ast.Symbol
	for _, export := range exports {
		if export.Name == "sendMessage" {
			fnSymbol = export
			break
		}
	}
	if fnSymbol == nil {
		t.Fatalf("expected sendMessage export, got %d exports", len(exports))
	}

	functionType := gen.checker.GetTypeOfSymbol(fnSymbol)
	signatures := gen.checker.GetSignaturesOfType(functionType, checker.SignatureKindCall)
	if len(signatures) != 1 {
		t.Fatalf("expected 1 call signature, got %d", len(signatures))
	}

	sig := signatures[0]
	gotSignature := gen.checker.SignatureToStringEx(
		sig,
		sig.Declaration(),
		checker.TypeFormatFlagsNoTruncation|checker.TypeFormatFlagsUseSingleQuotesForStringLiteralType,
	)

	gotParams := make([]string, 0, len(sig.Parameters()))
	for _, param := range sig.Parameters() {
		paramType := gen.checker.GetTypeOfSymbolAtLocation(param, sig.Declaration())
		gotParams = append(gotParams, fmt.Sprintf(
			"%s: %s",
			param.Name,
			gen.checker.TypeToStringEx(
				paramType,
				sig.Declaration(),
				checker.TypeFormatFlagsNoTruncation|checker.TypeFormatFlagsUseSingleQuotesForStringLiteralType,
			),
		))
	}

	wantParams := []string{
		"channelID: string",
		"payload: { message: { text: string; metadata?: { urgent: boolean; } | undefined; }; retries: number; }",
		"mode: 'fast' | 'slow' | undefined",
	}
	if strings.Join(gotParams, "\n") != strings.Join(wantParams, "\n") {
		t.Fatalf("unexpected parameter types\ngot:\n%s\n\nwant:\n%s", strings.Join(gotParams, "\n"), strings.Join(wantParams, "\n"))
	}

	wantFragments := []string{
		"channelID: string",
		"payload: {",
		"message: {",
		"text: string;",
		"metadata?: {",
		"urgent: boolean;",
		"retries: number;",
		"mode?: 'fast' | 'slow'",
		"): string",
	}
	for _, fragment := range wantFragments {
		if !strings.Contains(gotSignature, fragment) {
			t.Fatalf("signature %q missing fragment %q", gotSignature, fragment)
		}
	}
}

func TestFunctionSignatureWithNestedArgCanBePrintedForSDKSchema(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	source := `export function sendMessage(
  channelID: string,
  payload: {
    message: {
      text: string;
      metadata?: {
        urgent: boolean;
      };
    };
    retries: number;
  },
  mode?: "fast" | "slow",
): string {
  return channelID + payload.message.text + mode;
}`
	if err := os.WriteFile(filepath.Join(dir, "main.ts"), []byte(source), 0o644); err != nil {
		t.Fatalf("write main.ts: %v", err)
	}

	program, err := buildProgramForFixture(Fixture{
		Name: "sdk-signature",
		Path: dir,
	})
	if err != nil {
		t.Fatalf("build program: %v", err)
	}

	gen, err := NewGenerator(program, DefaultOptions())
	if err != nil {
		t.Fatalf("new generator: %v", err)
	}
	defer gen.Close()

	schema, err := gen.GenerateSchemaForName(context.Background(), "sendMessage")
	if err == nil {
		t.Fatalf("expected function schema generation to fail or be unsupported for now, got %#v", schema)
	}
}
