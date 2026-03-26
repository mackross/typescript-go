package toolbox

import (
	"context"
	"fmt"
	"sort"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/checker"
	"github.com/microsoft/typescript-go/internal/jsnum"
)

// ExtractTSType uses the existing Generator to extract a TSType for a named
// symbol.  This calls extractTSType directly from the checker types rather
// than going through the JSON Schema path.
func (g *Generator) ExtractTSType(ctx context.Context, name string) (*TSType, error) {
	if name == "*" {
		// Program-level schema: generate via the schema path and convert.
		schema, err := g.GenerateProgramSchema()
		if err != nil {
			return nil, err
		}
		return schemaToTSType(schema), nil
	}

	symbols := g.lookupSymbols(name)
	if len(symbols) == 0 {
		return nil, fmt.Errorf("toolbox: symbol %q not found", name)
	}
	if len(symbols) > 1 && !g.opts.UniqueNames {
		return nil, fmt.Errorf("toolbox: symbol %q resolved to %d candidates; enable unique names or use a qualified name", name, len(symbols))
	}
	if len(symbols) == 1 {
		return g.extractTSTypeForSymbol(symbols[0])
	}
	// Multiple symbols: fall back to schema path for unique-names support.
	schema, err := g.GenerateSchemaForName(ctx, name)
	if err != nil {
		return nil, err
	}
	return schemaToTSType(schema), nil
}

// ExtractTSTypeForSymbol extracts a TSType for a specific symbol.
func (g *Generator) ExtractTSTypeForSymbol(sym SymbolRef) (*TSType, error) {
	return g.extractTSTypeForSymbol(sym.Symbol)
}

// extractTSTypeForSymbol parallels generateSchemaForSymbol but produces
// a *TSType tree directly.  It handles reset, alias resolution, TopRef,
// and definition gathering.
func (g *Generator) extractTSTypeForSymbol(sym *ast.Symbol) (*TSType, error) {
	if sym == nil {
		return nil, fmt.Errorf("toolbox: nil symbol")
	}
	g.reset()

	result, err := g.extractSymbolTSType(sym, true)
	if err != nil {
		return nil, err
	}

	// Attach definitions gathered during extraction.
	if len(g.definitions) > 0 {
		rootName := g.outputNameForSymbol(sym)
		defsOnly := false
		if _, rootInDefs := g.definitions[rootName]; rootInDefs && isModuleQualifiedSymbol(sym) && !g.opts.TopRef {
			result = &TSType{Kind: TSTypeAny}
			defsOnly = true
		}
		if defsOnly {
			for name, def := range g.definitions {
				if _, hasID := def["id"]; !hasID {
					def["id"] = name
				}
			}
		}
		result.Definitions = make(map[string]*TSType, len(g.definitions))
		for name, def := range g.definitions {
			result.Definitions[name] = schemaMapToTSType(def)
		}
	}

	result.SchemaURI = "http://json-schema.org/draft-07/schema#"
	if g.opts.ID != "" {
		result.ID = g.opts.ID
	}
	return result, nil
}

