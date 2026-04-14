package toolbox

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/checker"
	"github.com/microsoft/typescript-go/internal/jsnum"
)

// ExtractTSType uses the existing Generator to extract a tsType for a named
// symbol.  This calls extractTSType directly from the checker types rather
// than going through the JSON Schema path.
func (g *Generator) ExtractTSType(ctx context.Context, name string) (*tsType, error) {
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

func (g *Generator) annotateCannotJSONInfo(out *tsType, t *checker.Type, node *ast.Node) *tsType {
	if out == nil || t == nil {
		return out
	}
	if out.CannotJSONKind == cannotJSONNone {
		out.CannotJSONKind = g.cannotJSONKindForType(t, node)
	}
	return out
}

func (g *Generator) cannotJSONKindForType(t *checker.Type, node *ast.Node) cannotJSONKind {
	if t == nil {
		return cannotJSONNone
	}
	if isBuiltinDateType(g.checker, t, node) {
		return cannotJSONDate
	}
	if g.checker.TypeHasCallOrConstructSignatures(t) {
		return cannotJSONFunction
	}
	switch {
	case t.Flags()&checker.TypeFlagsAny != 0:
		return cannotJSONAny
	case t.Flags()&checker.TypeFlagsUnknown != 0:
		return cannotJSONUnknown
	case t.Flags()&checker.TypeFlagsBigInt != 0 || t.Flags()&checker.TypeFlagsBigIntLiteral != 0:
		return cannotJSONBigInt
	case t.Flags()&checker.TypeFlagsESSymbol != 0 || t.Flags()&checker.TypeFlagsUniqueESSymbol != 0:
		return cannotJSONSymbol
	}
	name := strings.TrimSpace(g.typeString(t))
	if name == "object" {
		return cannotJSONObject
	}
	name = trimOuterParens(name)
	if i := strings.IndexByte(name, '<'); i >= 0 {
		name = name[:i]
	}
	switch strings.TrimSpace(name) {
	case "Function":
		return cannotJSONFunction
	case "Promise":
		return cannotJSONPromise
	case "Map", "ReadonlyMap":
		return cannotJSONMap
	case "Set", "ReadonlySet":
		return cannotJSONSet
	case "WeakMap":
		return cannotJSONWeakMap
	case "WeakSet":
		return cannotJSONWeakSet
	case "RegExp":
		return cannotJSONRegExp
	case "Error":
		return cannotJSONError
	case "ArrayBuffer":
		return cannotJSONArrayBuffer
	case "DataView":
		return cannotJSONDataView
	case "Int8Array":
		return cannotJSONInt8Array
	case "Uint8Array":
		return cannotJSONUint8Array
	case "Uint8ClampedArray":
		return cannotJSONUint8ClampedArray
	case "Int16Array":
		return cannotJSONInt16Array
	case "Uint16Array":
		return cannotJSONUint16Array
	case "Int32Array":
		return cannotJSONInt32Array
	case "Uint32Array":
		return cannotJSONUint32Array
	case "Float32Array":
		return cannotJSONFloat32Array
	case "Float64Array":
		return cannotJSONFloat64Array
	case "BigInt64Array":
		return cannotJSONBigInt64Array
	case "BigUint64Array":
		return cannotJSONBigUint64Array
	}
	return cannotJSONNone
}

// ExtractTSTypeForSymbol extracts a tsType for a specific symbol.
func (g *Generator) ExtractTSTypeForSymbol(sym SymbolRef) (*tsType, error) {
	return g.extractTSTypeForSymbol(sym.Symbol)
}

// extractTSTypeForSymbol parallels generateSchemaForSymbol but produces
// a *tsType tree directly.  It handles reset, alias resolution, TopRef,
// and definition gathering.
func (g *Generator) extractTSTypeForSymbol(sym *ast.Symbol) (*tsType, error) {
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
			result = &tsType{Kind: tsTypeAny}
			defsOnly = true
		}
		if defsOnly {
			for name, def := range g.definitions {
				if _, hasID := def["id"]; !hasID {
					def["id"] = name
				}
			}
		}
		if result.Definitions == nil {
			result.Definitions = make(map[string]*tsType, len(g.definitions))
			for name, def := range g.definitions {
				if tdef, ok := g.typeDefinitions[name]; ok && tdef != nil {
					result.Definitions[name] = tdef
					continue
				}
				result.Definitions[name] = schemaMapToTSType(def)
			}
		} else {
			for name := range result.Definitions {
				if tdef, ok := g.typeDefinitions[name]; ok && tdef != nil {
					result.Definitions[name] = tdef
					continue
				}
				if def, ok := g.definitions[name]; ok {
					result.Definitions[name] = schemaMapToTSType(def)
				}
			}
		}
	}

	result.SchemaURI = "http://json-schema.org/draft-07/schema#"
	if g.opts.ID != "" {
		result.ID = g.opts.ID
	}
	dropUnreferencedGenericBaseDefinitions(result)
	dropUnreferencedBuiltinDefinitions(result, "Array", "ReadonlyArray")
	return result, nil
}

