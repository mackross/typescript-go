package toolbox

import (
	"context"
	"fmt"
	"sort"
)

// ExtractTSType uses the existing Generator to extract a TSType for a named
// symbol.  This requires a live checker (via the Generator), but the returned
// TSType is checker-independent.
func (g *Generator) ExtractTSType(ctx context.Context, name string) (*TSType, error) {
	schema, err := g.GenerateSchemaForName(ctx, name)
	if err != nil {
		return nil, err
	}
	return SchemaToTSType(schema), nil
}

// ExtractTSTypeForSymbol extracts a TSType for a specific symbol.
func (g *Generator) ExtractTSTypeForSymbol(sym SymbolRef) (*TSType, error) {
	schema, err := g.generateSchemaForSymbol(sym.Symbol, true)
	if err != nil {
		return nil, err
	}
	return SchemaToTSType(schema), nil
}

// SchemaToTSType converts a JSON Schema (map[string]any) to a TSType tree.
// This is the inverse of TSTypeToJSON and enables round-tripping.
func SchemaToTSType(schema map[string]any) *TSType {
	if schema == nil {
		return &TSType{Kind: TSTypeAny}
	}
	t := schemaMapToTSType(schema)

	// Extract top-level schema keys.
	if v, ok := schema["$schema"].(string); ok {
		t.SchemaURI = v
	}
	if v, ok := schema["$id"].(string); ok {
		t.ID = v
		// Clear duplicate from annotations to avoid double-emitting.
		if t.Annotations != nil && t.Annotations.ID == v {
			t.Annotations.ID = ""
		}
	}

	// Extract definitions.  The definitions value may be typed as
	// map[string]any (from generateSchemaForSymbol) or map[string]Schema
	// (from GenerateProgramSchema), since Schema = map[string]any but
	// Go does not unify the map types automatically.
	switch defs := schema["definitions"].(type) {
	case map[string]any:
		t.Definitions = make(map[string]*TSType, len(defs))
		for name, def := range defs {
			if defMap, ok := def.(map[string]any); ok {
				t.Definitions[name] = schemaMapToTSType(defMap)
			}
		}
	case map[string]Schema:
		t.Definitions = make(map[string]*TSType, len(defs))
		for name, def := range defs {
			t.Definitions[name] = schemaMapToTSType(def)
		}
	}

	return t
}