// extractSymbolTSType parallels generateSymbolSchema but returns a TSType.
func (g *Generator) extractSymbolTSType(sym *ast.Symbol, root bool) (*TSType, error) {
	if override, ok := g.overrides[sym.Name]; ok && root {
		schema := cloneSchema(override)
		schema["$schema"] = "http://json-schema.org/draft-07/schema#"
		return schemaToTSType(schema), nil
	}

	t := g.declaredTypeForSymbol(sym)
	if t == nil {
		return &TSType{Kind: TSTypeAny}, nil
	}
	node := canonicalDeclaration(sym)

	// ReturnType resolution (same as generateSymbolSchema).
	if t.Flags()&checker.TypeFlagsAny != 0 && sym.Flags&ast.SymbolFlagsTypeAlias != 0 && node != nil {
		if typeNode := declaredTypeNode(node); typeNode != nil && typeNode.Kind == ast.KindTypeReference {
			refName := entityNameText(typeNode.AsTypeReferenceNode().TypeName)
			if refName == "ReturnType" && len(typeNode.TypeArguments()) == 1 {
				arg := typeNode.TypeArguments()[0]
				if argType := g.checker.GetTypeAtLocation(arg); argType != nil {
					sigs := g.checker.GetSignaturesOfType(argType, checker.SignatureKindCall)
					if len(sigs) > 0 {
						ret := g.checker.GetReturnTypeOfSignature(sigs[0])
						if ret != nil && ret.Flags()&checker.TypeFlagsAny == 0 {
							t = ret
						}
					}
				}
			}
		}
	}

	// Type alias resolution (same as generateSymbolSchema).
	if root && sym.Flags&ast.SymbolFlagsTypeAlias != 0 {
		if !g.opts.TopRef {
			if targetSym := g.aliasTargetSymbol(sym); targetSym != nil {
				return g.extractSymbolTSType(targetSym, root)
			}
			if typeNode := declaredTypeNode(node); typeNode != nil && typeNode.Kind == ast.KindTypeReference {
				refName := entityNameText(typeNode.AsTypeReferenceNode().TypeName)
				if isUtilityTypeName(refName) {
					if t != nil && t.Flags()&checker.TypeFlagsObject != 0 {
						result, err := g.extractObjectTSType(t, nil, node)
						if err != nil {
							return nil, err
						}
						return result, nil
					}
					if resolved := g.checker.GetTypeAtLocation(typeNode); resolved != nil && resolved != t {
						schema, err := g.concreteTypeSchema(resolved, node)
						if err != nil {
							return nil, err
						}
						if len(schema) > 0 {
							return schemaToTSType(schema), nil
						}
					}
				}
			}
		} else if !g.opts.AliasRef {
			if targetSym := g.aliasTargetSymbol(sym); targetSym != nil {
				return g.extractSymbolTSType(targetSym, root)
			}
		}
	}

	// TopRef handling (same as generateSymbolSchema).
	if root && g.shouldTopRefSymbol(sym) {
		name := g.outputNameForSymbol(sym)
		if _, ok := g.definitions[name]; !ok {
			g.inProgress[name] = true
			def, err := g.emitType(t, sym, node)
			delete(g.inProgress, name)
			if err != nil {
				return nil, err
			}
			if def == nil {
				def = Schema{}
			}
			g.definitions[name] = def
		}
		rootType := &TSType{Kind: TSTypeRef, Ref: g.refURI(name)}
		if docs := g.parseDocs(node); docs != nil {
			ann := g.collectAnnotations(node, sym, false)
			if ann != nil {
				rootType.Annotations = ann
			}
		}
		return rootType, nil
	}

	// Extends target resolution (same as generateSymbolSchema).
	if root && !g.opts.TopRef {
		if targetSym := extendsTargetSymbol(g.checker, node); targetSym != nil && hasNoOwnMembers(node) {
			targetType := g.declaredTypeForSymbol(targetSym)
			targetNode := canonicalDeclaration(targetSym)
			return g.typeTSType(targetType, targetSym, targetNode, root)
		}
	}

	result, err := g.typeTSType(t, sym, node, root)
	if err != nil {
		return nil, err
	}
	if result == nil {
		result = &TSType{Kind: TSTypeAny}
	}
	return result, nil
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
		case jsnum.Number:
			result.LiteralValue = float64(v)
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

	// Tuple types (which are object types with ObjectFlagsTuple).
	// Detect tuples before the generic object path so that inline
	// tuple literals (e.g. function params typed as [string, number])
	// are extracted as TSTypeTuple rather than TSTypeObject with
	// numeric properties.
	if t.Flags()&checker.TypeFlagsObject != 0 && checker.IsTupleType(t) {
		return g.extractTupleTSType(t, result)
	}

	// Object types.
	if t.Flags()&checker.TypeFlagsObject != 0 {
		return g.extractObjectTSType(t, sym, node)
	}

	// Fallback: empty schema = any.
	result.Kind = TSTypeAny
	return result, nil
}

