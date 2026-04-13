package toolbox

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// tsTypeToTS renders a tsType tree back to TypeScript source text.
// The output is semantically equivalent to the original type, though
// not necessarily character-for-character identical.
//
// This function does NOT depend on the checker or any internal/ packages.
func tsTypeToTS(t *tsType) string {
	if t == nil {
		return "any"
	}
	core := tsTypeToTSCore(t)
	if t.Nullable {
		core = core + " | null"
	}
	return core
}

// tsTypeDeclarationsToTS emits type alias declarations for all definitions
// in the tsType.  Each definition is rendered as "type Foo = ...;\n".
// Definition names are sanitized to valid TypeScript identifiers.
// Returns an empty string when there are no definitions.
func tsTypeDeclarationsToTS(t *tsType) string {
	if t == nil || len(t.Definitions) == 0 {
		return ""
	}
	// Sort definitions for deterministic output.
	names := make([]string, 0, len(t.Definitions))
	for name := range t.Definitions {
		names = append(names, name)
	}
	sort.Strings(names)

	var sb strings.Builder
	for _, name := range names {
		def := t.Definitions[name]
		sanitized := sanitizeTSIdentifier(name)
		sb.WriteString("type ")
		sb.WriteString(sanitized)
		sb.WriteString(" = ")
		sb.WriteString(tsTypeToTS(def))
		sb.WriteString(";\n")
	}
	return sb.String()
}

// sanitizeTSIdentifier converts a definition name (which may contain
// characters like <, >, spaces) into a valid TypeScript identifier.
func sanitizeTSIdentifier(name string) string {
	var sb strings.Builder
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '$' {
			sb.WriteRune(r)
		} else {
			sb.WriteRune('_')
		}
	}
	result := sb.String()
	if result == "" {
		return "_"
	}
	// Ensure it doesn't start with a digit.
	if unicode.IsDigit(rune(result[0])) {
		result = "_" + result
	}
	return result
}

