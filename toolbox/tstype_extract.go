package toolbox

import (
	"context"
	"fmt"
	"sort"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/checker"
)

// ExtractTSType uses the existing Generator to extract a TSType for a named
// symbol.  This requires a live checker (via the Generator), but the returned
// TSType is checker-independent.
func (g *Generator) ExtractTSType(ctx context.Context, name string) (*TSType, error) {
	schema, err := g.GenerateSchemaForName(ctx, name)
	if err != nil {
		return nil, err
	}
	return schemaToTSType(schema), nil
}

// ExtractTSTypeForSymbol extracts a TSType for a specific symbol.
func (g *Generator) ExtractTSTypeForSymbol(sym SymbolRef) (*TSType, error) {
	schema, err := g.generateSchemaForSymbol(sym.Symbol, true)
	if err != nil {
		return nil, err
	}
	return schemaToTSType(schema), nil
}

// extractTSType walks a checker.Type directly to produce a TSType tree.
// This parallels emitType but outputs TSType nodes instead of map[string]any.
func (g *Generator) extractTSType(t *checker.Type, sym *ast.Symbol, node *ast.Node) (*TSType, error) {
	if t == nil {
		return &TSType{Kind: TSTypeAny}, nil
	}

	result := &TSType{}

	// Collect JSDoc annotations from the node and symbol.
	isTypeAlias := sym != nil && sym.Flags&ast.SymbolFlagsTypeAlias != 0
	ann := g.collectAnnotations(node, sym, isTypeAlias)
	if ann != nil {
		result.Annotations = ann
	}

	// Check for @TJS-type override.
	docTypeOverride := ""
	if ann != nil && ann.Extra != nil {
		if v, ok := ann.Extra["type"].(string); ok {
			docTypeOverride = v
			result.DocTypeOverride = v
		}
	}

	// When @TJS-type fully overrides the type on an object type, return directly.
	if docTypeOverride != "" && t.Flags()&checker.TypeFlagsObject != 0 {
		result.Kind = TSTypePrimitive
		result.PrimitiveType = docTypeOverride
		return result, nil
	}

	// Type alias with declared type node: check for utility types, type node
	// resolution, etc. These all produce JSON Schema that we convert.
	if typeNode := declaredTypeNode(node); typeNode != nil {
		if sym != nil && sym.Flags&ast.SymbolFlagsTypeAlias != 0 {
			if typeNode.Kind == ast.KindTypeReference && isUtilityTypeName(entityNameText(typeNode.AsTypeReferenceNode().TypeName)) {
				if parsed, err := g.concreteTypeSchema(t, node); err != nil {
					return nil, err
				} else if len(parsed) > 0 {
					return schemaToTSType(overlayParsedSchema(parsed, g.annotationsToSchema(ann))), nil
				}
			}
			parsed, ok, err := g.schemaFromTypeNode(typeNode)
			if err != nil {
				return nil, err
			}
			if ok {
				if _, isRef := parsed["$ref"]; isRef {
					if targetSym := symbolForTypeNode(g.checker, typeNode); targetSym != nil {
						targetDesc := g.descriptionForSymbol(targetSym)
						annSchema := g.annotationsToSchema(ann)
						if targetDesc != "" {
							annSchema["description"] = targetDesc
						} else {
							delete(annSchema, "description")
						}
						return schemaToTSType(overlayParsedSchema(parsed, annSchema)), nil
					}
				}
				return schemaToTSType(overlayParsedSchema(parsed, g.annotationsToSchema(ann))), nil
			}
		}
		if shouldPreferTypeNodeSchema(g.checker, t, sym) {
			parsed, ok, err := g.schemaFromTypeNode(typeNode)
			if err != nil {
				return nil, err
			}
			if ok {
				return schemaToTSType(overlayParsedSchema(parsed, g.annotationsToSchema(ann))), nil
			}
		}
	}
	if targetSym := extendsTargetSymbol(g.checker, node); targetSym != nil && hasNoOwnMembers(node) {
		schema, err := g.refSchemaForSymbol(targetSym)
		if err != nil {
			return nil, err
		}
		return schemaToTSType(schema), nil
	}

	// Literal types from enum members/declarations.
	if isEnumMemberSymbol(sym) || (node != nil && node.Kind == ast.KindEnumMember) {
		schema, err := g.enumMemberSchema(sym, node)
		if err != nil {
			return nil, err
		}
		return schemaToTSType(schema), nil
	}
	if isEnumSymbol(sym) || (node != nil && node.Kind == ast.KindEnumDeclaration) {
		schema, err := g.enumSchema(sym, node)
		if err != nil {
			return nil, err
		}
		return schemaToTSType(schema), nil
	}

	// String/number/boolean/bigint literal types.
	if t.Flags()&(checker.TypeFlagsStringLiteral|checker.TypeFlagsNumberLiteral|checker.TypeFlagsBooleanLiteral|checker.TypeFlagsBigIntLiteral) != 0 {
		lit := t.AsLiteralType()
		val := lit.Value()
		result.Kind = TSTypeLiteral
		switch v := val.(type) {
		case string:
			result.LiteralValue = v
			result.PrimitiveType = "string"
		case bool:
			result.LiteralValue = v
			result.PrimitiveType = "boolean"
		case nil:
			result.LiteralValue = nil
			result.PrimitiveType = "null"
		case float64:
			result.LiteralValue = v
			result.PrimitiveType = g.numberType()
		default:
			if s, ok := val.(string); ok {
				result.LiteralValue = s
				result.PrimitiveType = "string"
			}
		}
		return result, nil
	}

	// Union types.
	if t.Flags()&checker.TypeFlagsUnion != 0 {
		return g.extractUnionTSType(t.AsUnionType(), sym, node, ann)
	}

	// Intersection types.
	if t.Flags()&checker.TypeFlagsIntersection != 0 {
		return g.extractIntersectionTSType(t.AsIntersectionType(), sym, node, ann)
	}

	// Template literal types.
	if t.Flags()&checker.TypeFlagsTemplateLiteral != 0 {
		result.Kind = TSTypeTemplateLiteral
		if docTypeOverride == "" {
			result.PrimitiveType = "string"
		}
		if pattern := templateLiteralPattern(t); pattern != "" {
			result.Pattern = pattern
		}
		return result, nil
	}

	// Primitive types.
	if t.Flags()&checker.TypeFlagsString != 0 {
		result.Kind = TSTypePrimitive
		if docTypeOverride == "" {
			result.PrimitiveType = "string"
		} else {
			result.PrimitiveType = docTypeOverride
		}
		return result, nil
	}
	if t.Flags()&checker.TypeFlagsBoolean != 0 {
		result.Kind = TSTypePrimitive
		if docTypeOverride == "" {
			result.PrimitiveType = "boolean"
		} else {
			result.PrimitiveType = docTypeOverride
		}
		return result, nil
	}
	if t.Flags()&checker.TypeFlagsNumber != 0 {
		result.Kind = TSTypePrimitive
		if docTypeOverride == "" {
			result.PrimitiveType = g.numberType()
		} else {
			result.PrimitiveType = docTypeOverride
		}
		return result, nil
	}
	if t.Flags()&checker.TypeFlagsBigInt != 0 {
		result.Kind = TSTypePrimitive
		if docTypeOverride == "" {
			result.PrimitiveType = g.numberType()
		} else {
			result.PrimitiveType = docTypeOverride
		}
		return result, nil
	}
	if t.Flags()&checker.TypeFlagsNull != 0 {
		result.Kind = TSTypePrimitive
		if docTypeOverride == "" {
			result.PrimitiveType = "null"
		} else {
			result.PrimitiveType = docTypeOverride
		}
		return result, nil
	}
	if t.Flags()&checker.TypeFlagsESSymbol != 0 || t.Flags()&checker.TypeFlagsUniqueESSymbol != 0 {
		result.Kind = TSTypePrimitive
		if docTypeOverride == "" {
			result.PrimitiveType = "object"
		} else {
			result.PrimitiveType = docTypeOverride
		}
		return result, nil
	}

	// Any/unknown.
	if t.Flags()&checker.TypeFlagsAny != 0 || t.Flags()&checker.TypeFlagsUnknown != 0 {
		if parsed, ok, err := g.schemaFromTypeString(g.typeString(t), node); err != nil {
			return nil, err
		} else if ok {
			return schemaToTSType(parsed), nil
		}
		result.Kind = TSTypeAny
		return result, nil
	}

	// Object types.
	if t.Flags()&checker.TypeFlagsObject != 0 {
		schema, err := g.objectSchema(t, sym, node)
		if err != nil {
			return nil, err
		}
		return schemaToTSType(schema), nil
	}

	// Fallback: empty schema = any.
	result.Kind = TSTypeAny
	return result, nil
}