// typeTSType parallels typeSchema but returns a *TSType.  It handles
// $ref creation and definition population: when a type should be emitted
// as a definition, the definition is populated via emitType (which stores
// it as a Schema) and a TSTypeRef node is returned.  For inline types,
// it delegates to extractTSType.
func (g *Generator) typeTSType(t *checker.Type, sym *ast.Symbol, node *ast.Node, root bool) (*TSType, error) {
	if t == nil {
		return &TSType{Kind: TSTypeAny}, nil
	}
	if typeNode := declaredTypeNode(node); isBuiltinDateTypeNode(g.checker, typeNode) {
		sym = nil
	}
	if t.Flags()&checker.TypeFlagsNever != 0 {
		if root {
			return nil, fmt.Errorf("Unsupported type: never")
		}
		return nil, nil
	}
	if t.Flags()&checker.TypeFlagsUndefined != 0 || t.Flags()&checker.TypeFlagsVoid != 0 {
		if root {
			return nil, fmt.Errorf("Not supported: root type undefined")
		}
		return nil, nil
	}
	if node != nil {
		if docs := g.parseDocs(node); docs != nil {
			if docs.ignore {
				return nil, nil
			}
		}
	}

	if sym != nil && g.shouldRefSymbol(sym) && !root {
		name := g.outputNameForSymbol(sym)
		nullable := g.isNullableAlias(sym)
		if g.inProgress[name] {
			ref := &TSType{Kind: TSTypeRef, Ref: g.refURI(name), Nullable: nullable}
			return ref, nil
		}
		if _, ok := g.definitions[name]; ok {
			ref := &TSType{Kind: TSTypeRef, Ref: g.refURI(name), Nullable: nullable}
			return ref, nil
		}
		if override, ok := g.overrides[sym.Name]; ok {
			g.definitions[name] = cloneSchema(override)
			return &TSType{Kind: TSTypeRef, Ref: g.refURI(name)}, nil
		}
		g.inProgress[name] = true
		defNode := canonicalDeclaration(sym)
		if defNode == nil {
			defNode = node
		}
		def, err := g.emitType(t, sym, defNode)
		delete(g.inProgress, name)
		if err != nil {
			return nil, err
		}
		if def != nil {
			g.definitions[name] = def
		}
		ref := &TSType{Kind: TSTypeRef, Ref: g.refURI(name), Nullable: nullable}
		return ref, nil
	}

	result, err := g.extractTSType(t, sym, node)
	if err != nil {
		return nil, err
	}
	if !root && g.isNullableAlias(sym) {
		result.Nullable = true
	}
	return result, err
}

