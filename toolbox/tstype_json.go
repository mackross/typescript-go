package toolbox

import "sort"

// TSTypeToJSON converts a TSType tree into a JSON Schema map[string]any.
// This function does NOT depend on the checker or any internal/ packages.
func TSTypeToJSON(t *TSType) map[string]any {
	if t == nil {
		return map[string]any{}
	}
	schema := tsTypeToSchema(t)
	if schema == nil {
		schema = map[string]any{}
	}

	// Apply top-level schema keys.
	if t.SchemaURI != "" {
		schema["$schema"] = t.SchemaURI
	}
	if t.ID != "" {
		schema["$id"] = t.ID
	}

	// Apply definitions.
	if len(t.Definitions) > 0 {
		defs := make(map[string]any, len(t.Definitions))
		for name, def := range t.Definitions {
			defs[name] = tsTypeToSchema(def)
		}
		schema["definitions"] = defs
	}

	return schema
}

// tsTypeToSchema recursively converts a TSType node into a schema map.
func tsTypeToSchema(t *TSType) map[string]any {
	if t == nil {
		return map[string]any{}
	}

	schema := map[string]any{}

	switch t.Kind {
	case TSTypeAny:
		// Empty schema = any/unknown.

	case TSTypePrimitive:
		if t.PrimitiveType != "" {
			schema["type"] = t.PrimitiveType
		}
		if t.Pattern != "" {
			schema["pattern"] = t.Pattern
		}
		if t.Format != "" {
			schema["format"] = t.Format
		}
		// Handle multi-type stored in ExtraFields (e.g. ["string", "null"]).
		if t.ExtraFields != nil {
			if multiType, ok := t.ExtraFields["type"]; ok {
				schema["type"] = multiType
			}
		}

	case TSTypeLiteral:
		if t.PrimitiveType != "" {
			schema["type"] = t.PrimitiveType
		}
		schema["const"] = t.LiteralValue

	case TSTypeEnum:
		if t.PrimitiveType != "" {
			schema["type"] = t.PrimitiveType
		}
		// Handle multi-type enum stored in ExtraFields.
		if t.ExtraFields != nil {
			if multiType, ok := t.ExtraFields["type"]; ok {
				schema["type"] = multiType
			}
		}
		if len(t.EnumValues) > 0 {
			schema["enum"] = t.EnumValues
		}

	case TSTypeRef:
		schema["$ref"] = t.Ref

	case TSTypeObject:
		schema["type"] = "object"
		if len(t.Properties) > 0 || t.EmptyObject {
			props := make(map[string]any, len(t.Properties))
			for _, prop := range t.Properties {
				props[prop.Name] = tsTypeToSchema(prop.Schema)
			}
			schema["properties"] = props
		}
		if len(t.Required) > 0 {
			schema["required"] = t.Required
		}
		applyTSAdditionalProperties(schema, t)
		applyTSPatternProperties(schema, t)

	case TSTypeArray:
		schema["type"] = "array"
		if t.Items != nil {
			schema["items"] = tsTypeToSchema(t.Items)
		}
		if t.MinItems != nil {
			schema["minItems"] = *t.MinItems
		}
		if t.MaxItems != nil {
			schema["maxItems"] = *t.MaxItems
		}
		if t.AdditionalItems != nil {
			schema["additionalItems"] = tsTypeToSchema(t.AdditionalItems)
		}

	case TSTypeTuple:
		schema["type"] = "array"
		if len(t.TupleItems) > 0 {
			items := make([]any, len(t.TupleItems))
			for i, item := range t.TupleItems {
				items[i] = tsTypeToSchema(item)
			}
			schema["items"] = items
		}
		if t.MinItems != nil {
			schema["minItems"] = *t.MinItems
		}
		if t.MaxItems != nil {
			schema["maxItems"] = *t.MaxItems
		}
		if t.AdditionalItems != nil {
			schema["additionalItems"] = tsTypeToSchema(t.AdditionalItems)
		}

	case TSTypeUnion:
		if len(t.Types) > 0 {
			anyOf := make([]any, len(t.Types))
			for i, branch := range t.Types {
				anyOf[i] = tsTypeToSchema(branch)
			}
			schema["anyOf"] = anyOf
		}
		// A union node may also carry a primitive type alongside anyOf.
		if t.PrimitiveType != "" {
			schema["type"] = t.PrimitiveType
		}
		// Handle multi-type stored in ExtraFields.
		if t.ExtraFields != nil {
			if multiType, ok := t.ExtraFields["type"]; ok {
				schema["type"] = multiType
			}
		}

	case TSTypeIntersection:
		if len(t.Types) > 0 {
			allOf := make([]any, len(t.Types))
			for i, branch := range t.Types {
				allOf[i] = tsTypeToSchema(branch)
			}
			schema["allOf"] = allOf
		}

	case TSTypeTemplateLiteral:
		if t.PrimitiveType != "" {
			schema["type"] = t.PrimitiveType
		}
		if t.Pattern != "" {
			schema["pattern"] = t.Pattern
		}
	}

	// Apply format if set (can appear on any kind).
	if t.Format != "" && t.Kind != TSTypePrimitive && t.Kind != TSTypeTemplateLiteral {
		schema["format"] = t.Format
	}

	// Apply annotations.
	applyTSAnnotations(schema, t.Annotations)

	// Apply extra fields (excluding "type" which is handled above).
	for k, v := range t.ExtraFields {
		if k == "type" {
			continue // Already handled in the switch above.
		}
		schema[k] = v
	}

	// Apply nullable.
	if t.Nullable {
		makeNullableSchema(schema)
	}

	return schema
}