// schemaMapToTSType converts a single schema node (without top-level keys)
// to a TSType.
func schemaMapToTSType(schema map[string]any) *TSType {
	if schema == nil {
		return &TSType{Kind: TSTypeAny}
	}

	t := &TSType{}

	// Extract annotations first (they can appear on any kind).
	t.Annotations = extractAnnotations(schema)
	if t.Annotations != nil && len(t.Annotations.Extra) == 0 && t.Annotations.Description == "" &&
		t.Annotations.Title == "" && t.Annotations.Comment == "" && t.Annotations.ID == "" &&
		t.Annotations.Ref == "" && !t.Annotations.HasDefault {
		t.Annotations = nil
	}

	// Check for $ref.
	if ref, ok := schema["$ref"].(string); ok {
		t.Kind = TSTypeRef
		t.Ref = ref
		extractExtraFields(t, schema)
		return t
	}

	// Check for anyOf.
	if anyOf, ok := schema["anyOf"].([]any); ok {
		t.Kind = TSTypeUnion
		t.AnyOf = make([]*TSType, len(anyOf))
		for i, branch := range anyOf {
			if branchMap, ok := branch.(map[string]any); ok {
				t.AnyOf[i] = schemaMapToTSType(branchMap)
			} else {
				t.AnyOf[i] = &TSType{Kind: TSTypeAny}
			}
		}
		// A union may also carry a "type" field.
		if typ, ok := schema["type"]; ok {
			t.Type = typ
		}
		return t
	}

	// Check for allOf.
	if allOf, ok := schema["allOf"].([]any); ok {
		t.Kind = TSTypeIntersection
		t.AllOf = make([]*TSType, len(allOf))
		for i, branch := range allOf {
			if branchMap, ok := branch.(map[string]any); ok {
				t.AllOf[i] = schemaMapToTSType(branchMap)
			} else {
				t.AllOf[i] = &TSType{Kind: TSTypeAny}
			}
		}
		return t
	}

	// Check for enum.
	if enumVals, ok := schema["enum"].([]any); ok {
		t.Kind = TSTypeEnum
		t.EnumValues = enumVals
		if typ, ok := schema["type"]; ok {
			t.Type = typ
		}
		return t
	}

	// Check for const.
	if constVal, hasConst := schema["const"]; hasConst {
		t.Kind = TSTypeLiteral
		t.ConstValue = constVal
		t.UseEnum = false
		if typ, ok := schema["type"]; ok {
			t.Type = typ
		}
		return t
	}

	// Check for type.
	typ, hasType := schema["type"]
	if !hasType {
		// Empty schema = any.
		if len(schema) == 0 || (onlyAnnotationKeys(schema)) {
			t.Kind = TSTypeAny
			return t
		}
		// Schema with just "id" or other non-type keys.
		t.Kind = TSTypeAny
		extractExtraFields(t, schema)
		return t
	}

	typeStr, isStr := typ.(string)
	if !isStr {
		// Multi-type: e.g. ["string", "null"].
		t.Kind = TSTypeSimple
		t.Type = typ
		extractExtraFields(t, schema)
		return t
	}

	switch typeStr {
	case "object":
		t.Kind = TSTypeObject
		t.Type = "object" // preserve for round-trip
		extractObjectFields(t, schema)
	case "array":
		extractArrayFields(t, schema, typeStr)
	case "string":
		t.Kind = TSTypeSimple
		t.Type = typeStr
		if p, ok := schema["pattern"].(string); ok {
			t.Pattern = p
		}
		if f, ok := schema["format"].(string); ok {
			t.Format = f
		}
		extractExtraFields(t, schema)
	case "number", "integer", "boolean", "null":
		t.Kind = TSTypeSimple
		t.Type = typeStr
		extractExtraFields(t, schema)
	default:
		t.Kind = TSTypeSimple
		t.Type = typeStr
		extractExtraFields(t, schema)
	}

	return t
}

// extractObjectFields populates object-specific fields on a TSType.
func extractObjectFields(t *TSType, schema map[string]any) {
	if props, ok := schema["properties"].(map[string]any); ok {
		t.HasProperties = true
		// Sort property names for deterministic order.
		names := make([]string, 0, len(props))
		for name := range props {
			names = append(names, name)
		}
		sort.Strings(names)
		t.Properties = make([]TSProperty, 0, len(names))
		for _, name := range names {
			propSchema := props[name]
			if propMap, ok := propSchema.(map[string]any); ok {
				t.Properties = append(t.Properties, TSProperty{
					Name:   name,
					Schema: schemaMapToTSType(propMap),
				})
			}
		}
	}

	if req, ok := schema["required"].([]string); ok {
		t.Required = req
	} else if req, ok := schema["required"].([]any); ok {
		t.Required = anyToStrings(req)
	}

	if ap, ok := schema["additionalProperties"]; ok {
		switch v := ap.(type) {
		case bool:
			t.AdditionalPropertiesBool = &v
		case map[string]any:
			t.AdditionalProperties = schemaMapToTSType(v)
		}
	}

	if pp, ok := schema["patternProperties"].(map[string]any); ok {
		for pattern, propSchema := range pp {
			if propMap, ok := propSchema.(map[string]any); ok {
				t.PatternProperties = append(t.PatternProperties, TSPatternProperty{
					Pattern: pattern,
					Schema:  schemaMapToTSType(propMap),
				})
			}
		}
	}

	extractExtraFields(t, schema)
}