// extractUnionTSType extracts a union type by walking each member directly,
// preserving TS-level union structure.  Literal members are kept as
// TSTypeLiteral nodes so the union identity is visible at the TSType level.
func (g *Generator) extractUnionTSType(t *checker.UnionType, sym *ast.Symbol, node *ast.Node, ann *TSAnnotations) (*TSType, error) {
	var literalTypes []*TSType
	var simpleTypes []string
	var complexBranches []*TSType
	hasNull := false

	var memberNodes []*ast.Node
	var usedMemberNodes []bool
	if typeNode := declaredTypeNode(node); typeNode != nil && typeNode.Kind == ast.KindUnionType {
		memberNodes = typeNode.AsUnionTypeNode().Types.Nodes
		usedMemberNodes = make([]bool, len(memberNodes))
	}

	for _, mt := range t.Types() {
		if mt == nil {
			continue
		}
		memberNode := matchUnionMemberNode(g.checker, mt, memberNodes, usedMemberNodes)
		if memberNode == nil {
			memberNode = node
		}
		memberSym := namedRefSymbol(g.checker, mt)
		if memberSym == nil {
			memberSym = symbolForTypeNode(g.checker, declaredTypeNode(memberNode))
		}

		if mt.Flags()&checker.TypeFlagsNever != 0 {
			continue
		}
		// Without strictNullChecks, null is implicitly part of every type.
		if !g.strictNullChecks && mt.Flags()&checker.TypeFlagsNull != 0 {
			continue
		}
		// Track null for nullable detection (strictNullChecks mode).
		if mt.Flags()&checker.TypeFlagsNull != 0 {
			hasNull = true
			continue
		}
		// Literal types → TSTypeLiteral children.
		if mt.Flags()&(checker.TypeFlagsStringLiteral|checker.TypeFlagsNumberLiteral|checker.TypeFlagsBooleanLiteral|checker.TypeFlagsBigIntLiteral) != 0 {
			lit := mt.AsLiteralType()
			val := lit.Value()
			child := &TSType{Kind: TSTypeLiteral, LiteralValue: val}
			switch v := val.(type) {
			case string:
				child.PrimitiveType = "string"
			case bool:
				child.PrimitiveType = "boolean"
			case float64:
				child.PrimitiveType = g.numberType()
			case jsnum.Number:
				child.LiteralValue = float64(v)
				child.PrimitiveType = g.numberType()
			}
			literalTypes = append(literalTypes, child)
			continue
		}
		// Simple primitive types.
		if mt.Flags()&checker.TypeFlagsString != 0 {
			simpleTypes = append(simpleTypes, "string")
			continue
		}
		if mt.Flags()&checker.TypeFlagsBoolean != 0 {
			simpleTypes = append(simpleTypes, "boolean")
			continue
		}
		if mt.Flags()&checker.TypeFlagsNumber != 0 {
			simpleTypes = append(simpleTypes, g.numberType())
			continue
		}
		if mt.Flags()&checker.TypeFlagsNull != 0 {
			simpleTypes = append(simpleTypes, "null")
			continue
		}
		// Complex types → go through typeTSType for ref handling.
		child, err := g.typeTSType(mt, memberSym, memberNode, false)
		if err != nil {
			return nil, err
		}
		if child == nil {
			continue
		}
		complexBranches = append(complexBranches, child)
	}

	// Collect enum values from literal types.
	var enumVals []any
	for _, lit := range literalTypes {
		enumVals = append(enumVals, lit.LiteralValue)
	}

	// When boolean appears alongside other literal values, decompose.
	if len(enumVals) > 0 && containsStr(simpleTypes, "boolean") {
		enumVals = append(enumVals, true, false)
		literalTypes = append(literalTypes, &TSType{Kind: TSTypeLiteral, LiteralValue: true, PrimitiveType: "boolean"})
		literalTypes = append(literalTypes, &TSType{Kind: TSTypeLiteral, LiteralValue: false, PrimitiveType: "boolean"})
		simpleTypes = removeStr(simpleTypes, "boolean")
	}

	// When simple types subsume all enum values, drop the subsumed values.
	if len(enumVals) > 0 && len(simpleTypes) > 0 {
		filtered := filterSubsumedEnumVals(enumVals, simpleTypes)
		if len(filtered) < len(enumVals) {
			// Rebuild literalTypes to only contain non-subsumed literals.
			subsumes := map[string]bool{}
			for _, st := range simpleTypes {
				subsumes[st] = true
			}
			var kept []*TSType
			for _, lit := range literalTypes {
				switch lit.LiteralValue.(type) {
				case string:
					if subsumes["string"] {
						continue
					}
				case bool:
					if subsumes["boolean"] {
						continue
					}
				case float64:
					if subsumes["number"] || subsumes["integer"] {
						continue
					}
				}
				kept = append(kept, lit)
			}
			literalTypes = kept
			enumVals = filtered
		}
	}

	// Build the result TSType based on what we have.
	result := &TSType{Annotations: ann}

	// Case 1: Only literals (no simple types, no complex branches).
	if len(literalTypes) > 0 && len(complexBranches) == 0 && len(simpleTypes) == 0 {
		// Boolean pair: true | false → TSTypePrimitive("boolean").
		if allSameType(enumVals, "bool") && len(enumVals) == 2 {
			result.Kind = TSTypePrimitive
			result.PrimitiveType = "boolean"
			if hasNull {
				result.Nullable = true
			}
			return result, nil
		}
		// Single literal: just return the literal directly.
		if len(literalTypes) == 1 && !g.useEnumFormat() {
			result.Kind = TSTypeLiteral
			result.LiteralValue = literalTypes[0].LiteralValue
			result.PrimitiveType = literalTypes[0].PrimitiveType
			if hasNull {
				result.Nullable = true
			}
			return result, nil
		}
		// Multiple literals: TSTypeUnion with TSTypeLiteral children.
		result.Kind = TSTypeUnion
		result.Types = literalTypes
		result.CollapseLiterals = true
		if hasNull {
			result.Nullable = true
		}
		return result, nil
	}

	// Case 2: Literals coexist with simple types or complex branches.
	if len(literalTypes) > 0 && (len(simpleTypes) > 0 || len(complexBranches) > 0) {
		// Wrap literals as an enum TSType in a union with the other branches.
		enumNode := &TSType{Kind: TSTypeEnum, EnumValues: enumVals}
		branches := []*TSType{enumNode}
		branches = append(branches, complexBranches...)
		// Simple types get added as primitive branches too.
		for _, st := range uniqueStringsStable(simpleTypes) {
			branches = append(branches, &TSType{Kind: TSTypePrimitive, PrimitiveType: st})
		}
		// If there are simple types, the union also carries the type field.
		if len(simpleTypes) > 0 {
			simpleTypes = uniqueStringsStable(simpleTypes)
			sort.Strings(simpleTypes)
		}
		result.Kind = TSTypeUnion
		result.Types = branches
		if hasNull {
			result.Nullable = true
		}
		return result, nil
	}

	// Case 3: Only simple types (no literals, no complex branches).
	if len(simpleTypes) > 0 && len(complexBranches) == 0 {
		simpleTypes = uniqueStringsStable(simpleTypes)
		sort.Strings(simpleTypes)
		if len(simpleTypes) == 1 {
			result.Kind = TSTypePrimitive
			result.PrimitiveType = simpleTypes[0]
		} else {
			result.Kind = TSTypePrimitive
			types := make([]any, len(simpleTypes))
			for i, st := range simpleTypes {
				types[i] = st
			}
			result.ExtraFields = map[string]any{"type": types}
		}
		if hasNull {
			result.Nullable = true
		}
		return result, nil
	}

	// Case 4: Only complex branches (or mix of simple + complex).
	if len(complexBranches) > 0 {
		// Merge simple types into the anyOf structure.
		if len(simpleTypes) > 0 {
			simpleTypes = uniqueStringsStable(simpleTypes)
			sort.Strings(simpleTypes)
			simpleNode := &TSType{Kind: TSTypePrimitive}
			if len(simpleTypes) == 1 {
				simpleNode.PrimitiveType = simpleTypes[0]
			} else {
				types := make([]any, len(simpleTypes))
				for i, st := range simpleTypes {
					types[i] = st
				}
				simpleNode.ExtraFields = map[string]any{"type": types}
			}
			complexBranches = append(complexBranches, simpleNode)
		}
		if len(complexBranches) == 1 {
			result = complexBranches[0]
			if ann != nil {
				result.Annotations = ann
			}
			if hasNull {
				result.Nullable = true
			}
			return result, nil
		}
		result.Kind = TSTypeUnion
		result.Types = complexBranches
		if hasNull {
			result.Nullable = true
		}
		return result, nil
	}

	// Case 5: Empty union (all members were filtered out).
	result.Kind = TSTypeAny
	if hasNull {
		result.Nullable = true
	}
	return result, nil
}

