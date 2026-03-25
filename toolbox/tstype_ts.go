package toolbox

import (
	"fmt"
	"strings"
)

// TSTypeToTS renders a TSType tree back to TypeScript source text.
// The output is semantically equivalent to the original type, though
// not necessarily character-for-character identical.
//
// This function does NOT depend on the checker or any internal/ packages.
func TSTypeToTS(t *TSType) string {
	if t == nil {
		return "any"
	}
	core := tsTypeToTSCore(t)
	if t.Nullable {
		core = core + " | null"
	}
	return core
}

// tsTypeToTSCore renders the core type without nullable handling.
func tsTypeToTSCore(t *TSType) string {
	switch t.Kind {
	case TSTypeAny:
		return "any"

	case TSTypePrimitive:
		// Handle multi-type primitives stored in ExtraFields (e.g. string | number).
		if t.ExtraFields != nil {
			if multiType, ok := t.ExtraFields["type"].([]any); ok {
				parts := make([]string, len(multiType))
				for i, mt := range multiType {
					if s, ok := mt.(string); ok {
						parts[i] = mapPrimitiveToTS(s)
					}
				}
				return strings.Join(parts, " | ")
			}
		}
		if t.PrimitiveType == "" {
			return "any"
		}
		return mapPrimitiveToTS(t.PrimitiveType)

	case TSTypeLiteral:
		return renderLiteral(t.LiteralValue)

	case TSTypeRef:
		// $ref like "#/definitions/MyType" -> just the type name
		ref := t.Ref
		if idx := strings.LastIndex(ref, "/"); idx >= 0 {
			ref = ref[idx+1:]
		}
		return ref

	case TSTypeObject:
		return renderObject(t)

	case TSTypeArray:
		return renderArray(t)

	case TSTypeTuple:
		return renderTuple(t)

	case TSTypeUnion:
		return renderUnion(t)

	case TSTypeIntersection:
		return renderIntersection(t)

	case TSTypeEnum:
		return renderEnum(t)

	case TSTypeTemplateLiteral:
		// Template literal types have a pattern; represent as string
		return "string"

	default:
		return "any"
	}
}

// mapPrimitiveToTS converts JSON Schema primitive type names to valid
// TypeScript type names. For example, "integer" becomes "number" since
// TypeScript doesn't have an integer type.
func mapPrimitiveToTS(t string) string {
	switch t {
	case "integer":
		return "number"
	case "null":
		return "null"
	default:
		return t
	}
}

// renderLiteral renders a literal value as TypeScript source text.
func renderLiteral(v any) string {
	switch val := v.(type) {
	case string:
		return fmt.Sprintf("%q", val)
	case float64:
		// Render without trailing zeros for integers
		if val == float64(int64(val)) {
			return fmt.Sprintf("%d", int64(val))
		}
		return fmt.Sprintf("%g", val)
	case bool:
		if val {
			return "true"
		}
		return "false"
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%v", val)
	}
}

// renderObject renders a TSTypeObject as TypeScript source text.
func renderObject(t *TSType) string {
	if t.WildcardObject {
		return "Record<string, any>"
	}

	// Determine which properties are required.
	reqSet := make(map[string]bool, len(t.Required))
	for _, name := range t.Required {
		reqSet[name] = true
	}

	var parts []string
	for _, prop := range t.Properties {
		optional := ""
		if !reqSet[prop.Name] {
			optional = "?"
		}
		propType := TSTypeToTS(prop.Schema)
		parts = append(parts, fmt.Sprintf("%s%s: %s", prop.Name, optional, propType))
	}

	// Handle index signatures from AdditionalProperties.
	if t.AdditionalProperties != nil {
		valType := TSTypeToTS(t.AdditionalProperties)
		parts = append(parts, fmt.Sprintf("[key: string]: %s", valType))
	}

	// Handle pattern properties (numeric index, etc.)
	for _, pp := range t.PatternProperties {
		valType := TSTypeToTS(pp.Schema)
		parts = append(parts, fmt.Sprintf("[key: string]: %s", valType))
	}

	if len(parts) == 0 {
		return "{}"
	}
	return "{ " + strings.Join(parts, "; ") + " }"
}

