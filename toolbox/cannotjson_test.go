package toolbox_test

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/microsoft/typescript-go/toolbox"
)

func TestTSTypeCannotJSON(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		source      string
		wantReasons []string
	}{
		{
			name: "any",
			source: `export default function tool(input: any) {
  return input;
}`,
			wantReasons: []string{"uses any"},
		},
		{
			name: "unknown",
			source: `export default function tool(input: unknown) {
  return input;
}`,
			wantReasons: []string{"uses unknown"},
		},
		{
			name: "explicit undefined union",
			source: `export default function tool(input: string | undefined) {
  return input;
}`,
			wantReasons: []string{"uses undefined"},
		},
		{
			name: "date",
			source: `export default function tool(input: Date) {
  return input.toISOString();
}`,
			wantReasons: []string{"uses Date"},
		},
		{
			name: "promise",
			source: `export default function tool(input: Promise<string>) {
  return input;
}`,
			wantReasons: []string{"uses Promise"},
		},
		{
			name: "map",
			source: `export default function tool(input: Map<string, number>) {
  return input.size;
}`,
			wantReasons: []string{"uses Map"},
		},
		{
			name: "set",
			source: `export default function tool(input: Set<string>) {
  return input.size;
}`,
			wantReasons: []string{"uses Set"},
		},
		{
			name: "weakmap",
			source: `export default function tool(input: WeakMap<object, string>) {
  return input;
}`,
			wantReasons: []string{"uses WeakMap"},
		},
		{
			name: "weakset",
			source: `export default function tool(input: WeakSet<object>) {
  return input;
}`,
			wantReasons: []string{"uses WeakSet"},
		},
		{
			name: "arraybuffer",
			source: `export default function tool(input: ArrayBuffer) {
  return input.byteLength;
}`,
			wantReasons: []string{"uses ArrayBuffer"},
		},
		{
			name: "dataview",
			source: `export default function tool(input: DataView) {
  return input.byteLength;
}`,
			wantReasons: []string{"uses DataView"},
		},
		{
			name: "typed array",
			source: `export default function tool(input: Uint8Array) {
  return input.byteLength;
}`,
			wantReasons: []string{"uses Uint8Array"},
		},
		{
			name: "regexp",
			source: `export default function tool(input: RegExp) {
  return input.source;
}`,
			wantReasons: []string{"uses RegExp"},
		},
		{
			name: "error",
			source: `export default function tool(input: Error) {
  return input.message;
}`,
			wantReasons: []string{"uses Error"},
		},
		{
			name: "bigint",
			source: `export default function tool(input: bigint) {
  return input;
}`,
			wantReasons: []string{"uses bigint"},
		},
		{
			name: "symbol",
			source: `export default function tool(input: symbol) {
  return input;
}`,
			wantReasons: []string{"uses symbol"},
		},
		{
			name: "function type",
			source: `export default function tool(input: (value: number) => string) {
  return input(1);
}`,
			wantReasons: []string{"uses function type"},
		},
		{
			name: "open object",
			source: `export default function tool(input: object) {
  return input;
}`,
			wantReasons: []string{"uses object", "uses open object"},
		},
		{
			name: "record unknown",
			source: `export default function tool(input: Record<string, unknown>) {
  return input;
}`,
			wantReasons: []string{"* uses unknown"},
		},
		{
			name: "record any",
			source: `export default function tool(input: { [key: string]: any }) {
  return input;
}`,
			wantReasons: []string{"* uses any"},
		},
		{
			name: "nested any property",
			source: `export default function tool(input: { meta: { payload: any } }) {
  return input;
}`,
			wantReasons: []string{"meta.payload uses any"},
		},
		{
			name: "nested unknown property",
			source: `export default function tool(input: { meta: { payload: unknown } }) {
  return input;
}`,
			wantReasons: []string{"meta.payload uses unknown"},
		},
		{
			name: "nested undefined property",
			source: `export default function tool(input: { meta: { payload: string | undefined } }) {
  return input;
}`,
			wantReasons: []string{"meta.payload uses undefined"},
		},
		{
			name: "nested bigint property",
			source: `export default function tool(input: { meta: { amount: bigint } }) {
  return input;
}`,
			wantReasons: []string{"meta.amount uses bigint"},
		},
		{
			name: "nested symbol property",
			source: `export default function tool(input: { meta: { token: symbol } }) {
  return input;
}`,
			wantReasons: []string{"meta.token uses symbol"},
		},
		{
			name: "array nested unknown values",
			source: `export default function tool(input: Array<{ payload: unknown }>) {
  return input;
}`,
			wantReasons: []string{"[].payload uses unknown"},
		},
		{
			name: "tuple nested bigint values",
			source: `export default function tool(input: [string, { amount: bigint }]) {
  return input;
}`,
			wantReasons: []string{"[1].amount uses bigint"},
		},
		{
			name: "nested bad property path",
			source: `export default function tool(input: { meta: { when: Date } }) {
  return input;
}`,
			wantReasons: []string{"meta.when uses Date"},
		},
		{
			name: "tuple bad element path",
			source: `export default function tool(input: [string, Date]) {
  return input;
}`,
			wantReasons: []string{"[1] uses Date"},
		},
		{
			name: "array of bad values",
			source: `export default function tool(input: Array<Map<string, string>>) {
  return input;
}`,
			wantReasons: []string{"[] uses Map"},
		},
		{
			name: "union with bad branch",
			source: `export default function tool(input: string | Date) {
  return input;
}`,
			wantReasons: []string{"uses Date"},
		},
		{
			name: "required property with explicit undefined",
			source: `export default function tool(input: { maybe: string | undefined }) {
  return input.maybe ?? "";
}`,
			wantReasons: []string{"maybe uses undefined"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reasons := extractParamType(t, tc.source).CannotJSON()
			if len(reasons) == 0 {
				t.Fatal("CannotJSON() = nil, want reason")
			}
			joined := strings.Join(reasons, "; ")
			for _, want := range tc.wantReasons {
				if !strings.Contains(joined, want) {
					t.Fatalf("CannotJSON() = %#v, want substring %q", reasons, want)
				}
			}
		})
	}
}