// extractIntersectionTSType extracts an intersection type by walking each
// member directly via extractTSType.
func (g *Generator) extractIntersectionTSType(t *checker.IntersectionType, sym *ast.Symbol, node *ast.Node, ann *TSAnnotations) (*TSType, error) {
	members := t.Types()

	// Try the merged intersection path first (same logic as intersectionSchema).
	if merged, ok, err := g.mergeIntersectionMembers(members, sym, node); err != nil {
		return nil, err
	} else if ok {
		result := schemaToTSType(merged)
		if ann != nil {
			result.Annotations = ann
		}
		return result, nil
	}

	if g.opts.NoExtraProps {
		// With NoExtraProps, merge all intersection members into a single object.
		schema := Schema{}
		for _, mt := range members {
			sub, err := g.emitType(mt, sym, node)
			if err != nil {
				return nil, err
			}
			if sub == nil {
				continue
			}
			if props, ok := sub["properties"].(map[string]any); ok {
				if schema["properties"] == nil {
					schema["properties"] = map[string]any{}
				}
				if dst, ok2 := schema["properties"].(map[string]any); ok2 {
					for k, v := range props {
						dst[k] = v
					}
				}
			}
			if req, ok := sub["required"].([]string); ok {
				schema["required"] = uniqueStrings(append(asStrings(schema["required"]), req...))
			} else if reqAny, ok := sub["required"].([]any); ok {
				schema["required"] = uniqueStrings(append(asStrings(schema["required"]), anyToStrings(reqAny)...))
			}
			if t, ok := sub["type"].(string); ok {
				schema["type"] = t
			}
			if ap, ok := sub["additionalProperties"]; ok {
				schema["additionalProperties"] = ap
			}
		}
		if schema["type"] == nil {
			schema["type"] = "object"
		}
		if schema["additionalProperties"] == nil {
			schema["additionalProperties"] = false
		}
		result := schemaToTSType(schema)
		if ann != nil {
			result.Annotations = ann
		}
		return result, nil
	}

	// General case: build allOf with each member.
	var branches []*TSType
	for _, mt := range members {
		child, err := g.typeTSType(mt, sym, node, false)
		if err != nil {
			return nil, err
		}
		if child != nil {
			branches = append(branches, child)
		}
	}

	result := &TSType{
		Kind:        TSTypeIntersection,
		Types:       branches,
		Annotations: ann,
	}
	return result, nil
}