// extractSymbolTSType parallels generateSymbolSchema but returns a tsType.
func (g *Generator) extractSymbolTSType(sym *ast.Symbol, root bool) (*tsType, error) {
	if override, ok := g.overrides[sym.Name]; ok && root {
		schema := cloneSchema(override)
		schema["$schema"] = "http://json-schema.org/draft-07/schema#"
		return schemaToTSType(schema), nil
	}

	t := g.declaredTypeForSymbol(sym)
	if t == nil {
		return &tsType{Kind: tsTypeAny}, nil
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
		rootType := &tsType{Kind: tsTypeRef, Ref: g.refURI(name)}
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
		result = &tsType{Kind: tsTypeAny}
	}
	return result, nil
}

// extractTSType walks a checker.Type directly to produce a tsType tree.
// This parallels emitType but outputs tsType nodes instead of map[string]any.
func (g *Generator) extractTSType(t *checker.Type, sym *ast.Symbol, node *ast.Node) (*tsType, error) {
	if t == nil {
		return &tsType{Kind: tsTypeAny}, nil
	}

	result := &tsType{}

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
		result.Kind = tsTypePrimitive
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
					return g.annotateCannotJSONInfo(schemaToTSType(overlayParsedSchema(parsed, g.annotationsToSchema(ann))), t, node), nil
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
						return g.annotateCannotJSONInfo(schemaToTSType(overlayParsedSchema(parsed, annSchema)), t, node), nil
					}
				}
				return g.annotateCannotJSONInfo(schemaToTSType(overlayParsedSchema(parsed, g.annotationsToSchema(ann))), t, node), nil
			}
		}
		if shouldPreferTypeNodeSchema(g.checker, t, sym) {
			parsed, ok, err := g.schemaFromTypeNode(typeNode)
			if err != nil {
				return nil, err
			}
			if ok {
				return g.annotateCannotJSONInfo(schemaToTSType(overlayParsedSchema(parsed, g.annotationsToSchema(ann))), t, node), nil
			}
		}
	}
	if targetSym := extendsTargetSymbol(g.checker, node); targetSym != nil && hasNoOwnMembers(node) {
		schema, err := g.refSchemaForSymbol(targetSym)
		if err != nil {
			return nil, err
		}
		return g.annotateCannotJSONInfo(schemaToTSType(schema), t, node), nil
	}

	// Literal types from enum members/declarations.
	if isEnumMemberSymbol(sym) || (node != nil && node.Kind == ast.KindEnumMember) {
		schema, err := g.enumMemberSchema(sym, node)
		if err != nil {
			return nil, err
		}
		return g.annotateCannotJSONInfo(schemaToTSType(schema), t, node), nil
	}
	if isEnumSymbol(sym) || (node != nil && node.Kind == ast.KindEnumDeclaration) {
		schema, err := g.enumSchema(sym, node)
		if err != nil {
			return nil, err
		}
		return g.annotateCannotJSONInfo(schemaToTSType(schema), t, node), nil
	}

	if isBuiltinDateType(g.checker, t, node) {
		result.Kind = tsTypePrimitive
		result.PrimitiveType = "string"
		result.Format = "date-time"
		return g.annotateCannotJSONInfo(result, t, node), nil
	}

	// String/number/boolean/bigint literal types.
	if t.Flags()&(checker.TypeFlagsStringLiteral|checker.TypeFlagsNumberLiteral|checker.TypeFlagsBooleanLiteral|checker.TypeFlagsBigIntLiteral) != 0 {
		lit := t.AsLiteralType()
		val := lit.Value()
		result.Kind = tsTypeLiteral
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
		return g.annotateCannotJSONInfo(result, t, node), nil
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
		result.Kind = tsTypePrimitive
		if docTypeOverride == "" {
			result.PrimitiveType = "string"
		}
		if pattern := templateLiteralPattern(t); pattern != "" {
			result.Pattern = pattern
		}
		return g.annotateCannotJSONInfo(result, t, node), nil
	}

	// Primitive types.
	if t.Flags()&checker.TypeFlagsString != 0 {
		result.Kind = tsTypePrimitive
		if docTypeOverride == "" {
			result.PrimitiveType = "string"
		} else {
			result.PrimitiveType = docTypeOverride
		}
		return g.annotateCannotJSONInfo(result, t, node), nil
	}
	if t.Flags()&checker.TypeFlagsBoolean != 0 {
		result.Kind = tsTypePrimitive
		if docTypeOverride == "" {
			result.PrimitiveType = "boolean"
		} else {
			result.PrimitiveType = docTypeOverride
		}
		return g.annotateCannotJSONInfo(result, t, node), nil
	}
	if t.Flags()&checker.TypeFlagsNumber != 0 {
		result.Kind = tsTypePrimitive
		if docTypeOverride == "" {
			result.PrimitiveType = g.numberType()
		} else {
			result.PrimitiveType = docTypeOverride
		}
		return g.annotateCannotJSONInfo(result, t, node), nil
	}
	if t.Flags()&checker.TypeFlagsBigInt != 0 {
		result.Kind = tsTypePrimitive
		if docTypeOverride == "" {
			result.PrimitiveType = g.numberType()
		} else {
			result.PrimitiveType = docTypeOverride
		}
		return g.annotateCannotJSONInfo(result, t, node), nil
	}
	if t.Flags()&checker.TypeFlagsNull != 0 {
		result.Kind = tsTypePrimitive
		if docTypeOverride == "" {
			result.PrimitiveType = "null"
		} else {
			result.PrimitiveType = docTypeOverride
		}
		return g.annotateCannotJSONInfo(result, t, node), nil
	}
	if strings.TrimSpace(g.typeString(t)) == "object" {
		result.Kind = tsTypeObject
		result.WildcardObject = true
		return g.annotateCannotJSONInfo(result, t, node), nil
	}
	if t.Flags()&checker.TypeFlagsESSymbol != 0 || t.Flags()&checker.TypeFlagsUniqueESSymbol != 0 {
		result.Kind = tsTypePrimitive
		if docTypeOverride == "" {
			result.PrimitiveType = "object"
		} else {
			result.PrimitiveType = docTypeOverride
		}
		return g.annotateCannotJSONInfo(result, t, node), nil
	}

	// Any/unknown.
	if t.Flags()&checker.TypeFlagsAny != 0 || t.Flags()&checker.TypeFlagsUnknown != 0 {
		if parsed, ok, err := g.schemaFromTypeString(g.typeString(t), node); err != nil {
			return nil, err
		} else if ok {
			return g.annotateCannotJSONInfo(schemaToTSType(parsed), t, node), nil
		}
		result.Kind = tsTypeAny
		return g.annotateCannotJSONInfo(result, t, node), nil
	}

	// Tuple types (which are object types with ObjectFlagsTuple).
	// Detect tuples before the generic object path so that inline
	// tuple literals (e.g. function params typed as [string, number])
	// are extracted as tsTypeTuple rather than tsTypeObject with
	// numeric properties.
	if t.Flags()&checker.TypeFlagsObject != 0 && checker.IsTupleType(t) {
		return g.extractTupleTSType(t, result)
	}

	// Object types.
	if t.Flags()&checker.TypeFlagsObject != 0 {
		return g.extractObjectTSType(t, sym, node)
	}

	// Fallback: empty schema = any.
	result.Kind = tsTypeAny
	return g.annotateCannotJSONInfo(result, t, node), nil
}