// extractUnionTSType extracts a union type, preserving TS-level union structure.
// Individual literal members are kept as TSTypeLiteral nodes in the union.
func (g *Generator) extractUnionTSType(t *checker.UnionType, sym *ast.Symbol, node *ast.Node, ann *TSAnnotations) (*TSType, error) {
	// Delegate to the existing unionSchema and convert.
	// This ensures identical JSON Schema output while preserving
	// the union structure at the TSType level.
	schema, err := g.unionSchema(t, sym, node)
	if err != nil {
		return nil, err
	}
	result := schemaToTSType(schema)
	if ann != nil {
		result.Annotations = ann
	}
	return result, nil
}

// extractIntersectionTSType extracts an intersection type.
func (g *Generator) extractIntersectionTSType(t *checker.IntersectionType, sym *ast.Symbol, node *ast.Node, ann *TSAnnotations) (*TSType, error) {
	schema, err := g.intersectionSchema(t, sym, node)
	if err != nil {
		return nil, err
	}
	result := schemaToTSType(schema)
	if ann != nil {
		result.Annotations = ann
	}
	return result, nil
}

// collectAnnotations gathers JSDoc annotations from a node and symbol into
// a TSAnnotations struct.
func (g *Generator) collectAnnotations(node *ast.Node, sym *ast.Symbol, isTypeAlias bool) *TSAnnotations {
	ann := &TSAnnotations{}
	hasContent := false

	applyDocs := func(docs *docInfo, overwrite bool) {
		if docs == nil {
			return
		}
		if docs.description != "" && (overwrite || ann.Description == "") {
			ann.Description = docs.description
			hasContent = true
		}
		if docs.title != "" && (overwrite || ann.Title == "") {
			ann.Title = docs.title
			hasContent = true
		}
		if docs.comment != "" && (overwrite || ann.Comment == "") {
			ann.Comment = docs.comment
			hasContent = true
		}
		if docs.id != "" && (overwrite || ann.ID == "") {
			ann.ID = docs.id
			hasContent = true
		}
		if docs.ref != "" && (overwrite || ann.Ref == "") {
			ann.Ref = docs.ref
			hasContent = true
		}
		if docs.nullable {
			if isTypeAlias {
				g.nullableTypes[sym] = true
			} else {
				ann.Nullable = true
				hasContent = true
			}
		}
		for k, v := range docs.fields {
			if ann.Extra == nil {
				ann.Extra = map[string]any{}
			}
			if overwrite {
				ann.Extra[k] = v
			} else if _, exists := ann.Extra[k]; !exists {
				ann.Extra[k] = v
			}
			hasContent = true
		}
	}

	if docs := g.parseDocs(node); docs != nil {
		if isTypeAlias && docs.nullable {
			g.nullableTypes[sym] = true
			docs.nullable = false
		}
		applyDocs(docs, true)
	}
	if sym != nil {
		if docs := g.parseDocs(sym.ValueDeclaration); docs != nil {
			if isTypeAlias && docs.nullable {
				g.nullableTypes[sym] = true
				docs.nullable = false
			}
			applyDocs(docs, false)
		}
		if len(sym.Declarations) > 0 {
			if docs := g.parseDocs(sym.Declarations[0]); docs != nil {
				if isTypeAlias && docs.nullable {
					g.nullableTypes[sym] = true
					docs.nullable = false
				}
				applyDocs(docs, false)
			}
		}
	}

	if !hasContent {
		return nil
	}
	return ann
}