// applyTSAdditionalProperties sets additionalProperties on the schema.
func applyTSAdditionalProperties(schema map[string]any, t *TSType) {
	if t.AdditionalProperties != nil {
		schema["additionalProperties"] = tsTypeToSchema(t.AdditionalProperties)
	} else if t.AdditionalPropertiesBool != nil {
		schema["additionalProperties"] = *t.AdditionalPropertiesBool
	}
}

// applyTSPatternProperties sets patternProperties on the schema.
func applyTSPatternProperties(schema map[string]any, t *TSType) {
	if len(t.PatternProperties) > 0 {
		pp := make(map[string]any, len(t.PatternProperties))
		for _, p := range t.PatternProperties {
			pp[p.Pattern] = tsTypeToSchema(p.Schema)
		}
		schema["patternProperties"] = pp
	}
}

// applyTSAnnotations merges TSAnnotations into a schema map.
func applyTSAnnotations(schema map[string]any, ann *TSAnnotations) {
	if ann == nil {
		return
	}
	if ann.Description != "" {
		schema["description"] = ann.Description
	}
	if ann.Title != "" {
		schema["title"] = ann.Title
	}
	if ann.Comment != "" {
		schema["$comment"] = ann.Comment
	}
	if ann.ID != "" {
		schema["$id"] = ann.ID
	}
	if ann.Ref != "" {
		schema["$ref"] = ann.Ref
	}
	if ann.HasDefault {
		schema["default"] = ann.Default
	}
	for k, v := range ann.Extra {
		// When there's a $ref, only "safe" fields should be included.
		if _, hasRef := schema["$ref"]; hasRef && !isRefSafeDocField(k) {
			continue
		}
		// Merge nested maps.
		if existing, ok := schema[k].(map[string]any); ok {
			if extra, ok := v.(map[string]any); ok {
				for ek, ev := range extra {
					existing[ek] = ev
				}
				schema[k] = existing
				continue
			}
		}
		schema[k] = v
	}
}

// makeNullableSchema makes a schema nullable, matching the behavior of the
// existing makeNullable function. This version operates on map[string]any.
func makeNullableSchema(schema map[string]any) {
	if schema == nil {
		return
	}
	if t, ok := schema["type"].(string); ok {
		if schemaHasStructuralPropsJSON(schema) {
			original := cloneSchemaMap(schema)
			for k := range schema {
				delete(schema, k)
			}
			schema["anyOf"] = []any{original, map[string]any{"type": "null"}}
			return
		}
		schema["type"] = []any{t, "null"}
		return
	}
	if arr, ok := schema["type"].([]any); ok {
		for _, x := range arr {
			if s, ok := x.(string); ok && s == "null" {
				return
			}
		}
		schema["type"] = append(arr, "null")
		return
	}
	if _, ok := schema["$ref"]; ok {
		ref := schema["$ref"]
		delete(schema, "$ref")
		schema["anyOf"] = []any{map[string]any{"$ref": ref}, map[string]any{"type": "null"}}
		return
	}
	if _, ok := schema["anyOf"]; ok {
		return
	}
	original := cloneSchemaMap(schema)
	for k := range schema {
		delete(schema, k)
	}
	schema["anyOf"] = []any{original, map[string]any{"type": "null"}}
}

func schemaHasStructuralPropsJSON(schema map[string]any) bool {
	for k := range schema {
		switch k {
		case "type", "description", "title", "default", "examples":
			continue
		default:
			return true
		}
	}
	return false
}

func cloneSchemaMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = cloneValue(v)
	}
	return out
}

// TSFuncSigToJSON converts a TSFuncSig to the ParamsSchema format
// (a JSON Schema for the first parameter's type).
func TSFuncSigToJSON(sig *TSFuncSig) map[string]any {
	if sig == nil || len(sig.Params) == 0 {
		return nil
	}
	return TSTypeToJSON(sig.Params[0].Type)
}

// sortedDefinitionNames returns definition names in sorted order
// for deterministic output.
func sortedDefinitionNames(defs map[string]*TSType) []string {
	names := make([]string, 0, len(defs))
	for name := range defs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