// typeTSType parallels typeSchema but returns a *tsType.  It handles
// $ref creation and definition population: when a type should be emitted
// as a definition, the definition is populated via emitType (which stores
// it as a Schema) and a tsTypeRef node is returned.  For inline types,
// it delegates to extractTSType.
func (g *Generator) typeTSType(t *checker.Type, sym *ast.Symbol, node *ast.Node, root bool) (*tsType, error) {
	if t == nil {
		return &tsType{Kind: tsTypeAny}, nil
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
		if typeNode := declaredTypeNode(node); typeNode != nil && typeNode.Kind == ast.KindTypeReference && len(typeNode.TypeArguments()) > 0 {
			typeRefName := entityNameText(typeNode.AsTypeReferenceNode().TypeName)
			if !isLibSymbol(sym) && !isUtilityTypeName(typeRefName) {
				if defName := resolveAliasRefName(g.checker, typeNode); defName != "" {
					name = defName
				} else {
					name = typeNodeString(typeNode)
				}
			}
		}
		nullable := g.isNullableAlias(sym)
		if g.inProgress[name] {
			ref := &tsType{Kind: tsTypeRef, Ref: g.refURI(name), Nullable: nullable}
			return ref, nil
		}
		if _, ok := g.definitions[name]; ok {
			ref := &tsType{Kind: tsTypeRef, Ref: g.refURI(name), Nullable: nullable}
			return ref, nil
		}
		if _, ok := g.typeDefinitions[name]; ok {
			ref := &tsType{Kind: tsTypeRef, Ref: g.refURI(name), Nullable: nullable}
			return ref, nil
		}
		if override, ok := g.overrides[sym.Name]; ok {
			g.definitions[name] = cloneSchema(override)
			g.typeDefinitions[name] = schemaToTSType(override)
			return &tsType{Kind: tsTypeRef, Ref: g.refURI(name)}, nil
		}
		g.inProgress[name] = true
		defNode := canonicalDeclaration(sym)
		if defNode == nil {
			defNode = node
		}
		def, err := g.extractTSType(t, sym, defNode)
		delete(g.inProgress, name)
		if err != nil {
			return nil, err
		}
		if def != nil {
			g.typeDefinitions[name] = def
			g.definitions[name] = tsTypeToJSON(def)
		}
		ref := &tsType{Kind: tsTypeRef, Ref: g.refURI(name), Nullable: nullable}
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
// tsTypeLiteral nodes so the union identity is visible at the tsType level.
func (g *Generator) extractUnionTSType(t *checker.UnionType, sym *ast.Symbol, node *ast.Node, ann *tsAnnotations) (*tsType, error) {
	var literalTypes []*tsType
	var simpleTypes []string
	var complexBranches []*tsType
	hasNull := false
	hasUndefined := false

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
		// Literal types → tsTypeLiteral children.
		if mt.Flags()&(checker.TypeFlagsStringLiteral|checker.TypeFlagsNumberLiteral|checker.TypeFlagsBooleanLiteral|checker.TypeFlagsBigIntLiteral) != 0 {
			lit := mt.AsLiteralType()
			val := lit.Value()
			child := &tsType{Kind: tsTypeLiteral, LiteralValue: val}
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
		if mt.Flags()&checker.TypeFlagsUndefined != 0 || mt.Flags()&checker.TypeFlagsVoid != 0 {
			hasUndefined = true
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
		literalTypes = append(literalTypes, &tsType{Kind: tsTypeLiteral, LiteralValue: true, PrimitiveType: "boolean"})
		literalTypes = append(literalTypes, &tsType{Kind: tsTypeLiteral, LiteralValue: false, PrimitiveType: "boolean"})
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
			var kept []*tsType
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

	// Build the result tsType based on what we have.
	result := &tsType{
		Annotations:       ann,
		IncludesUndefined: hasUndefined,
	}

	// Case 1: Only literals (no simple types, no complex branches).
	if len(literalTypes) > 0 && len(complexBranches) == 0 && len(simpleTypes) == 0 {
		// Boolean pair: true | false → tsTypePrimitive("boolean").
		if allSameType(enumVals, "bool") && len(enumVals) == 2 {
			result.Kind = tsTypePrimitive
			result.PrimitiveType = "boolean"
			if hasNull {
				result.Nullable = true
			}
			return result, nil
		}
		// Single literal: just return the literal directly.
		if len(literalTypes) == 1 && !g.useEnumFormat() {
			result.Kind = tsTypeLiteral
			result.LiteralValue = literalTypes[0].LiteralValue
			result.PrimitiveType = literalTypes[0].PrimitiveType
			if hasNull {
				result.Nullable = true
			}
			return result, nil
		}
		// Multiple literals: tsTypeUnion with tsTypeLiteral children.
		result.Kind = tsTypeUnion
		result.Types = literalTypes
		result.CollapseLiterals = true
		if hasNull {
			result.Nullable = true
		}
		return result, nil
	}

	// Case 2: Literals coexist with simple types or complex branches.
	if len(literalTypes) > 0 && (len(simpleTypes) > 0 || len(complexBranches) > 0) {
		// Wrap literals as an enum tsType in a union with the other branches.
		enumNode := &tsType{Kind: tsTypeEnum, EnumValues: enumVals}
		branches := []*tsType{enumNode}
		branches = append(branches, complexBranches...)
		// Simple types get added as primitive branches too.
		for _, st := range uniqueStringsStable(simpleTypes) {
			branches = append(branches, &tsType{Kind: tsTypePrimitive, PrimitiveType: st})
		}
		// If there are simple types, the union also carries the type field.
		if len(simpleTypes) > 0 {
			simpleTypes = uniqueStringsStable(simpleTypes)
			sort.Strings(simpleTypes)
		}
		result.Kind = tsTypeUnion
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
			result.Kind = tsTypePrimitive
			result.PrimitiveType = simpleTypes[0]
		} else {
			result.Kind = tsTypePrimitive
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
			simpleNode := &tsType{Kind: tsTypePrimitive}
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
			result.IncludesUndefined = hasUndefined
			return result, nil
		}
		result.Kind = tsTypeUnion
		result.Types = complexBranches
		if hasNull {
			result.Nullable = true
		}
		return result, nil
	}

	// Case 5: Empty union (all members were filtered out).
	result.Kind = tsTypeAny
	if hasNull {
		result.Nullable = true
	}
	return result, nil
}

// extractIntersectionTSType extracts an intersection type by walking each
// member directly via extractTSType.
func (g *Generator) extractIntersectionTSType(t *checker.IntersectionType, sym *ast.Symbol, node *ast.Node, ann *tsAnnotations) (*tsType, error) {
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
	var branches []*tsType
	for _, mt := range members {
		child, err := g.typeTSType(mt, sym, node, false)
		if err != nil {
			return nil, err
		}
		if child != nil {
			branches = append(branches, child)
		}
	}

	result := &tsType{
		Kind:        tsTypeIntersection,
		Types:       branches,
		Annotations: ann,
	}
	return result, nil
}

// extractTupleTSType extracts a tuple type directly from checker type
// arguments and element flags, producing a tsTypeTuple node.
// This is called when IsTupleType(t) returns true, which covers both
// named tuple type aliases and inline tuple literals in function params.
func (g *Generator) extractTupleTSType(t *checker.Type, result *tsType) (*tsType, error) {
	tupleType := t.TargetTupleType()
	typeArgs := g.checker.GetTypeArguments(t)
	elementFlags := tupleType.ElementFlags()

	result.Kind = tsTypeTuple

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
		if flags&checker.ElementFlagsOptional != 0 {
			elemSchema.IncludesUndefined = false
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
func (g *Generator) extractObjectTSType(t *checker.Type, sym *ast.Symbol, node *ast.Node) (*tsType, error) {
	schema, err := g.objectSchema(t, sym, node)
	if err != nil {
		return nil, err
	}
	out := g.annotateCannotJSONInfo(schemaToTSType(schema), t, node)
	if out == nil {
		return nil, nil
	}
	if out.Kind == tsTypeArray {
		if typeArgs := g.checker.GetTypeArguments(t); len(typeArgs) > 0 {
			item, err := g.extractTSType(typeArgs[0], nil, nil)
			if err != nil {
				return nil, err
			}
			out.Items = preserveSchemaMetadata(g.annotateCannotJSONInfo(item, typeArgs[0], nil), out.Items)
		}
		return out, nil
	}

	props := g.checker.GetPropertiesOfType(t)
	if len(props) == 0 {
		props = g.checker.GetApparentProperties(t)
	}
	if len(props) == 0 && sym != nil && isLibSymbol(sym) {
		props = extractInterfaceMembers(sym)
	}
	byName := make(map[string]*ast.Symbol, len(props))
	for _, prop := range props {
		if prop == nil {
			continue
		}
		byName[prop.Name] = prop
	}
	for i := range out.Properties {
		prop := byName[out.Properties[i].Name]
		if prop == nil {
			continue
		}
		out.Properties[i].Optional = prop.Flags&ast.SymbolFlagsOptional != 0
		propDecl := prop.ValueDeclaration
		if propDecl == nil && len(prop.Declarations) > 0 {
			propDecl = prop.Declarations[0]
		}
		propLookupNode := nodeOrFallback(node, propDecl)
		propType := g.checker.GetTypeOfSymbolAtLocation(prop, propLookupNode)
		if propType == nil {
			continue
		}
		if schemaHasTypeof(out.Properties[i].Schema) {
			out.Properties[i].Schema = g.annotateCannotJSONInfo(out.Properties[i].Schema, propType, propLookupNode)
			continue
		}
		refSym := (*ast.Symbol)(nil)
		if !shouldInlinePropertyType(propType) {
			refSym = g.refSymbolForType(propType, propDecl)
		}
		child, err := g.typeTSType(propType, refSym, propDecl, false)
		if err != nil {
			return nil, err
		}
		if out.Properties[i].Optional {
			child.IncludesUndefined = false
		}
		out.Properties[i].Schema = preserveSchemaMetadata(
			g.annotateCannotJSONInfo(child, propType, propDecl),
			out.Properties[i].Schema,
		)
	}

	for _, info := range g.checker.GetIndexInfosOfType(t) {
		if info == nil || info.ValueType() == nil {
			continue
		}
		refSym := (*ast.Symbol)(nil)
		if !shouldInlinePropertyType(info.ValueType()) {
			refSym = g.refSymbolForType(info.ValueType(), node)
		}
		child, err := g.typeTSType(info.ValueType(), refSym, node, false)
		if err != nil {
			return nil, err
		}
		child = g.annotateCannotJSONInfo(child, info.ValueType(), node)
		if info.KeyType() != nil && info.KeyType().Flags()&checker.TypeFlagsNumberLike != 0 {
			for i := range out.PatternProperties {
				out.PatternProperties[i].Schema = preserveSchemaMetadata(child, out.PatternProperties[i].Schema)
			}
			continue
		}
		out.AdditionalProperties = preserveSchemaMetadata(child, out.AdditionalProperties)
		if out.AdditionalPropertiesBool != nil && *out.AdditionalPropertiesBool {
			out.AdditionalPropertiesBool = nil
		}
	}

	return out, nil
}

func preserveSchemaMetadata(dst, src *tsType) *tsType {
	if dst == nil || src == nil {
		return dst
	}
	if src.ExtraFields != nil {
		if _, ok := src.ExtraFields["typeof"]; ok {
			return src
		}
	}
	if src.Kind == tsTypeRef && src.Ref != "" {
		return src
	}
	if (src.Kind == tsTypeUnion || src.Kind == tsTypeIntersection) && dst.Kind != src.Kind {
		return src
	}
	if src.Kind == tsTypeEnum && len(src.EnumValues) > 0 && dst.Kind != tsTypeEnum {
		return src
	}

	if src.Annotations != nil {
		dst.Annotations = src.Annotations
	}
	if src.DocTypeOverride != "" {
		dst.DocTypeOverride = src.DocTypeOverride
	}
	if src.ID != "" {
		dst.ID = src.ID
	}
	if src.Nullable {
		dst.Nullable = true
	}
	if src.PrimitiveType != "" {
		dst.PrimitiveType = src.PrimitiveType
	}
	if src.Pattern != "" {
		dst.Pattern = src.Pattern
	}
	if src.Format != "" {
		dst.Format = src.Format
	}
	if src.ExtraFields != nil {
		dst.ExtraFields = copyExtraFields(src.ExtraFields)
	}

	switch src.Kind {
	case tsTypeArray:
		if dst.Kind != tsTypeArray {
			return src
		}
		if src.Items != nil {
			dst.Items = preserveSchemaMetadata(dst.Items, src.Items)
		}
		if src.MinItems != nil {
			v := *src.MinItems
			dst.MinItems = &v
		}
		if src.MaxItems != nil {
			v := *src.MaxItems
			dst.MaxItems = &v
		}
		if src.AdditionalItems != nil {
			dst.AdditionalItems = preserveSchemaMetadata(dst.AdditionalItems, src.AdditionalItems)
		}
	case tsTypeTuple:
		if dst.Kind != tsTypeTuple {
			return src
		}
		if len(src.TupleItems) == len(dst.TupleItems) {
			for i := range src.TupleItems {
				dst.TupleItems[i] = preserveSchemaMetadata(dst.TupleItems[i], src.TupleItems[i])
			}
		}
		if src.MinItems != nil {
			v := *src.MinItems
			dst.MinItems = &v
		}
		if src.MaxItems != nil {
			v := *src.MaxItems
			dst.MaxItems = &v
		}
		if src.AdditionalItems != nil {
			dst.AdditionalItems = preserveSchemaMetadata(dst.AdditionalItems, src.AdditionalItems)
		}
	case tsTypeObject:
		if dst.Kind != tsTypeObject {
			if objectHasExplicitShape(src) {
				return src
			}
			return dst
		}
		dst.EmptyObject = src.EmptyObject
		if src.WildcardObject {
			dst.WildcardObject = true
		}
		if len(src.Required) > 0 {
			dst.Required = append([]string(nil), src.Required...)
		}
		if src.AdditionalPropertiesBool != nil {
			v := *src.AdditionalPropertiesBool
			dst.AdditionalPropertiesBool = &v
		}
		if src.AdditionalProperties != nil {
			dst.AdditionalProperties = preserveSchemaMetadata(dst.AdditionalProperties, src.AdditionalProperties)
		}
		if len(src.PatternProperties) > 0 && len(src.PatternProperties) == len(dst.PatternProperties) {
			for i := range src.PatternProperties {
				dst.PatternProperties[i].Schema = preserveSchemaMetadata(dst.PatternProperties[i].Schema, src.PatternProperties[i].Schema)
			}
		}
		if len(src.Properties) > 0 {
			byName := make(map[string]*tsType, len(src.Properties))
			for _, prop := range src.Properties {
				byName[prop.Name] = prop.Schema
			}
			for i := range dst.Properties {
				if propSchema, ok := byName[dst.Properties[i].Name]; ok {
					dst.Properties[i].Schema = preserveSchemaMetadata(dst.Properties[i].Schema, propSchema)
				}
			}
		}
	}

	return dst
}

func objectHasExplicitShape(t *tsType) bool {
	if t == nil || t.Kind != tsTypeObject {
		return false
	}
	return len(t.Properties) > 0 ||
		len(t.Required) > 0 ||
		t.AdditionalProperties != nil ||
		t.AdditionalPropertiesBool != nil ||
		len(t.PatternProperties) > 0 ||
		t.EmptyObject ||
		t.WildcardObject
}

func dropUnreferencedGenericBaseDefinitions(root *tsType) {
	if root == nil || len(root.Definitions) == 0 {
		return
	}
	refs := referencedDefinitionNames(root)
	if len(refs) == 0 {
		return
	}

	instantiatedBases := map[string]bool{}
	for name := range root.Definitions {
		if i := strings.IndexByte(name, '<'); i > 0 {
			instantiatedBases[name[:i]] = true
		}
	}
	if len(instantiatedBases) == 0 {
		return
	}

	for name := range root.Definitions {
		if refs[name] {
			continue
		}
		if !instantiatedBases[name] {
			continue
		}
		delete(root.Definitions, name)
	}
}

func dropUnreferencedDefinitions(root *tsType) {
	if root == nil || len(root.Definitions) == 0 {
		return
	}
	refs := referencedDefinitionNames(root)
	if len(refs) == 0 {
		for name := range root.Definitions {
			delete(root.Definitions, name)
		}
		return
	}
	for name := range root.Definitions {
		if !refs[name] {
			delete(root.Definitions, name)
		}
	}
}

func dropUnreferencedBuiltinDefinitions(root *tsType, names ...string) {
	if root == nil || len(root.Definitions) == 0 || len(names) == 0 {
		return
	}
	refs := referencedDefinitionNames(root)
	for _, name := range names {
		if refs[name] {
			continue
		}
		delete(root.Definitions, name)
	}
}

func referencedDefinitionNames(root *tsType) map[string]bool {
	refs := map[string]bool{}
	var visit func(*tsType)
	visit = func(t *tsType) {
		if t == nil {
			return
		}
		if t.Kind == tsTypeRef {
			name := strings.TrimPrefix(t.Ref, "#/definitions/")
			if name != t.Ref {
				if refs[name] {
					return
				}
				refs[name] = true
				visit(root.Definitions[name])
				return
			}
		}
		for _, prop := range t.Properties {
			visit(prop.Schema)
		}
		visit(t.AdditionalProperties)
		for _, prop := range t.PatternProperties {
			visit(prop.Schema)
		}
		visit(t.Items)
		for _, item := range t.TupleItems {
			visit(item)
		}
		visit(t.AdditionalItems)
		for _, child := range t.Types {
			visit(child)
		}
	}
	visit(root)
	return refs
}

func schemaHasTypeof(t *tsType) bool {
	if t == nil || t.ExtraFields == nil {
		return false
	}
	_, ok := t.ExtraFields["typeof"]
	return ok
}

func shouldInlinePropertyType(t *checker.Type) bool {
	if t == nil {
		return true
	}
	return t.Flags()&(checker.TypeFlagsAny|
		checker.TypeFlagsUnknown|
		checker.TypeFlagsBigInt|
		checker.TypeFlagsBigIntLiteral|
		checker.TypeFlagsESSymbol|
		checker.TypeFlagsUniqueESSymbol) != 0
}

func copyExtraFields(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// collectAnnotations gathers JSDoc annotations from a node and symbol into
// a tsAnnotations struct.
func (g *Generator) collectAnnotations(node *ast.Node, sym *ast.Symbol, isTypeAlias bool) *tsAnnotations {
	ann := &tsAnnotations{}
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

// annotationsToSchema converts tsAnnotations to a partial JSON Schema map
// for use with overlayParsedSchema.
func (g *Generator) annotationsToSchema(ann *tsAnnotations) Schema {
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

// schemaToTSType converts a JSON Schema (map[string]any) to a tsType tree.
// This is used internally for cases where we still delegate to the schema
// generation path (e.g. object types, utility types).
func schemaToTSType(schema map[string]any) *tsType {
	if schema == nil {
		return &tsType{Kind: tsTypeAny}
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
		t.Definitions = make(map[string]*tsType, len(defs))
		for name, def := range defs {
			if defMap, ok := def.(map[string]any); ok {
				t.Definitions[name] = schemaMapToTSType(defMap)
			}
		}
	case map[string]Schema:
		t.Definitions = make(map[string]*tsType, len(defs))
		for name, def := range defs {
			t.Definitions[name] = schemaMapToTSType(def)
		}
	}

	return t
}

// schemaMapToTSType converts a single schema node (without top-level keys)
// to a tsType.
func schemaMapToTSType(schema map[string]any) *tsType {
	if schema == nil {
		return &tsType{Kind: tsTypeAny}
	}

	t := &tsType{}

	// Extract annotations first (they can appear on any kind).
	t.Annotations = extractSchemaAnnotations(schema)
	if t.Annotations != nil && len(t.Annotations.Extra) == 0 && t.Annotations.Description == "" &&
		t.Annotations.Title == "" && t.Annotations.Comment == "" && t.Annotations.ID == "" &&
		t.Annotations.Ref == "" && !t.Annotations.HasDefault {
		t.Annotations = nil
	}

	// Check for $ref.
	if ref, ok := schema["$ref"].(string); ok {
		t.Kind = tsTypeRef
		t.Ref = ref
		extractSchemaExtraFields(t, schema)
		return t
	}

	// Check for anyOf (union).
	if anyOf, ok := schema["anyOf"].([]any); ok {
		t.Kind = tsTypeUnion
		t.Types = make([]*tsType, len(anyOf))
		for i, branch := range anyOf {
			if branchMap, ok := branch.(map[string]any); ok {
				t.Types[i] = schemaMapToTSType(branchMap)
			} else {
				t.Types[i] = &tsType{Kind: tsTypeAny}
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
		t.Kind = tsTypeIntersection
		t.Types = make([]*tsType, len(allOf))
		for i, branch := range allOf {
			if branchMap, ok := branch.(map[string]any); ok {
				t.Types[i] = schemaMapToTSType(branchMap)
			} else {
				t.Types[i] = &tsType{Kind: tsTypeAny}
			}
		}
		return t
	}

	// Check for enum (multiple values).
	if enumVals, ok := schema["enum"].([]any); ok {
		t.Kind = tsTypeEnum
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
		t.Kind = tsTypeLiteral
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
			t.Kind = tsTypeAny
			return t
		}
		t.Kind = tsTypeAny
		extractSchemaExtraFields(t, schema)
		return t
	}

	typeStr, isStr := typ.(string)
	if !isStr {
		// Multi-type: e.g. ["string", "null"].
		t.Kind = tsTypePrimitive
		// Store multi-type as ExtraFields for round-trip fidelity.
		t.ExtraFields = map[string]any{"type": typ}
		extractSchemaExtraFields(t, schema)
		return t
	}

	switch typeStr {
	case "object":
		t.Kind = tsTypeObject
		extractSchemaObjectFields(t, schema)
	case "array":
		extractSchemaArrayFields(t, schema)
	case "string":
		t.Kind = tsTypePrimitive
		t.PrimitiveType = typeStr
		if p, ok := schema["pattern"].(string); ok {
			t.Pattern = p
		}
		if f, ok := schema["format"].(string); ok {
			t.Format = f
		}
		extractSchemaExtraFields(t, schema)
	case "number", "integer", "boolean", "null":
		t.Kind = tsTypePrimitive
		t.PrimitiveType = typeStr
		extractSchemaExtraFields(t, schema)
	default:
		t.Kind = tsTypePrimitive
		t.PrimitiveType = typeStr
		extractSchemaExtraFields(t, schema)
	}

	return t
}

// extractSchemaObjectFields populates object-specific fields on a tsType.
func extractSchemaObjectFields(t *tsType, schema map[string]any) {
	if props, ok := schema["properties"]; ok {
		t.EmptyObject = true // Mark that "properties" key was present.
		if propsMap, ok := props.(map[string]any); ok {
			// Sort property names for deterministic order.
			names := make([]string, 0, len(propsMap))
			for name := range propsMap {
				names = append(names, name)
			}
			sort.Strings(names)
			t.Properties = make([]tsProperty, 0, len(names))
			for _, name := range names {
				propSchema := propsMap[name]
				if propMap, ok := propSchema.(map[string]any); ok {
					t.Properties = append(t.Properties, tsProperty{
						Name:   name,
						Schema: schemaMapToTSType(propMap),
					})
				}
			}
		}
	}

	hadRequired := false
	if req, ok := schema["required"].([]string); ok {
		t.Required = req
		hadRequired = true
	} else if req, ok := schema["required"].([]any); ok {
		t.Required = anyToStrings(req)
		hadRequired = true
	}
	if hadRequired && len(t.Properties) > 0 {
		requiredSet := make(map[string]bool, len(t.Required))
		for _, name := range t.Required {
			requiredSet[name] = true
		}
		for i := range t.Properties {
			t.Properties[i].Optional = !requiredSet[t.Properties[i].Name]
		}
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
				t.PatternProperties = append(t.PatternProperties, tsPatternProperty{
					Pattern: pattern,
					Schema:  schemaMapToTSType(propMap),
				})
			}
		}
	}

	extractSchemaExtraFields(t, schema)
}

// extractSchemaArrayFields populates array-specific fields on a tsType.
func extractSchemaArrayFields(t *tsType, schema map[string]any) {
	items := schema["items"]
	switch itemsVal := items.(type) {
	case map[string]any:
		// Homogeneous array.
		t.Kind = tsTypeArray
		t.Items = schemaMapToTSType(itemsVal)
	case []any:
		// Tuple.
		t.Kind = tsTypeTuple
		t.TupleItems = make([]*tsType, len(itemsVal))
		for i, item := range itemsVal {
			if itemMap, ok := item.(map[string]any); ok {
				t.TupleItems[i] = schemaMapToTSType(itemMap)
			} else {
				t.TupleItems[i] = &tsType{Kind: tsTypeAny}
			}
		}
	default:
		// Array with no items specified.
		t.Kind = tsTypeArray
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
func extractSchemaAnnotations(schema map[string]any) *tsAnnotations {
	ann := &tsAnnotations{}
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
func extractSchemaExtraFields(t *tsType, schema map[string]any) {
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

// isStandardSchemaKey returns true for keys that are handled by named tsType
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