func TestTSTypeCannotJSONAllowsJSONFriendlyShapes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		source string
	}{
		{
			name: "primitive object",
			source: `export default function tool(input: {
  subject: string;
  count: number;
  ok: boolean;
  retryAt: null | string;
}) {
  return input.subject;
}`,
		},
		{
			name: "optional property",
			source: `export default function tool(input: { maybe?: string }) {
  return input.maybe ?? "";
}`,
		},
		{
			name: "nested arrays and tuples",
			source: `export default function tool(input: {
  rows: Array<{ values: [string, number | null] }>;
}) {
  return input.rows.length;
}`,
		},
		{
			name: "record of json object",
			source: `export default function tool(input: Record<string, { count: number | null }>) {
  return input;
}`,
		},
		{
			name: "string literal union",
			source: `export default function tool(input: "open" | "closed") {
  return input;
}`,
		},
		{
			name: "string enum",
			source: `enum Status {
  Open = "open",
  Closed = "closed",
}

export default function tool(input: Status) {
  return input;
}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if reasons := extractParamType(t, tc.source).CannotJSON(); len(reasons) != 0 {
				t.Fatalf("CannotJSON() = %#v, want nil", reasons)
			}
		})
	}
}

func TestTSTypeCannotJSONFunctionValuedPropertyReportsFunctionType(t *testing.T) {
	t.Parallel()

	reasons := extractParamType(t, `export default function tool(input: { cb: () => void }) {
  return input;
}`).CannotJSON()
	if len(reasons) == 0 {
		t.Fatal("CannotJSON() = nil, want reason")
	}
	joined := strings.Join(reasons, "; ")
	if !strings.Contains(joined, "cb uses function type") {
		t.Fatalf("CannotJSON() = %#v, want substring %q", reasons, "cb uses function type")
	}
	if strings.Contains(joined, "cb uses any") {
		t.Fatalf("CannotJSON() = %#v, want function-type reason instead of any", reasons)
	}
}

func TestTSTypeCannotJSONTypeOfFunctionPropertyReportsFunctionType(t *testing.T) {
	t.Parallel()

	meta, err := toolbox.ExtractToolMetadata(context.Background(), toolbox.ExtractInput{
		Files: fstest.MapFS{
			"main.ts": {Data: []byte(`interface Input {
  cb: () => void;
}

export default function tool(input: Input) {
  return input;
}`)},
		},
		Entry: "main.ts",
	})
	if err != nil {
		t.Fatalf("ExtractToolMetadata: %v", err)
	}
	if meta.ParamsType == nil {
		t.Fatal("ParamsType = nil")
	}
	reasons := meta.ParamsType.CannotJSON()
	if len(reasons) == 0 {
		t.Fatal("CannotJSON() = nil, want reason")
	}
	joined := strings.Join(reasons, "; ")
	if !strings.Contains(joined, "cb uses function type") {
		t.Fatalf("CannotJSON() = %#v, want substring %q", reasons, "cb uses function type")
	}
	if strings.Contains(joined, "cb uses any") {
		t.Fatalf("CannotJSON() = %#v, want function-type reason instead of any", reasons)
	}
}

func extractParamType(t *testing.T, source string) *toolbox.TSType {
	t.Helper()

	meta, err := toolbox.ExtractToolMetadata(context.Background(), toolbox.ExtractInput{
		Files: fstest.MapFS{
			"main.ts": {Data: []byte(source)},
		},
		Entry: "main.ts",
	})
	if err != nil {
		t.Fatalf("ExtractToolMetadata: %v", err)
	}
	if meta.ParamsType == nil {
		t.Fatal("ParamsType = nil")
	}
	return meta.ParamsType
}