// extractTupleTSType extracts a tuple type directly from checker type
// arguments and element flags, producing a TSTypeTuple node.
// This is called when IsTupleType(t) returns true, which covers both
// named tuple type aliases and inline tuple literals in function params.
func (g *Generator) extractTupleTSType(t *checker.Type, result *TSType) (*TSType, error) {
	tupleType := t.TargetTupleType()
	typeArgs := g.checker.GetTypeArguments(t)
	elementFlags := tupleType.ElementFlags()

	result.Kind = TSTypeTuple

	minItems := 0
	for i, arg := range typeArgs {
		var flags checker.ElementFlags
		if i < len(elementFlags) {
			flags = elementFlags[i]
		}

		if flags&checker.ElementFlagsRest != 0 {
			// Rest element: ...T[] → AdditionalItems
			elemType := arg
			// For rest elements, the type argument is already the element type
			// (not the array type), so we can use it directly.
			restSchema, err := g.extractTSType(elemType, nil, nil)
			if err != nil {
				return nil, err
			}
			result.AdditionalItems = restSchema
			continue
		}

		elemSchema, err := g.extractTSType(arg, nil, nil)
		if err != nil {
			return nil, err
		}
		result.TupleItems = append(result.TupleItems, elemSchema)

		if flags&checker.ElementFlagsRequired != 0 || flags == 0 {
			minItems = i + 1
		}
	}

	if minItems < len(result.TupleItems) {
		result.MinItems = &minItems
	}

	return result, nil
}

// extractObjectTSType extracts an object type by walking its properties
// directly from the checker.
func (g *Generator) extractObjectTSType(t *checker.Type, sym *ast.Symbol, node *ast.Node) (*TSType, error) {
	schema, err := g.objectSchema(t, sym, node)
	if err != nil {
		return nil, err
	}
	return schemaToTSType(schema), nil
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

