package toolbox_test

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/microsoft/typescript-go/toolbox"
)

func TestExtractToolMetadataParamsAsObjectMatrix(t *testing.T) {
	tests := []struct {
		name          string
		source        string
		wantTS        string
		wantReasons   []string
		wantNoReasons bool
		forbidReasons []string
		reason        string
		failureMode   string
	}{
		{
			name: "single object param preserves nested optional property",
			source: `export default function tool(params: {
  query: string;
  limit?: number;
}) {
  return params.query;
}`,
			wantTS:        `{ params: { limit?: number; query: string } }`,
			wantNoReasons: true,
			reason:        "Exercises ParamsAsObject().ToTS() for a single object param with an optional nested member.",
		},
		{
			name: "multiple params preserve optional param marker",
			source: `export default function tool(query: string, limit: number, offset?: number): string {
  return query;
}`,
			wantTS:        `{ query: string; limit: number; offset?: number }`,
			wantNoReasons: true,
			reason:        "Exercises the synthetic object path in ParamsAsObject() for flat multi-param tools.",
			failureMode:   "renderObject() can mis-render synthetic optional params as required if it stops honoring the Optional flag carried by ParamsAsObject()",
		},
		{
			name: "function-valued property keeps function rendering and reason",
			source: `export default function tool(input: {
  cb: () => void;
}) {
  return input;
}`,
			wantTS:        `{ input: { cb: (...args: any[]) => any } }`,
			wantReasons:   []string{"input.cb uses function type"},
			forbidReasons: []string{"input.cb uses any"},
			reason:        "Exercises the typeof:function extraction path through the synthetic object facade.",
		},
		{
			name: "TJS-type override suppresses bigint diagnostic",
			source: `export default function tool(input: {
  /** @TJS-type string */
  amount: bigint;
}) {
  return input;
}`,
			wantTS:        `{ input: { amount: string } }`,
			wantNoReasons: true,
			reason:        "Exercises a property-level @TJS-type override through ParamsAsObject() to keep schema, TS text, and CannotJSON aligned.",
			failureMode:   "@TJS-type can change the rendered/schema type while CannotJSON still reports the original bigint checker type",
		},
		{
			name: "required property with explicit undefined union stays explicit",
			source: `export default function tool(input: {
  maybe: string | undefined;
}) {
  return input.maybe ?? "";
}`,
			wantTS:      `{ input: { maybe: string | undefined } }`,
			wantReasons: []string{"input.maybe uses undefined"},
			reason:      "Exercises IncludesUndefined rendering through ParamsAsObject().ToTS().",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			meta := extractToolMetadataForParamsAsObject(t, tc.source)
			if meta.Sig == nil {
				t.Fatal("Sig = nil")
			}
			obj := meta.Sig.ParamsAsObject()
			if obj == nil {
				t.Fatal("ParamsAsObject() = nil")
			}

			if got := obj.ToTS(); got != tc.wantTS {
				if tc.failureMode != "" {
					t.Fatalf("ParamsAsObject().ToTS() = %q, want %q\nreason: %s\nfailure mode: %s", got, tc.wantTS, tc.reason, tc.failureMode)
				}
				t.Fatalf("ParamsAsObject().ToTS() = %q, want %q\nreason: %s", got, tc.wantTS, tc.reason)
			}

			reasons := obj.CannotJSON()
			if tc.wantNoReasons {
				if len(reasons) != 0 {
					if tc.failureMode != "" {
						t.Fatalf("ParamsAsObject().CannotJSON() = %#v, want nil\nreason: %s\nfailure mode: %s", reasons, tc.reason, tc.failureMode)
					}
					t.Fatalf("ParamsAsObject().CannotJSON() = %#v, want nil\nreason: %s", reasons, tc.reason)
				}
				return
			}

			if len(tc.wantReasons) == 0 {
				return
			}
			if len(reasons) == 0 {
				t.Fatalf("ParamsAsObject().CannotJSON() = nil, want %#v\nreason: %s", tc.wantReasons, tc.reason)
			}
			joined := strings.Join(reasons, "; ")
			for _, want := range tc.wantReasons {
				if !strings.Contains(joined, want) {
					if tc.failureMode != "" {
						t.Fatalf("ParamsAsObject().CannotJSON() = %#v, want substring %q\nreason: %s\nfailure mode: %s", reasons, want, tc.reason, tc.failureMode)
					}
					t.Fatalf("ParamsAsObject().CannotJSON() = %#v, want substring %q\nreason: %s", reasons, want, tc.reason)
				}
			}
			for _, forbid := range tc.forbidReasons {
				if strings.Contains(joined, forbid) {
					t.Fatalf("ParamsAsObject().CannotJSON() = %#v, should not contain %q\nreason: %s", reasons, forbid, tc.reason)
				}
			}
		})
	}
}

func extractToolMetadataForParamsAsObject(t *testing.T, source string) *toolbox.ToolMetadata {
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
	return meta
}