// tsTypeToTSCore renders the core type without nullable handling.
func tsTypeToTSCore(t *tsType) string {
	switch t.Kind {
	case tsTypeAny:
		return "any"

	case tsTypePrimitive:
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

	case tsTypeLiteral:
		return renderLiteral(t.LiteralValue)

	case tsTypeRef:
		if isExternalRef(t.Ref) {
			// External $ref (e.g. "http://my-schema.org") cannot be
			// represented as a TS type name.  Render as "any"; the
			// caller (renderObject) emits a JSDoc @$ref annotation
			// on the property so the ref survives re-parsing.
			return "any"
		}
		// $ref like "#/definitions/MyType" -> sanitized type name
		ref := t.Ref
		if idx := strings.LastIndex(ref, "/"); idx >= 0 {
			ref = ref[idx+1:]
		}
		return sanitizeTSIdentifier(ref)

	case tsTypeObject:
		return renderObject(t)

	case tsTypeArray:
		return renderArray(t)

	case tsTypeTuple:
		return renderTuple(t)

	case tsTypeUnion:
		return renderUnion(t)

	case tsTypeIntersection:
		return renderIntersection(t)

	case tsTypeEnum:
		return renderEnum(t)

	case tsTypeTemplateLiteral:
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

// isExternalRef returns true when a $ref URI points to an external schema
// (e.g. "http://my-schema.org") rather than an internal definition
// (e.g. "#/definitions/MyType").
func isExternalRef(ref string) bool {
	return ref != "" && !strings.HasPrefix(ref, "#")
}

// renderObject renders a tsTypeObject as TypeScript source text.
func renderObject(t *tsType) string {
	if t.WildcardObject {
		return "Record<string, any>"
	}

	// Determine which properties are required.
	reqSet := make(map[string]bool, len(t.Required))
	for _, name := range t.Required {
		reqSet[name] = true
	}

	// Check if any property needs a JSDoc annotation (external $ref).
	// When JSDoc is needed, we must use multi-line format because the
	// TS parser only associates /** ... */ comments with the next node
	// when they appear on a preceding line.
	hasJSDoc := false
	for _, prop := range t.Properties {
		if prop.Schema != nil && prop.Schema.Kind == tsTypeRef && isExternalRef(prop.Schema.Ref) {
			hasJSDoc = true
			break
		}
	}

	var parts []string
	for _, prop := range t.Properties {
		optional := ""
		isOptional := prop.Optional
		if reqSet[prop.Name] {
			isOptional = false
		} else if len(reqSet) > 0 {
			isOptional = true
		}
		if isOptional {
			optional = "?"
		}
		propType := tsTypeToTS(prop.Schema)
		if hasJSDoc && prop.Schema != nil && prop.Schema.Kind == tsTypeRef && isExternalRef(prop.Schema.Ref) {
			// Multi-line JSDoc annotation before the property.
			parts = append(parts, fmt.Sprintf("/** @$ref %s */\n%s%s: %s", prop.Schema.Ref, prop.Name, optional, propType))
		} else {
			parts = append(parts, fmt.Sprintf("%s%s: %s", prop.Name, optional, propType))
		}
	}

	// Handle index signatures from AdditionalProperties.
	if t.AdditionalProperties != nil {
		valType := tsTypeToTS(t.AdditionalProperties)
		parts = append(parts, fmt.Sprintf("[key: string]: %s", valType))
	}

	// Handle pattern properties (numeric index, etc.)
	for _, pp := range t.PatternProperties {
		valType := tsTypeToTS(pp.Schema)
		// Numeric pattern (^[0-9]+$) → [key: number]
		if pp.Pattern == "^[0-9]+$" {
			parts = append(parts, fmt.Sprintf("[key: number]: %s", valType))
		} else {
			parts = append(parts, fmt.Sprintf("[key: string]: %s", valType))
		}
	}

	if len(parts) == 0 {
		return "{}"
	}
	if hasJSDoc {
		// Multi-line format for JSDoc annotations.
		return "{\n" + strings.Join(parts, ";\n") + ";\n}"
	}
	return "{ " + strings.Join(parts, "; ") + " }"
}

// renderArray renders a tsTypeArray as TypeScript source text.
func renderArray(t *tsType) string {
	if t.Items == nil {
		return "any[]"
	}
	elemType := tsTypeToTS(t.Items)
	// Use Array<T> for complex element types (unions, intersections,
	// nullable, or multi-type primitives).
	if needsArrayGenericForm(t.Items) || strings.Contains(elemType, "|") || strings.Contains(elemType, "&") {
		return "Array<" + elemType + ">"
	}
	return elemType + "[]"
}

// needsArrayGenericForm returns true if the element type should use
// Array<T> form instead of T[] form.
func needsArrayGenericForm(t *tsType) bool {
	if t == nil {
		return false
	}
	if t.Kind == tsTypeUnion || t.Kind == tsTypeIntersection || t.Nullable {
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

// renderTuple renders a tsTypeTuple as TypeScript source text.
func renderTuple(t *tsType) string {
	if len(t.TupleItems) == 0 && t.AdditionalItems == nil {
		return "[]"
	}

	// Determine which items are optional (index >= minItems).
	minItems := len(t.TupleItems) // default: all required
	if t.MinItems != nil {
		minItems = *t.MinItems
	}

	var parts []string
	for i, item := range t.TupleItems {
		elemType := tsTypeToTS(item)
		if i >= minItems {
			// Optional tuple element
			elemType += "?"
		}
		parts = append(parts, elemType)
	}

	// AdditionalItems → rest element.
	// Only render as rest when it represents a genuine rest parameter,
	// not a JSON Schema catch-all. Heuristic: skip when MinItems equals
	// the number of tuple items AND the additional items type is a union
	// of the tuple item types (catch-all pattern).
	if t.AdditionalItems != nil && !isAdditionalItemsCatchAll(t) {
		restType := tsTypeToTS(t.AdditionalItems)
		// Wrap complex types in parens before adding []
		if needsArrayGenericForm(t.AdditionalItems) || strings.Contains(restType, "|") || strings.Contains(restType, "&") {
			parts = append(parts, "...Array<"+restType+">")
		} else {
			parts = append(parts, "..."+restType+"[]")
		}
	}

	return "[" + strings.Join(parts, ", ") + "]"
}

// isAdditionalItemsCatchAll returns true when the AdditionalItems on a tuple
// is just a union of all the tuple item types (JSON Schema catch-all pattern,
// not a genuine rest element).
func isAdditionalItemsCatchAll(t *tsType) bool {
	if t.AdditionalItems == nil || len(t.TupleItems) == 0 {
		return false
	}
	minItems := len(t.TupleItems)
	if t.MinItems != nil {
		minItems = *t.MinItems
	}
	// If not all items are required, it's a variable-length tuple, not a catch-all.
	if minItems < len(t.TupleItems) {
		return false
	}
	// If AdditionalItems is a union, check if it's the union of all tuple
	// item types (catch-all pattern).
	ai := t.AdditionalItems
	if ai.Kind == tsTypeUnion && len(ai.Types) == len(t.TupleItems) {
		// Quick check: each union branch matches a tuple item type.
		tupleTypes := make(map[string]bool, len(t.TupleItems))
		for _, item := range t.TupleItems {
			tupleTypes[tsTypeToTS(item)] = true
		}
		for _, branch := range ai.Types {
			if !tupleTypes[tsTypeToTS(branch)] {
				return false
			}
		}
		return true
	}
	// Single type AdditionalItems where there's exactly one tuple item type
	// and they match.
	if len(t.TupleItems) == 1 && tsTypeToTS(ai) == tsTypeToTS(t.TupleItems[0]) {
		return true
	}
	return false
}

// renderUnion renders a tsTypeUnion as TypeScript source text.
func renderUnion(t *tsType) string {
	if len(t.Types) == 0 {
		return "never"
	}

	// Check for collapsed literal unions (enum-like).
	if t.CollapseLiterals {
		return renderCollapsedLiteralUnion(t)
	}

	parts := make([]string, len(t.Types))
	for i, branch := range t.Types {
		part := tsTypeToTS(branch)
		// Wrap intersection types in parens when inside a union.
		if branch.Kind == tsTypeIntersection {
			part = "(" + part + ")"
		}
		parts[i] = part
	}
	return strings.Join(parts, " | ")
}

// renderCollapsedLiteralUnion handles unions of literals (CollapseLiterals=true).
func renderCollapsedLiteralUnion(t *tsType) string {
	parts := make([]string, len(t.Types))
	for i, branch := range t.Types {
		parts[i] = tsTypeToTS(branch)
	}
	return strings.Join(parts, " | ")
}

// renderIntersection renders a tsTypeIntersection as TypeScript source text.
func renderIntersection(t *tsType) string {
	if len(t.Types) == 0 {
		return "unknown"
	}
	parts := make([]string, len(t.Types))
	for i, branch := range t.Types {
		part := tsTypeToTS(branch)
		// Wrap union types in parens when inside an intersection.
		if branch.Kind == tsTypeUnion {
			part = "(" + part + ")"
		}
		parts[i] = part
	}
	return strings.Join(parts, " & ")
}

// renderEnum renders a tsTypeEnum as a union of literal values.
func renderEnum(t *tsType) string {
	if len(t.EnumValues) == 0 {
		return "never"
	}
	parts := make([]string, len(t.EnumValues))
	for i, v := range t.EnumValues {
		parts[i] = renderLiteral(v)
	}
	return strings.Join(parts, " | ")
}

// tsFuncSigToTS renders a tsFuncSig as a TypeScript function type expression.
// Example output: (channelID: string, payload: { text: string }) => void
//
// This function does NOT depend on the checker or any internal/ packages.
func tsFuncSigToTS(sig *tsFuncSig) string {
	if sig == nil {
		return "() => void"
	}

	params := make([]string, len(sig.Params))
	for i, p := range sig.Params {
		paramType := "any"
		if p.Type != nil {
			paramType = tsTypeToTS(p.Type)
		}
		params[i] = p.Name + ": " + paramType
	}

	retStr := "void"
	if sig.ReturnType != nil {
		retStr = tsTypeToTS(sig.ReturnType)
	}

	return "(" + strings.Join(params, ", ") + ") => " + retStr
}