// annotationsToSchema converts TSAnnotations to a partial JSON Schema map
// for use with overlayParsedSchema.
func (g *Generator) annotationsToSchema(ann *TSAnnotations) Schema {
	if ann == nil {
		return Schema{}
	}
	schema := Schema{}
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
	if ann.Nullable {
		makeNullable(schema)
	}
	if ann.HasDefault {
		schema["default"] = ann.Default
	}
	for k, v := range ann.Extra {
		schema[k] = v
	}
	return schema
}

// schemaToTSType converts a JSON Schema (map[string]any) to a TSType tree.
// This is used internally for cases where we still delegate to the schema
// generation path (e.g. object types, utility types).
func schemaToTSType(schema map[string]any) *TSType {
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

	// Extract definitions.
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
	t.Annotations = extractSchemaAnnotations(schema)
	if t.Annotations != nil && len(t.Annotations.Extra) == 0 && t.Annotations.Description == "" &&
		t.Annotations.Title == "" && t.Annotations.Comment == "" && t.Annotations.ID == "" &&
		t.Annotations.Ref == "" && !t.Annotations.HasDefault {
		t.Annotations = nil
	}

	// Check for $ref.
	if ref, ok := schema["$ref"].(string); ok {
		t.Kind = TSTypeRef
		t.Ref = ref
		extractSchemaExtraFields(t, schema)
		return t
	}

	// Check for anyOf (union).
	if anyOf, ok := schema["anyOf"].([]any); ok {
		t.Kind = TSTypeUnion
		t.Types = make([]*TSType, len(anyOf))
		for i, branch := range anyOf {
			if branchMap, ok := branch.(map[string]any); ok {
				t.Types[i] = schemaMapToTSType(branchMap)
			} else {
				t.Types[i] = &TSType{Kind: TSTypeAny}
			}
		}
		// A union may also carry a "type" field (simple types alongside anyOf).
		if typ, ok := schema["type"]; ok {
			t.PrimitiveType, _ = typ.(string)
		}
		return t
	}

	// Check for allOf (intersection).
	if allOf, ok := schema["allOf"].([]any); ok {
		t.Kind = TSTypeIntersection
		t.Types = make([]*TSType, len(allOf))
		for i, branch := range allOf {
			if branchMap, ok := branch.(map[string]any); ok {
				t.Types[i] = schemaMapToTSType(branchMap)
			} else {
				t.Types[i] = &TSType{Kind: TSTypeAny}
			}
		}
		return t
	}

	// Check for enum (multiple values).
	if enumVals, ok := schema["enum"].([]any); ok {
		t.Kind = TSTypeEnum
		t.EnumValues = enumVals
		if typ, ok := schema["type"]; ok {
			switch v := typ.(type) {
			case string:
				t.PrimitiveType = v
			case []any:
				// Multi-type enum: store as ExtraFields for round-trip.
				t.ExtraFields = map[string]any{"type": v}
			}
		}
		return t
	}

	// Check for const (single literal value from schema).
	if constVal, hasConst := schema["const"]; hasConst {
		t.Kind = TSTypeLiteral
		t.LiteralValue = constVal
		if typ, ok := schema["type"].(string); ok {
			t.PrimitiveType = typ
		}
		return t
	}

	// Check for type.
	typ, hasType := schema["type"]
	if !hasType {
		// Empty schema = any.
		if len(schema) == 0 || onlyAnnotationKeys(schema) {
			t.Kind = TSTypeAny
			return t
		}
		t.Kind = TSTypeAny
		extractSchemaExtraFields(t, schema)
		return t
	}

	typeStr, isStr := typ.(string)
	if !isStr {
		// Multi-type: e.g. ["string", "null"].
		t.Kind = TSTypePrimitive
		// Store multi-type as ExtraFields for round-trip fidelity.
		t.ExtraFields = map[string]any{"type": typ}
		extractSchemaExtraFields(t, schema)
		return t
	}

	switch typeStr {
	case "object":
		t.Kind = TSTypeObject
		extractSchemaObjectFields(t, schema)
	case "array":
		extractSchemaArrayFields(t, schema)
	case "string":
		t.Kind = TSTypePrimitive
		t.PrimitiveType = typeStr
		if p, ok := schema["pattern"].(string); ok {
			t.Pattern = p
		}
		if f, ok := schema["format"].(string); ok {
			t.Format = f
		}
		extractSchemaExtraFields(t, schema)
	case "number", "integer", "boolean", "null":
		t.Kind = TSTypePrimitive
		t.PrimitiveType = typeStr
		extractSchemaExtraFields(t, schema)
	default:
		t.Kind = TSTypePrimitive
		t.PrimitiveType = typeStr
		extractSchemaExtraFields(t, schema)
	}

	return t
}

