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
	if msg := cannotJSONKindReason(t.CannotJSONKind); msg != "" {
		reasons = append(reasons, cannotJSONReason(path, msg))
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
