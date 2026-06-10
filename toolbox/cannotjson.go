package toolbox

import (
	"fmt"
	"strings"
)

func cannotJSONType(t *tsType, defs map[string]*tsType, path string, optional bool, stack map[*tsType]bool) []string {
	if t == nil {
		return nil
	}
	if stack[t] {
		return nil
	}
	stack[t] = true
	defer delete(stack, t)

	defs = mergeDefinitions(defs, t.Definitions)

	var reasons []string
	if t.IncludesUndefined && !optional {
		reasons = append(reasons, cannotJSONReason(path, "uses undefined"))
	}
	if inner := strings.TrimSuffix(t.DocTypeOverride, "[]"); inner != t.DocTypeOverride {
		if msg := cannotJSONKindReason(nativeObjectCannotJSONKindFromText(inner)); msg != "" {
			reasons = append(reasons, cannotJSONReason(path+"[]", msg))
		}
	}
	if t.DocTypeOverride == "" || isNativeObjectTypeText(t.DocTypeOverride) {
		kind := t.CannotJSONKind
		if kind == cannotJSONNone && t.DocTypeOverride != "" {
			kind = nativeObjectCannotJSONKindFromText(t.DocTypeOverride)
		}
		if msg := cannotJSONKindReason(kind); msg != "" {
			reasons = append(reasons, cannotJSONReason(path, msg))
		}
	}

	switch t.Kind {
	case tsTypeAny:
		if t.CannotJSONKind == cannotJSONNone {
			reasons = append(reasons, cannotJSONReason(path, "uses any"))
		}
	case tsTypeRef:
		if target := resolveDefinition(defs, t.Ref); target != nil {
			reasons = append(reasons, cannotJSONType(target, defs, path, optional, stack)...)
		}
	case tsTypeObject:
		if t.WildcardObject || (t.AdditionalPropertiesBool != nil && *t.AdditionalPropertiesBool && t.AdditionalProperties == nil) {
			reasons = append(reasons, cannotJSONReason(path, "uses open object"))
		}
		for _, prop := range t.Properties {
			reasons = append(reasons, cannotJSONType(prop.Schema, defs, joinCannotJSONPath(path, prop.Name), prop.Optional, stack)...)
		}
		if t.AdditionalProperties != nil {
			reasons = append(reasons, cannotJSONType(t.AdditionalProperties, defs, joinCannotJSONPath(path, "*"), false, stack)...)
		}
		for _, prop := range t.PatternProperties {
			reasons = append(reasons, cannotJSONType(prop.Schema, defs, joinCannotJSONPath(path, fmt.Sprintf("/{%s}", prop.Pattern)), false, stack)...)
		}
	case tsTypeArray:
		reasons = append(reasons, cannotJSONType(t.Items, defs, path+"[]", false, stack)...)
	case tsTypeTuple:
		for i, item := range t.TupleItems {
			reasons = append(reasons, cannotJSONType(item, defs, fmt.Sprintf("%s[%d]", path, i), false, stack)...)
		}
		if t.AdditionalItems != nil {
			reasons = append(reasons, cannotJSONType(t.AdditionalItems, defs, path+"[]", false, stack)...)
		}
	case tsTypeUnion, tsTypeIntersection:
		for _, child := range t.Types {
			reasons = append(reasons, cannotJSONType(child, defs, path, false, stack)...)
		}
	}

	return reasons
}

func cannotJSONKindReason(kind cannotJSONKind) string {
	switch kind {
	case cannotJSONNone:
		return ""
	case cannotJSONAny:
		return "uses any"
	case cannotJSONUnknown:
		return "uses unknown"
	case cannotJSONObject:
		return "uses object"
	case cannotJSONDate:
		return "uses Date"
	case cannotJSONRegExp:
		return "uses RegExp"
	case cannotJSONError:
		return "uses Error"
	case cannotJSONPromise:
		return "uses Promise"
	case cannotJSONFunction:
		return "uses function type"
	case cannotJSONMap:
		return "uses Map"
	case cannotJSONSet:
		return "uses Set"
	case cannotJSONWeakMap:
		return "uses WeakMap"
	case cannotJSONWeakSet:
		return "uses WeakSet"
	case cannotJSONArrayBuffer:
		return "uses ArrayBuffer"
	case cannotJSONDataView:
		return "uses DataView"
	case cannotJSONInt8Array:
		return "uses Int8Array"
	case cannotJSONUint8Array:
		return "uses Uint8Array"
	case cannotJSONUint8ClampedArray:
		return "uses Uint8ClampedArray"
	case cannotJSONInt16Array:
		return "uses Int16Array"
	case cannotJSONUint16Array:
		return "uses Uint16Array"
	case cannotJSONInt32Array:
		return "uses Int32Array"
	case cannotJSONUint32Array:
		return "uses Uint32Array"
	case cannotJSONFloat32Array:
		return "uses Float32Array"
	case cannotJSONFloat64Array:
		return "uses Float64Array"
	case cannotJSONBigInt64Array:
		return "uses BigInt64Array"
	case cannotJSONBigUint64Array:
		return "uses BigUint64Array"
	case cannotJSONBigInt:
		return "uses bigint"
	case cannotJSONSymbol:
		return "uses symbol"
	default:
		return ""
	}
}