// extractSchemaObjectFields populates object-specific fields on a TSType.
func extractSchemaObjectFields(t *TSType, schema map[string]any) {
	if props, ok := schema["properties"]; ok {
		t.EmptyObject = true // Mark that "properties" key was present.
		if propsMap, ok := props.(map[string]any); ok {
			// Sort property names for deterministic order.
			names := make([]string, 0, len(propsMap))
			for name := range propsMap {
				names = append(names, name)
			}
			sort.Strings(names)
			t.Properties = make([]TSProperty, 0, len(names))
			for _, name := range names {
				propSchema := propsMap[name]
				if propMap, ok := propSchema.(map[string]any); ok {
					t.Properties = append(t.Properties, TSProperty{
						Name:   name,
						Schema: schemaMapToTSType(propMap),
					})
				}
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

	extractSchemaExtraFields(t, schema)
}

// extractSchemaArrayFields populates array-specific fields on a TSType.
func extractSchemaArrayFields(t *TSType, schema map[string]any) {
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

	extractSchemaExtraFields(t, schema)
}

// extractSchemaAnnotations pulls annotation fields from a schema map.
func extractSchemaAnnotations(schema map[string]any) *TSAnnotations {
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
	if v, ok := schema["$id"].(string); ok {
		ann.ID = v
	}
	if v, ok := schema["default"]; ok {
		ann.Default = v
		ann.HasDefault = true
	}
	return ann
}

// extractSchemaExtraFields copies non-standard schema fields into ExtraFields.
func extractSchemaExtraFields(t *TSType, schema map[string]any) {
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
// keys and no structural type keys.
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
// TypeScript tool file.
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
				Type: schemaToTSType(meta.ParamsSchema),
			},
		}
	}

	return meta, sig, nil
}