// extractArrayFields populates array-specific fields on a TSType.
func extractArrayFields(t *TSType, schema map[string]any, typeStr string) {
	items := schema["items"]
	switch itemsVal := items.(type) {
	case map[string]any:
		// Homogeneous array.
		t.Kind = TSTypeArray
		t.Items = schemaMapToTSType(itemsVal)
	case []any:
		// Tuple.
		t.Kind = TSTypeTuple
		t.TupleItems = make([]*TSType, len(itemsVal))
		for i, item := range itemsVal {
			if itemMap, ok := item.(map[string]any); ok {
				t.TupleItems[i] = schemaMapToTSType(itemMap)
			} else {
				t.TupleItems[i] = &TSType{Kind: TSTypeAny}
			}
		}
	default:
		// Array with no items specified.
		t.Kind = TSTypeArray
	}

	if mi, ok := schema["minItems"]; ok {
		v := toInt(mi)
		t.MinItems = &v
	}
	if mi, ok := schema["maxItems"]; ok {
		v := toInt(mi)
		t.MaxItems = &v
	}
	if ai, ok := schema["additionalItems"].(map[string]any); ok {
		t.AdditionalItems = schemaMapToTSType(ai)
	}

	extractExtraFields(t, schema)
}

// extractAnnotations pulls annotation fields from a schema map.
func extractAnnotations(schema map[string]any) *TSAnnotations {
	ann := &TSAnnotations{}
	if v, ok := schema["description"].(string); ok {
		ann.Description = v
	}
	if v, ok := schema["title"].(string); ok {
		ann.Title = v
	}
	if v, ok := schema["$comment"].(string); ok {
		ann.Comment = v
	}
	// $id from annotation (not top-level).
	if v, ok := schema["$id"].(string); ok {
		ann.ID = v
	}
	if v, ok := schema["default"]; ok {
		ann.Default = v
		ann.HasDefault = true
	}
	return ann
}

// extractExtraFields copies non-standard schema fields into ExtraFields.
func extractExtraFields(t *TSType, schema map[string]any) {
	for k, v := range schema {
		if isStandardSchemaKey(k) {
			continue
		}
		if t.ExtraFields == nil {
			t.ExtraFields = map[string]any{}
		}
		t.ExtraFields[k] = v
	}
}

// isStandardSchemaKey returns true for keys that are handled by named TSType
// fields (and thus should not be duplicated into ExtraFields).
func isStandardSchemaKey(k string) bool {
	switch k {
	case "type", "properties", "required", "additionalProperties",
		"patternProperties", "items", "additionalItems",
		"minItems", "maxItems", "anyOf", "allOf", "oneOf",
		"$ref", "const", "enum",
		"$schema", "$id", "definitions",
		"description", "title", "$comment", "default",
		"pattern", "format":
		return true
	}
	return false
}

// onlyAnnotationKeys returns true if the schema contains only annotation
// keys (description, title, etc.) and no structural type keys.
func onlyAnnotationKeys(schema map[string]any) bool {
	for k := range schema {
		switch k {
		case "description", "title", "$comment", "$id", "$schema", "default", "definitions":
			continue
		default:
			return false
		}
	}
	return true
}

// toInt converts a JSON number to an int.
func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return 0
	}
}

// ExtractToolTSType extracts both ToolMetadata and a TSFuncSig from a
// TypeScript tool file.  The TSFuncSig provides structured access to the
// parameter types without needing to hold the checker in memory.
func ExtractToolTSType(ctx context.Context, input ExtractInput) (*ToolMetadata, *TSFuncSig, error) {
	if input.Files == nil {
		return nil, nil, fmt.Errorf("toolbox: files are required")
	}
	if input.Entry == "" {
		return nil, nil, fmt.Errorf("toolbox: entry is required")
	}

	meta, err := ExtractToolMetadata(ctx, input)
	if err != nil {
		return nil, nil, err
	}

	sig := &TSFuncSig{
		Description: meta.Description,
	}

	if meta.ParamsSchema != nil {
		sig.Params = []TSFuncParam{
			{
				Name: "params",
				Type: SchemaToTSType(meta.ParamsSchema),
			},
		}
	}

	return meta, sig, nil
}