// nativeGlobalTypeKinds is the single canonical table of native JS global
// type names recognized by the toolbox, mapped to the cannotJSONKind they
// produce when used in a schema. A value of cannotJSONNone means the name is
// still a native global, but the type is JSON-serializable (e.g. Date, which
// has toJSON). Add new native globals here; nativeObjectCannotJSONKindFromText,
// isNativeObjectBaseName, and IsNativeGlobalTypeName all derive from this map.
var nativeGlobalTypeKinds = map[string]cannotJSONKind{
	"RegExp":            cannotJSONRegExp,
	"Error":             cannotJSONError,
	"Map":               cannotJSONMap,
	"ReadonlyMap":       cannotJSONMap,
	"Set":               cannotJSONSet,
	"ReadonlySet":       cannotJSONSet,
	"WeakMap":           cannotJSONWeakMap,
	"WeakSet":           cannotJSONWeakSet,
	"ArrayBuffer":       cannotJSONArrayBuffer,
	"DataView":          cannotJSONDataView,
	"Int8Array":         cannotJSONInt8Array,
	"Uint8Array":        cannotJSONUint8Array,
	"Uint8ClampedArray": cannotJSONUint8ClampedArray,
	"Int16Array":        cannotJSONInt16Array,
	"Uint16Array":       cannotJSONUint16Array,
	"Int32Array":        cannotJSONInt32Array,
	"Uint32Array":       cannotJSONUint32Array,
	"Float32Array":      cannotJSONFloat32Array,
	"Float64Array":      cannotJSONFloat64Array,
	"BigInt64Array":     cannotJSONBigInt64Array,
	"BigUint64Array":    cannotJSONBigUint64Array,
	"Date":              cannotJSONNone,
}

// IsNativeGlobalTypeName reports whether name is a native JS global type
// name recognized by the toolbox, including JSON-serializable globals such
// as Date.
func IsNativeGlobalTypeName(name string) bool {
	_, ok := nativeGlobalTypeKinds[strings.TrimSpace(name)]
	return ok
}

func nativeObjectCannotJSONKindFromText(text string) cannotJSONKind {
	text = strings.TrimSpace(trimOuterParens(text))
	if strings.HasSuffix(text, "[]") {
		return cannotJSONNone
	}
	base := text
	if idx := strings.IndexByte(base, '<'); idx >= 0 {
		base = base[:idx]
	}
	parts := splitQualifiedName(strings.TrimSpace(base))
	if len(parts) > 0 {
		base = parts[len(parts)-1]
	}
	return nativeGlobalTypeKinds[base]
}

func cannotJSONReason(path, msg string) string {
	if path == "" {
		return msg
	}
	return path + " " + msg
}

func joinCannotJSONPath(path, next string) string {
	if path == "" {
		return next
	}
	if strings.HasPrefix(next, "[") || strings.HasPrefix(next, "/") {
		return path + next
	}
	return path + "." + next
}

func resolveDefinition(defs map[string]*tsType, ref string) *tsType {
	name := strings.TrimPrefix(ref, "#/definitions/")
	if name == ref {
		return nil
	}
	return defs[name]
}

func mergeDefinitions(base, next map[string]*tsType) map[string]*tsType {
	if len(next) == 0 {
		return base
	}
	if len(base) == 0 {
		return next
	}
	out := make(map[string]*tsType, len(base)+len(next))
	for name, def := range base {
		out[name] = def
	}
	for name, def := range next {
		out[name] = def
	}
	return out
}

func dedupeStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}