// renderArray renders a TSTypeArray as TypeScript source text.
func renderArray(t *TSType) string {
	if t.Items == nil {
		return "any[]"
	}
	elemType := TSTypeToTS(t.Items)
	// Use Array<T> for complex element types (unions, intersections,
	// nullable, or multi-type primitives).
	if needsArrayGenericForm(t.Items) || strings.Contains(elemType, "|") || strings.Contains(elemType, "&") {
		return "Array<" + elemType + ">"
	}
	return elemType + "[]"
}

// needsArrayGenericForm returns true if the element type should use
// Array<T> form instead of T[] form.
func needsArrayGenericForm(t *TSType) bool {
	if t == nil {
		return false
	}
	if t.Kind == TSTypeUnion || t.Kind == TSTypeIntersection || t.Nullable {
		return true
	}
	// Multi-type primitives stored in ExtraFields (e.g. string | number).
	if t.ExtraFields != nil {
		if _, ok := t.ExtraFields["type"].([]any); ok {
			return true
		}
	}
	return false
}

// renderTuple renders a TSTypeTuple as TypeScript source text.
func renderTuple(t *TSType) string {
	if len(t.TupleItems) == 0 {
		return "[]"
	}
	parts := make([]string, len(t.TupleItems))
	for i, item := range t.TupleItems {
		parts[i] = TSTypeToTS(item)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// renderUnion renders a TSTypeUnion as TypeScript source text.
func renderUnion(t *TSType) string {
	if len(t.Types) == 0 {
		return "never"
	}

	// Check for collapsed literal unions (enum-like).
	if t.CollapseLiterals {
		return renderCollapsedLiteralUnion(t)
	}

	parts := make([]string, len(t.Types))
	for i, branch := range t.Types {
		part := TSTypeToTS(branch)
		// Wrap intersection types in parens when inside a union.
		if branch.Kind == TSTypeIntersection {
			part = "(" + part + ")"
		}
		parts[i] = part
	}
	return strings.Join(parts, " | ")
}

// renderCollapsedLiteralUnion handles unions of literals (CollapseLiterals=true).
func renderCollapsedLiteralUnion(t *TSType) string {
	parts := make([]string, len(t.Types))
	for i, branch := range t.Types {
		parts[i] = TSTypeToTS(branch)
	}
	return strings.Join(parts, " | ")
}

// renderIntersection renders a TSTypeIntersection as TypeScript source text.
func renderIntersection(t *TSType) string {
	if len(t.Types) == 0 {
		return "unknown"
	}
	parts := make([]string, len(t.Types))
	for i, branch := range t.Types {
		part := TSTypeToTS(branch)
		// Wrap union types in parens when inside an intersection.
		if branch.Kind == TSTypeUnion {
			part = "(" + part + ")"
		}
		parts[i] = part
	}
	return strings.Join(parts, " & ")
}

// renderEnum renders a TSTypeEnum as a union of literal values.
func renderEnum(t *TSType) string {
	if len(t.EnumValues) == 0 {
		return "never"
	}
	parts := make([]string, len(t.EnumValues))
	for i, v := range t.EnumValues {
		parts[i] = renderLiteral(v)
	}
	return strings.Join(parts, " | ")
}

// TSFuncSigToTS renders a TSFuncSig as a TypeScript function type expression.
// Example output: (channelID: string, payload: { text: string }) => void
//
// This function does NOT depend on the checker or any internal/ packages.
func TSFuncSigToTS(sig *TSFuncSig) string {
	if sig == nil {
		return "() => void"
	}

	params := make([]string, len(sig.Params))
	for i, p := range sig.Params {
		paramType := "any"
		if p.Type != nil {
			paramType = TSTypeToTS(p.Type)
		}
		params[i] = p.Name + ": " + paramType
	}

	return "(" + strings.Join(params, ", ") + ") => void"
}
