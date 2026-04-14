package toolbox

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/checker"
	"github.com/microsoft/typescript-go/internal/compiler"
	"github.com/microsoft/typescript-go/internal/jsnum"
)

type Schema = map[string]any

type SymbolRef struct {
	Name   string
	Symbol *ast.Symbol
}

type Generator struct {
	program *compiler.Program
	checker *checker.Checker
	done    func()
	opts    Options

	rootDir string

	symbolsByName map[string][]*ast.Symbol
	overrides     map[string]Schema

	definitions     map[string]Schema
	typeDefinitions map[string]*tsType
	inProgress      map[string]bool
	nullableTypes   map[*ast.Symbol]bool

	// When true, literal values should use "enum" format instead of "const".
	// Set during concrete type resolution (e.g. generic instantiations).
	inConcreteContext bool

	// Counter for anonymous interface naming (e.g. "interface-0").
	anonInterfaceCounter int
	// Set of symbols that are only referenced as alias targets.
	aliasTargetSymbols map[*ast.Symbol]bool

	// Cross-file import disambiguation: when multiple distinct symbols share
	// the same qualified name (e.g. MyInterface from different files), later
	// ones are suffixed _1, _2, etc.
	symbolOutputName map[*ast.Symbol]string
	outputNameOwner  map[string]*ast.Symbol

	// strictNullChecks caches whether the program's compiler options
	// have strictNullChecks enabled.
	strictNullChecks bool
}

func NewGenerator(program *compiler.Program, opts Options) (*Generator, error) {
	if program == nil {
		return nil, fmt.Errorf("toolbox: program is required")
	}
	ch, done := program.GetTypeChecker(context.Background())
	g := &Generator{
		program:            program,
		checker:            ch,
		done:               done,
		opts:               opts,
		rootDir:            program.GetCurrentDirectory(),
		symbolsByName:      map[string][]*ast.Symbol{},
		overrides:          map[string]Schema{},
		definitions:        map[string]Schema{},
		typeDefinitions:    map[string]*tsType{},
		inProgress:         map[string]bool{},
		nullableTypes:      map[*ast.Symbol]bool{},
		symbolOutputName:   map[*ast.Symbol]string{},
		outputNameOwner:    map[string]*ast.Symbol{},
		aliasTargetSymbols: map[*ast.Symbol]bool{},
	}
	if cwd, err := os.Getwd(); err == nil {
		g.rootDir = cwd
	}
	if compOpts := program.Options(); compOpts != nil {
		g.strictNullChecks = compOpts.StrictNullChecks.IsTrue()
	}
	g.collectSymbols()
	return g, nil
}

func (g *Generator) Close() {
	if g.done != nil {
		g.done()
		g.done = nil
	}
	g.checker = nil
}

func (g *Generator) SetSchemaOverride(name string, schema Schema) {
	if schema == nil {
		delete(g.overrides, name)
		return
	}
	g.overrides[name] = cloneSchema(schema)
}

func (g *Generator) GetSymbols(name string) []SymbolRef {
	if name == "" {
		return nil
	}
	if name == "*" {
		out := make([]SymbolRef, 0)
		for _, sym := range g.collectTopLevelSymbols() {
			out = append(out, SymbolRef{Name: g.outputNameForSymbol(sym), Symbol: sym})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return out
	}
	symbols := g.lookupSymbols(name)
	out := make([]SymbolRef, 0, len(symbols))
	for _, sym := range symbols {
		out = append(out, SymbolRef{Name: g.outputNameForSymbol(sym), Symbol: sym})
	}
	if g.opts.UniqueNames && len(out) == 1 {
		out[0].Name = name
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (g *Generator) GenerateSchemaForName(_ context.Context, name string) (Schema, error) {
	if name == "*" {
		return g.GenerateProgramSchema()
	}
	symbols := g.lookupSymbols(name)
	if len(symbols) == 0 {
		return nil, fmt.Errorf("toolbox: symbol %q not found", name)
	}
	if len(symbols) > 1 && !g.opts.UniqueNames {
		return nil, fmt.Errorf("toolbox: symbol %q resolved to %d candidates; enable unique names or use a qualified name", name, len(symbols))
	}
	if len(symbols) == 1 {
		return g.generateSchemaForSymbol(symbols[0], true)
	}
	// Multiple symbols with the same name are only used with unique-names fixtures.
	root := Schema{"$schema": "http://json-schema.org/draft-07/schema#"}
	defs := map[string]Schema{}
	for _, sym := range symbols {
		schema, err := g.generateSchemaForSymbol(sym, true)
		if err != nil {
			return nil, err
		}
		defs[g.outputNameForSymbol(sym)] = schema
	}
	root["definitions"] = defs
	return root, nil
}

func (g *Generator) GenerateProgramSchema() (Schema, error) {
	g.reset()
	root := Schema{"$schema": "http://json-schema.org/draft-07/schema#"}
	defs := map[string]Schema{}
	if id := g.opts.ID; id != "" {
		root["$id"] = id
	}
	// Temporarily remove overrides so program symbols are generated from
	// their actual declarations, not from overrides.
	savedOverrides := g.overrides
	g.overrides = map[string]Schema{}
	for _, sym := range g.collectTopLevelSymbols() {
		schema, err := g.generateSymbolSchema(sym, true)
		if err != nil {
			g.overrides = savedOverrides
			return nil, err
		}
		// Strip root-level keys that should only appear on the program
		// schema root, not on individual definitions.
		delete(schema, "$schema")
		delete(schema, "$id")
		defs[g.outputNameForSymbol(sym)] = schema
	}
	g.overrides = savedOverrides
	for name, schema := range g.overrides {
		// Only add overrides for types not already generated from the program.
		if _, exists := defs[name]; !exists {
			defs[name] = cloneSchema(schema)
		}
	}
	if len(defs) > 0 {
		root["definitions"] = defs
	}
	return root, nil
}

func (g *Generator) generateSchemaForSymbol(sym *ast.Symbol, root bool) (Schema, error) {
	if sym == nil {
		return nil, fmt.Errorf("toolbox: nil symbol")
	}
	g.reset()
	schema, err := g.generateSymbolSchema(sym, root)
	if err != nil {
		return nil, err
	}
	if len(g.definitions) > 0 {
		// When the root symbol is module-qualified, already appears in
		// definitions (due to self-reference), and TopRef is off, emit
		// a definitions-only schema with no inline root properties and
		// with "id" fields on each definition.
		rootName := g.outputNameForSymbol(sym)
		defsOnly := false
		if _, rootInDefs := g.definitions[rootName]; rootInDefs && isModuleQualifiedSymbol(sym) && !g.opts.TopRef {
			schema = Schema{}
			defsOnly = true
		}
		if defsOnly {
			for name, def := range g.definitions {
				if _, hasID := def["id"]; !hasID {
					def["id"] = name
				}
			}
		}
		// Convert map[string]Schema to map[string]any so that JSON
		// canonicalization (which type-switches on map[string]any)
		// recurses into each definition correctly.
		defs := make(map[string]any, len(g.definitions))
		for k, v := range g.definitions {
			defs[k] = v
		}
		schema["definitions"] = defs
	}
	schema["$schema"] = "http://json-schema.org/draft-07/schema#"
	if g.opts.ID != "" {
		schema["$id"] = g.opts.ID
	}
	return schema, nil
}

func (g *Generator) generateSymbolSchema(sym *ast.Symbol, root bool) (Schema, error) {
	if override, ok := g.overrides[sym.Name]; ok && root {
		schema := cloneSchema(override)
		schema["$schema"] = "http://json-schema.org/draft-07/schema#"
		return schema, nil
	}

	t := g.declaredTypeForSymbol(sym)
	if t == nil {
		return Schema{}, nil
	}
	node := canonicalDeclaration(sym)
	// When the declared type resolves to "any" for a type alias with a
	// ReturnType<typeof fn<...>> body, try to resolve through the function's
	// return type by inspecting the function expression's body.
	if t.Flags()&checker.TypeFlagsAny != 0 && sym.Flags&ast.SymbolFlagsTypeAlias != 0 && node != nil {
		if typeNode := declaredTypeNode(node); typeNode != nil && typeNode.Kind == ast.KindTypeReference {
			refName := entityNameText(typeNode.AsTypeReferenceNode().TypeName)
			if refName == "ReturnType" && len(typeNode.TypeArguments()) == 1 {
				arg := typeNode.TypeArguments()[0]
				// Try to get the resolved type of the ReturnType argument.
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

	// When the root symbol is a type alias and TopRef is off, resolve
	// through the alias to the target type so the schema is inlined.
	// When TopRef is on but AliasRef is off, the alias is also transparent
	// and should be resolved to the target for the top $ref.
	if root && sym.Flags&ast.SymbolFlagsTypeAlias != 0 {
		if !g.opts.TopRef {
			// No TopRef: always resolve the alias, emit the target inline.
			if targetSym := g.aliasTargetSymbol(sym); targetSym != nil {
				return g.generateSymbolSchema(targetSym, root)
			}
			// When the alias body is a utility type (e.g. ReturnType<...>),
			// resolve to the concrete type from the checker.  Try both the
			// declared type of the alias and GetTypeAtLocation on the type node.
			if typeNode := declaredTypeNode(node); typeNode != nil && typeNode.Kind == ast.KindTypeReference {
				refName := entityNameText(typeNode.AsTypeReferenceNode().TypeName)
				if isUtilityTypeName(refName) {
					// First try the declared type (already resolved by the checker).
					if t != nil && t.Flags()&checker.TypeFlagsObject != 0 {
						schema, err := g.objectSchema(t, nil, node)
						if err != nil {
							return nil, err
						}
						if len(schema) > 0 {
							return schema, nil
						}
					}
					// Fall back to GetTypeAtLocation.
					if resolved := g.checker.GetTypeAtLocation(typeNode); resolved != nil && resolved != t {
						schema, err := g.concreteTypeSchema(resolved, node)
						if err != nil {
							return nil, err
						}
						if len(schema) > 0 {
							return schema, nil
						}
					}
				}
			}
		} else if !g.opts.AliasRef {
			// TopRef on, AliasRef off: resolve the alias so the top $ref
			// points directly to the target type (MyObject, not MyAlias).
			if targetSym := g.aliasTargetSymbol(sym); targetSym != nil {
				return g.generateSymbolSchema(targetSym, root)
			}
		}
	}

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
		rootSchema := Schema{"$ref": g.refURI(name)}
		if docs := g.parseDocs(node); docs != nil {
			g.applyDocs(rootSchema, docs)
		}
		if docs := g.parseDocs(sym); docs != nil {
			g.applyDocs(rootSchema, docs)
		}
		return rootSchema, nil
	}

	// When the root type extends another type and has no own members (e.g.
	// `interface MyObject extends D.D {}`), expand the extends target's
	// schema directly at the root level instead of creating a $ref.  This
	// applies only when TopRef is off (when TopRef is on, the definition
	// path above already handles it correctly).
	if root && !g.opts.TopRef {
		if targetSym := extendsTargetSymbol(g.checker, node); targetSym != nil && hasNoOwnMembers(node) {
			targetType := g.declaredTypeForSymbol(targetSym)
			targetNode := canonicalDeclaration(targetSym)
			return g.typeSchema(targetType, targetSym, targetNode, root)
		}
	}

	schema, err := g.typeSchema(t, sym, node, root)
	if err != nil {
		return nil, err
	}
	if schema == nil {
		schema = Schema{}
	}
	return schema, nil
}

// aliasTargetSymbol returns the symbol that a type alias's body refers to,
// if the alias body is a simple TypeReference to a named non-library type
// (interface, class, or another alias).  Returns nil for complex alias
// bodies or references to library types.
func (g *Generator) aliasTargetSymbol(sym *ast.Symbol) *ast.Symbol {
	if sym == nil || sym.Flags&ast.SymbolFlagsTypeAlias == 0 {
		return nil
	}
	typeNode := declaredTypeNode(canonicalDeclaration(sym))
	if typeNode == nil || typeNode.Kind != ast.KindTypeReference {
		return nil
	}
	// Don't resolve if the reference has type arguments (e.g. Array<T>)
	if len(typeNode.TypeArguments()) > 0 {
		return nil
	}
	target := symbolForTypeNode(g.checker, typeNode)
	if target == nil {
		return nil
	}
	if target.Flags&(ast.SymbolFlagsClass|ast.SymbolFlagsInterface|ast.SymbolFlagsTypeAlias) == 0 {
		return nil
	}
	if isLibSymbol(target) {
		return nil
	}
	return target
}

func (g *Generator) declaredTypeForSymbol(sym *ast.Symbol) *checker.Type {
	if sym == nil {
		return nil
	}
	return g.checker.GetDeclaredTypeOfSymbol(sym)
}

func (g *Generator) typeSchema(t *checker.Type, sym *ast.Symbol, node *ast.Node, root bool) (Schema, error) {
	if t == nil {
		return Schema{}, nil
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
		if g.inProgress[name] {
			schema := Schema{"$ref": g.refURI(name)}
			if g.isNullableAlias(sym) {
				makeNullable(schema)
			}
			return schema, nil
		}
		if _, ok := g.definitions[name]; ok {
			schema := Schema{"$ref": g.refURI(name)}
			if g.isNullableAlias(sym) {
				makeNullable(schema)
			}
			return schema, nil
		}
		// Check for schema overrides before generating the definition.
		if override, ok := g.overrides[sym.Name]; ok {
			g.definitions[name] = cloneSchema(override)
			return Schema{"$ref": g.refURI(name)}, nil
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
		schema := Schema{"$ref": g.refURI(name)}
		if g.isNullableAlias(sym) {
			makeNullable(schema)
		}
		return schema, nil
	}

	schema, err := g.emitType(t, sym, node)
	if err != nil {
		return nil, err
	}
	if !root && g.isNullableAlias(sym) {
		makeNullable(schema)
	}
	return schema, err
}

// isNullableAlias checks whether a symbol's type alias declaration has a
// @nullable JSDoc annotation. Results are cached in nullableTypes.
func (g *Generator) isNullableAlias(sym *ast.Symbol) bool {
	if sym == nil {
		return false
	}
	if v, ok := g.nullableTypes[sym]; ok {
		return v
	}
	nullable := false
	if sym.ValueDeclaration != nil {
		if docs := g.parseDocs(sym.ValueDeclaration); docs != nil && docs.nullable {
			nullable = true
		}
	}
	if !nullable && len(sym.Declarations) > 0 {
		if docs := g.parseDocs(sym.Declarations[0]); docs != nil && docs.nullable {
			nullable = true
		}
	}
	g.nullableTypes[sym] = nullable
	return nullable
}

func isEnumSymbol(sym *ast.Symbol) bool {
	if sym == nil {
		return false
	}
	return sym.Flags&(ast.SymbolFlagsRegularEnum|ast.SymbolFlagsConstEnum|ast.SymbolFlagsEnum) != 0
}

func isEnumMemberSymbol(sym *ast.Symbol) bool {
	return sym != nil && sym.Flags&ast.SymbolFlagsEnumMember != 0
}

func isUtilityTypeName(name string) bool {
	switch name {
	case "Pick", "Partial", "Readonly", "ReturnType":
		return true
	default:
		return false
	}
}

func (g *Generator) emitType(t *checker.Type, sym *ast.Symbol, node *ast.Node) (Schema, error) {
	if t == nil {
		return Schema{}, nil
	}
	schema := Schema{}

	// For type aliases with @nullable, the nullable annotation is applied at
	// each usage site (property level) rather than baked into the definition.
	isTypeAlias := sym != nil && sym.Flags&ast.SymbolFlagsTypeAlias != 0

	if docs := g.parseDocs(node); docs != nil {
		if isTypeAlias && docs.nullable {
			g.nullableTypes[sym] = true
			docs.nullable = false
		}
		g.applyDocs(schema, docs)
	}
	if sym != nil {
		// Use applyDocsNonOverwrite for the symbol's own docs so that
		// annotations from the node (e.g. a property declaration with an
		// overriding description) are not clobbered when the type is
		// inlined rather than emitted as a $ref definition.
		if docs := g.parseDocs(sym.ValueDeclaration); docs != nil {
			if isTypeAlias && docs.nullable {
				g.nullableTypes[sym] = true
				docs.nullable = false
			}
			g.applyDocsNonOverwrite(schema, docs)
		}
		if len(sym.Declarations) > 0 {
			if docs := g.parseDocs(sym.Declarations[0]); docs != nil {
				if isTypeAlias && docs.nullable {
					g.nullableTypes[sym] = true
					docs.nullable = false
				}
				g.applyDocsNonOverwrite(schema, docs)
			}
		}
	}
	// If a doc annotation (e.g. @TJS-type) explicitly set the "type" field,
	// preserve it and skip automatic type inference for simple primitives.
	docTypeOverride, _ := schema["type"].(string)

	// When @TJS-type fully overrides the type (e.g. a class annotated with
	// @TJS-type number), return the doc-provided schema directly.  This
	// prevents falling through to objectSchema which would add
	// additionalProperties and other object-level keys on top of the override.
	if docTypeOverride != "" && t.Flags()&checker.TypeFlagsObject != 0 {
		return schema, nil
	}

	if typeNode := declaredTypeNode(node); typeNode != nil {
		if sym != nil && sym.Flags&ast.SymbolFlagsTypeAlias != 0 {
			if typeNode.Kind == ast.KindTypeReference && isUtilityTypeName(entityNameText(typeNode.AsTypeReferenceNode().TypeName)) {
				if parsed, err := g.concreteTypeSchema(t, node); err != nil {
					return nil, err
				} else if len(parsed) > 0 {
					return overlayParsedSchema(parsed, schema), nil
				}
			}
			parsed, ok, err := g.schemaFromTypeNode(typeNode)
			if err != nil {
				return nil, err
			}
			if ok {
				// When a type alias body resolves to a $ref (i.e. the alias
				// just points to another named type), the description should
				// come from the target type, not the alias itself.
				if _, isRef := parsed["$ref"]; isRef {
					if targetSym := symbolForTypeNode(g.checker, typeNode); targetSym != nil {
						targetDesc := g.descriptionForSymbol(targetSym)
						if targetDesc != "" {
							schema["description"] = targetDesc
						} else {
							delete(schema, "description")
						}
					}
				}
				return overlayParsedSchema(parsed, schema), nil
			}
		}
		if shouldPreferTypeNodeSchema(g.checker, t, sym) {
			parsed, ok, err := g.schemaFromTypeNode(typeNode)
			if err != nil {
				return nil, err
			}
			if ok {
				return overlayParsedSchema(parsed, schema), nil
			}
		}
	}
	if targetSym := extendsTargetSymbol(g.checker, node); targetSym != nil && hasNoOwnMembers(node) {
		return g.refSchemaForSymbol(targetSym)
	}

	// Literal types.
	if isEnumMemberSymbol(sym) || (node != nil && node.Kind == ast.KindEnumMember) {
		return g.enumMemberSchema(sym, node)
	}
	if isEnumSymbol(sym) || (node != nil && node.Kind == ast.KindEnumDeclaration) {
		return g.enumSchema(sym, node)
	}
	if t.Flags()&(checker.TypeFlagsStringLiteral|checker.TypeFlagsNumberLiteral|checker.TypeFlagsBooleanLiteral|checker.TypeFlagsBigIntLiteral) != 0 {
		lit := t.AsLiteralType()
		val := lit.Value()
		switch v := val.(type) {
		case string:
			schema["type"] = "string"
			schema = withConst(schema, v, g.useEnumFormat())
		case bool:
			schema["type"] = "boolean"
			schema = withConst(schema, v, g.useEnumFormat())
		case nil:
			schema["type"] = "null"
			schema = withConst(schema, nil, g.useEnumFormat())
		case float64:
			schema["type"] = g.numberType()
			schema = withConst(schema, v, g.useEnumFormat())
		default:
			if s, ok := val.(string); ok {
				schema["type"] = "string"
				schema = withConst(schema, s, g.useEnumFormat())
			}
		}
		return schema, nil
	}

	if t.Flags()&checker.TypeFlagsUnion != 0 {
		return g.unionSchema(t.AsUnionType(), sym, node)
	}
	if t.Flags()&checker.TypeFlagsIntersection != 0 {
		return g.intersectionSchema(t.AsIntersectionType(), sym, node)
	}
	if t.Flags()&checker.TypeFlagsTemplateLiteral != 0 {
		if docTypeOverride == "" {
			schema["type"] = "string"
		}
		if pattern := templateLiteralPattern(t); pattern != "" {
			schema["pattern"] = pattern
		}
		return schema, nil
	}
	if t.Flags()&checker.TypeFlagsString != 0 {
		if docTypeOverride == "" {
			schema["type"] = "string"
		}
		return schema, nil
	}
	if t.Flags()&checker.TypeFlagsBoolean != 0 {
		if docTypeOverride == "" {
			schema["type"] = "boolean"
		}
		return schema, nil
	}
	if t.Flags()&checker.TypeFlagsNumber != 0 {
		if docTypeOverride == "" {
			schema["type"] = g.numberType()
		}
		return schema, nil
	}
	if t.Flags()&checker.TypeFlagsBigInt != 0 {
		if docTypeOverride == "" {
			schema["type"] = g.numberType()
		}
		return schema, nil
	}
	if t.Flags()&checker.TypeFlagsNull != 0 {
		if docTypeOverride == "" {
			schema["type"] = "null"
		}
		return schema, nil
	}
	if t.Flags()&checker.TypeFlagsESSymbol != 0 || t.Flags()&checker.TypeFlagsUniqueESSymbol != 0 {
		if docTypeOverride == "" {
			schema["type"] = "object"
		}
		return schema, nil
	}
	if t.Flags()&checker.TypeFlagsAny != 0 || t.Flags()&checker.TypeFlagsUnknown != 0 {
		if parsed, ok, err := g.schemaFromTypeString(g.typeString(t), node); err != nil {
			return nil, err
		} else if ok {
			return parsed, nil
		}
		return schema, nil
	}

	if t.Flags()&checker.TypeFlagsObject != 0 {
		return g.objectSchema(t, sym, node)
	}

	// Fallback to simple strings for unsupported structured types.
	return schema, nil
}

func (g *Generator) concreteTypeSchema(t *checker.Type, node *ast.Node) (Schema, error) {
	if t == nil {
		return Schema{}, nil
	}
	if t.Flags()&checker.TypeFlagsNever != 0 {
		return nil, nil
	}
	if t.Flags()&checker.TypeFlagsUndefined != 0 || t.Flags()&checker.TypeFlagsVoid != 0 {
		return nil, nil
	}
	if isBuiltinDateType(g.checker, t, node) {
		if g.opts.RejectDateType {
			return nil, fmt.Errorf("Unsupported type: Date")
		}
		return Schema{"type": "string", "format": "date-time"}, nil
	}
	if t.Flags()&(checker.TypeFlagsStringLiteral|checker.TypeFlagsNumberLiteral|checker.TypeFlagsBooleanLiteral|checker.TypeFlagsBigIntLiteral) != 0 {
		// Inside concrete type resolution (e.g. generic instantiations),
		// always use enum format for literal values to match the original
		// typescript-json-schema behavior.
		lit := t.AsLiteralType()
		val := lit.Value()
		schema := Schema{}
		switch v := val.(type) {
		case string:
			schema["type"] = "string"
			schema["enum"] = []any{v}
		case bool:
			schema["type"] = "boolean"
			schema["enum"] = []any{v}
		case nil:
			schema["type"] = "null"
			schema["enum"] = []any{nil}
		case float64:
			schema["type"] = g.numberType()
			schema["enum"] = []any{v}
		default:
			if s, ok := val.(string); ok {
				schema["type"] = "string"
				schema["enum"] = []any{s}
			}
		}
		return schema, nil
	}
	if t.Flags()&checker.TypeFlagsUnion != 0 {
		return g.unionSchema(t.AsUnionType(), nil, nil)
	}
	if t.Flags()&checker.TypeFlagsIntersection != 0 {
		return g.intersectionSchema(t.AsIntersectionType(), nil, nil)
	}
	if t.Flags()&checker.TypeFlagsTemplateLiteral != 0 {
		schema := Schema{"type": "string"}
		if pattern := templateLiteralPattern(t); pattern != "" {
			schema["pattern"] = pattern
		}
		return schema, nil
	}
	if t.Flags()&checker.TypeFlagsString != 0 {
		return Schema{"type": "string"}, nil
	}
	if t.Flags()&checker.TypeFlagsBoolean != 0 {
		return Schema{"type": "boolean"}, nil
	}
	if t.Flags()&checker.TypeFlagsNumber != 0 {
		return Schema{"type": g.numberType()}, nil
	}
	if t.Flags()&checker.TypeFlagsBigInt != 0 {
		return Schema{"type": g.numberType()}, nil
	}
	if t.Flags()&checker.TypeFlagsNull != 0 {
		return Schema{"type": "null"}, nil
	}
	if t.Flags()&(checker.TypeFlagsAny|checker.TypeFlagsUnknown) != 0 {
		if parsed, ok, err := g.schemaFromTypeString(g.typeString(t), node); err != nil {
			return nil, err
		} else if ok {
			return parsed, nil
		}
		return Schema{}, nil
	}
	if t.Flags()&checker.TypeFlagsObject != 0 {
		return g.objectSchema(t, nil, node)
	}
	return Schema{}, nil
}

func (g *Generator) utilityTypeSchema(node *ast.Node) (Schema, bool, error) {
	if node == nil || node.Kind != ast.KindTypeReference {
		return nil, false, nil
	}
	name := entityNameText(node.AsTypeReferenceNode().TypeName)
	args := node.TypeArguments()
	resolveArg := func(arg *ast.Node) (*checker.Type, error) {
		if arg == nil {
			return nil, nil
		}
		if typ := g.checker.GetTypeAtLocation(arg); typ != nil {
			return typ, nil
		}
		return g.checker.GetTypeFromTypeNode(arg), nil
	}

	switch name {
	case "Readonly":
		if len(args) != 1 {
			return nil, false, nil
		}
		if sym := symbolForTypeNode(g.checker, args[0]); sym != nil && !isLibSymbol(sym) && g.shouldRefSymbol(sym) {
			schema, err := g.refSchemaForSymbol(sym)
			return schema, err == nil, err
		}
		typ, err := resolveArg(args[0])
		if err != nil {
			return nil, false, err
		}
		if typ == nil {
			return nil, false, nil
		}
		schema, err := g.concreteTypeSchema(typ, args[0])
		return schema, len(schema) > 0, err
	case "Partial":
		if len(args) != 1 {
			return nil, false, nil
		}
		typ, err := resolveArg(args[0])
		if err != nil {
			return nil, false, err
		}
		if typ == nil {
			return nil, false, nil
		}
		schema, err := g.concreteTypeSchema(typ, args[0])
		if err != nil || len(schema) == 0 {
			return schema, len(schema) > 0, err
		}
		delete(schema, "required")
		if g.opts.AliasRef {
			return g.wrapUtilityTypeAsRef("Partial", schema, node)
		}
		return schema, true, nil
	case "Pick":
		if len(args) != 2 {
			return nil, false, nil
		}
		typ, err := resolveArg(args[0])
		if err != nil {
			return nil, false, err
		}
		if typ == nil {
			return nil, false, nil
		}
		schema, err := g.concreteTypeSchema(typ, args[0])
		if err != nil || len(schema) == 0 {
			return schema, len(schema) > 0, err
		}
		keys := stringLiteralTypeKeys(args[1])
		if len(keys) == 0 {
			return schema, true, nil
		}
		return pickObjectSchema(schema, keys), true, nil
	case "ReturnType":
		if typ := g.checker.GetTypeAtLocation(node); typ != nil {
			schema, err := g.concreteTypeSchema(typ, node)
			return schema, len(schema) > 0, err
		}
	}
	return nil, false, nil
}

// wrapUtilityTypeAsRef creates definitions for a utility type instantiation
// (e.g. Partial<Foo>) when AliasRef is on.  It produces a definition like
// "Partial" -> {"$ref": "#/definitions/__type"} and "__type" -> schema.
// For duplicate names it appends _1, _2, etc.
func (g *Generator) wrapUtilityTypeAsRef(utilName string, schema Schema, node *ast.Node) (Schema, bool, error) {
	// Pick a unique name for the anonymous inner type.
	anonBase := "__type"
	anonName := anonBase
	for suffix := 1; ; suffix++ {
		if _, exists := g.definitions[anonName]; !exists {
			break
		}
		anonName = fmt.Sprintf("%s_%d", anonBase, suffix)
	}
	g.definitions[anonName] = schema

	// Pick a unique name for the utility type alias.
	aliasName := utilName
	for suffix := 1; ; suffix++ {
		if _, exists := g.definitions[aliasName]; !exists {
			break
		}
		aliasName = fmt.Sprintf("%s_%d", utilName, suffix)
	}
	g.definitions[aliasName] = Schema{"$ref": g.refURI(anonName)}

	return Schema{"$ref": g.refURI(aliasName)}, true, nil
}

func (g *Generator) unionSchema(t *checker.UnionType, sym *ast.Symbol, node *ast.Node) (Schema, error) {
	schema := Schema{}
	var simpleTypes []string
	var anyOf []any
	var enumVals []any
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
		// Without strictNullChecks, null is implicitly part of every type,
		// so strip it from explicit unions (e.g. object | null -> object).
		if !g.strictNullChecks && mt.Flags()&checker.TypeFlagsNull != 0 {
			continue
		}
		if mt.Flags()&(checker.TypeFlagsStringLiteral|checker.TypeFlagsNumberLiteral|checker.TypeFlagsBooleanLiteral|checker.TypeFlagsBigIntLiteral) != 0 {
			lit := mt.AsLiteralType()
			enumVals = append(enumVals, lit.Value())
			continue
		}
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
		sub, err := g.typeSchema(mt, memberSym, memberNode, false)
		if err != nil {
			return nil, err
		}
		if len(sub) == 0 {
			continue
		}
		anyOf = append(anyOf, sub)
	}

	// When boolean appears alongside other literal values, decompose it
	// into true/false enum values so they can be combined into one enum array.
	if len(enumVals) > 0 && containsStr(simpleTypes, "boolean") {
		enumVals = append(enumVals, true, false)
		simpleTypes = removeStr(simpleTypes, "boolean")
	}

	// When simple types subsume all enum values, drop the enum values.
	// For example: "ok" | "fail" | string -> just "string".
	if len(enumVals) > 0 && len(simpleTypes) > 0 {
		enumVals = filterSubsumedEnumVals(enumVals, simpleTypes)
	}

	if len(enumVals) > 0 && len(anyOf) == 0 && len(simpleTypes) == 0 {
		if allSameType(enumVals, "bool") && len(enumVals) == 2 {
			schema["type"] = "boolean"
			return schema, nil
		}
		if len(enumVals) == 1 && !g.opts.ConstAsEnum {
			schema["const"] = enumVals[0]
		} else {
			if allSameType(enumVals, "string") {
				sort.Slice(enumVals, func(i, j int) bool {
					return enumVals[i].(string) < enumVals[j].(string)
				})
			}
			schema["enum"] = enumVals
		}
		if allSameType(enumVals, "string") {
			schema["type"] = "string"
		} else if allSameType(enumVals, "bool") {
			schema["type"] = "boolean"
		} else if allSameType(enumVals, "num") {
			schema["type"] = g.numberType()
		}
		return schema, nil
	}
	// When enum values coexist with simple types or anyOf branches,
	// wrap them in an anyOf.
	if len(enumVals) > 0 && (len(simpleTypes) > 0 || len(anyOf) > 0) {
		enumSchema := Schema{"enum": enumVals}
		anyOf = append([]any{enumSchema}, anyOf...)
	}
	if len(simpleTypes) > 0 {
		simpleTypes = uniqueStringsStable(simpleTypes)
		sort.Strings(simpleTypes)
		if len(simpleTypes) == 1 {
			schema["type"] = simpleTypes[0]
		} else {
			types := make([]any, len(simpleTypes))
			for i, t := range simpleTypes {
				types[i] = t
			}
			schema["type"] = types
		}
	}
	if len(anyOf) == 1 && len(schema) == 0 {
		if s, ok := anyOf[0].(Schema); ok {
			return s, nil
		}
	}
	if len(anyOf) > 0 {
		combined := make([]any, 0, len(anyOf))
		for _, a := range anyOf {
			combined = append(combined, a)
		}
		if len(schema) == 0 {
			schema["anyOf"] = combined
			return schema, nil
		}
		combined = append(combined, schema)
		return Schema{"anyOf": combined}, nil
	}
	return schema, nil
}

func (g *Generator) intersectionSchema(t *checker.IntersectionType, sym *ast.Symbol, node *ast.Node) (Schema, error) {
	members := t.Types()
	if merged, ok, err := g.mergeIntersectionMembers(members, sym, node); err != nil {
		return nil, err
	} else if ok {
		return merged, nil
	}
	if g.opts.NoExtraProps {
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
		return schema, nil
	}
	allOf := make([]any, 0, len(members))
	for _, mt := range members {
		sub, err := g.emitType(mt, sym, node)
		if err != nil {
			return nil, err
		}
		allOf = append(allOf, sub)
	}
	return Schema{"allOf": allOf}, nil
}

func (g *Generator) mergeIntersectionMembers(members []*checker.Type, sym *ast.Symbol, node *ast.Node) (Schema, bool, error) {
	schema := Schema{}
	properties := map[string]any{}
	var required []string
	for _, mt := range members {
		sub, err := g.emitType(mt, sym, node)
		if err != nil {
			return nil, false, err
		}
		if len(sub) == 0 {
			return nil, false, nil
		}
		sub = g.derefLocalSchema(sub)
		if t, ok := sub["type"].(string); !ok || t != "object" {
			return nil, false, nil
		}
		for k := range sub {
			if k != "type" && k != "properties" && k != "required" && k != "additionalProperties" {
				return nil, false, nil
			}
		}
		if props, ok := sub["properties"].(map[string]any); ok {
			for k, v := range props {
				properties[k] = v
			}
		}
		required = append(required, asStrings(sub["required"])...)
		if ap, ok := sub["additionalProperties"]; ok {
			schema["additionalProperties"] = ap
		}
	}
	if len(properties) == 0 {
		return nil, false, nil
	}
	schema["type"] = "object"
	schema["properties"] = properties
	if len(required) > 0 {
		schema["required"] = uniqueStrings(required)
	}
	return schema, true, nil
}

func (g *Generator) derefLocalSchema(schema Schema) Schema {
	if schema == nil || len(schema) != 1 {
		return schema
	}
	ref, ok := schema["$ref"].(string)
	if !ok || !strings.HasPrefix(ref, "#/definitions/") {
		return schema
	}
	name := strings.TrimPrefix(ref, "#/definitions/")
	if def, ok := g.definitions[name]; ok && def != nil {
		return cloneSchema(def)
	}
	return schema
}

func (g *Generator) objectSchema(t *checker.Type, sym *ast.Symbol, node *ast.Node) (Schema, error) {
	schema := Schema{}
	tstr := g.checker.TypeToStringEx(t, nil, checker.TypeFormatFlagsUseStructuralFallback|checker.TypeFormatFlagsNoTruncation)

	if typeNode := declaredTypeNode(node); typeNode != nil && shouldExpandArrayAnnotation(g.checker, g, typeNode) {
		if parsed, ok, err := g.schemaFromTypeNode(typeNode); err != nil {
			return nil, err
		} else if ok && isArraySchema(parsed) {
			return parsed, nil
		}
	}
	if looksLikeArrayTypeString(tstr) {
		if parsed, ok, err := g.schemaFromTypeString(tstr, node); err != nil {
			return nil, err
		} else if ok {
			return parsed, nil
		}
	}

	// Fallback: use the checker API to detect array/tuple types that were not
	// caught by the type-string heuristic (e.g. when UseStructuralFallback
	// renders the array type as "{}" inside a structural expansion).
	if elemType := g.checker.GetElementTypeOfArrayType(t); elemType != nil {
		elemSchema, err := g.typeSchema(elemType, nil, node, false)
		if err != nil {
			return nil, err
		}
		if elemSchema == nil {
			elemSchema = Schema{}
		}
		return Schema{"type": "array", "items": elemSchema}, nil
	}

	if isBuiltinDateType(g.checker, t, node) {
		if g.opts.RejectDateType {
			return nil, fmt.Errorf("Unsupported type: Date")
		}
		schema["type"] = "string"
		schema["format"] = "date-time"
		return schema, nil
	}
	if tstr == "object" {
		schema["type"] = "object"
		schema["properties"] = map[string]any{}
		schema["additionalProperties"] = true
		return schema, nil
	}
	if tstr == "{}" {
		schema["type"] = "object"
		schema["properties"] = map[string]any{}
		return schema, nil
	}

	props := g.checker.GetPropertiesOfType(t)
	if len(props) == 0 {
		props = g.checker.GetApparentProperties(t)
	}
	// Fallback for library interfaces: if the checker returns no properties,
	// try to extract members from the AST declarations directly.
	if len(props) == 0 && sym != nil && isLibSymbol(sym) {
		props = extractInterfaceMembers(sym)
	}
	sortSymbolsByDeclaration(props)
	propertySchemas := map[string]any{}
	required := make([]string, 0, len(props))
	indexSchemas, err := g.indexSchemasForType(t, node)
	if err != nil {
		return nil, err
	}
	// When the type has a string index signature, filter out properties
	// that are implied by the index (no own declarations) rather than
	// explicitly declared.  This prevents mapped types like
	// { [key: string]: T } from expanding into named properties.
	hasStringIndex := false
	for _, idx := range indexSchemas {
		if !idx.isNumeric {
			hasStringIndex = true
			break
		}
	}
	if hasStringIndex {
		var filtered []*ast.Symbol
		for _, prop := range props {
			if prop == nil {
				continue
			}
			if len(prop.Declarations) == 0 && prop.ValueDeclaration == nil {
				continue
			}
			filtered = append(filtered, prop)
		}
		props = filtered
	}

	symIsLib := isLibSymbol(sym)
	for _, prop := range props {
		if prop == nil {
			continue
		}
		// Skip well-known symbol properties (e.g. [Symbol.hasInstance])
		// which use the Go checker's internal \ufffd encoding and have no
		// meaningful representation in JSON Schema.
		if isWellKnownSymbolProp(prop.Name) {
			continue
		}
		// Skip methods on user types, but include them on library types
		// (e.g. Function interface members like apply, bind, call).
		if !symIsLib && !g.opts.ExcludePrivate && prop.Flags&ast.SymbolFlagsMethod != 0 {
			continue
		}
		if g.opts.ExcludePrivate && isPrivateProp(prop) {
			continue
		}
		propType := g.checker.GetTypeOfSymbolAtLocation(prop, nodeOrFallback(node, prop.ValueDeclaration))
		if propType == nil {
			continue
		}
		// For library interface methods, emit a simple {"type": "object"}
		// schema instead of trying to resolve the function type.
		if symIsLib && prop.Flags&ast.SymbolFlagsMethod != 0 {
			propSchema := Schema{"type": "object"}
			if decl := prop.ValueDeclaration; decl != nil {
				if docs := g.parseDocs(decl); docs != nil && docs.description != "" {
					propSchema["description"] = docs.description
				}
			}
			propertySchemas[prop.Name] = propSchema
			required = append(required, prop.Name)
			continue
		}
		propSchema, err := g.propertySchema(prop, propType, t, sym, node)
		if err != nil {
			return nil, err
		}
		if propSchema == nil {
			continue
		}
		propertySchemas[prop.Name] = propSchema
		if isRequiredProp(prop, propType, propSchema) {
			required = append(required, prop.Name)
		}
	}
	schema["type"] = "object"
	if len(propertySchemas) > 0 {
		schema["properties"] = propertySchemas
	}
	hasStringIndex = false
	var stringIndexSchema Schema
	for _, idx := range indexSchemas {
		if idx.isNumeric {
			if schema["patternProperties"] == nil {
				schema["patternProperties"] = map[string]any{}
			}
			if pp, ok := schema["patternProperties"].(map[string]any); ok {
				pp["^[0-9]+$"] = idx.schema
			}
		} else {
			hasStringIndex = true
			stringIndexSchema = idx.schema
		}
	}
	if hasStringIndex {
		schema["additionalProperties"] = stringIndexSchema
	} else if g.opts.NoExtraProps {
		schema["additionalProperties"] = false
	}

	if len(required) > 0 && g.opts.Required {
		sort.Strings(required)
		schema["required"] = required
	}
	if docs := g.parseDocs(node); docs != nil {
		g.applyDocs(schema, docs)
	}
	if docs := g.parseDocs(sym); docs != nil {
		// Use applyDocsNonOverwrite so that annotations from the node
		// (e.g. a property declaration) are not clobbered by the type
		// symbol's own docs when the type is inlined.
		g.applyDocsNonOverwrite(schema, docs)
	}
	if hasStringIndex {
		schema["additionalProperties"] = stringIndexSchema
	}
	return schema, nil
}

type indexSchemaResult struct {
	schema    Schema
	isNumeric bool
}

func (g *Generator) indexSchemasForType(t *checker.Type, node *ast.Node) ([]indexSchemaResult, error) {
	if t == nil {
		return nil, nil
	}
	indexInfos := g.checker.GetIndexInfosOfType(t)
	if len(indexInfos) == 0 {
		return nil, nil
	}
	var results []indexSchemaResult
	for _, info := range indexInfos {
		if info == nil || info.ValueType() == nil {
			continue
		}
		refSym := namedRefSymbol(g.checker, info.ValueType())
		if refSym == nil {
			sym := info.ValueType().Symbol()
			// Only use the value type's symbol as a ref target if it is a
			// user-defined type.  Internal/synthetic symbols (e.g. from
			// mapped type resolution) should be inlined.
			if sym != nil && !isInternalSymbolName(sym.Name) {
				refSym = sym
			}
		}
		schema, err := g.typeSchema(info.ValueType(), refSym, node, false)
		if err != nil {
			return nil, err
		}
		if schema != nil {
			isNumeric := info.KeyType() != nil && info.KeyType().Flags()&checker.TypeFlagsNumberLike != 0
			results = append(results, indexSchemaResult{schema: schema, isNumeric: isNumeric})
		}
	}
	return results, nil
}

func (g *Generator) enumMemberSchema(sym *ast.Symbol, node *ast.Node) (Schema, error) {
	if sym == nil && node != nil {
		sym = node.Symbol()
	}
	if sym == nil || !isEnumMemberSymbol(sym) {
		return Schema{}, nil
	}
	if node == nil {
		node = canonicalDeclaration(sym)
	}
	if node == nil || node.Kind != ast.KindEnumMember {
		return Schema{}, nil
	}

	value := g.checker.GetConstantValue(node)
	if value == nil {
		var ok bool
		value, ok = evaluateLiteral(node.Initializer())
		if !ok {
			return Schema{}, nil
		}
	}
	value = normalizeSchemaScalar(value)
	schema := withConst(Schema{}, value, g.opts.ConstAsEnum)
	switch value.(type) {
	case nil:
		schema["type"] = "null"
	case string:
		schema["type"] = "string"
	case bool:
		schema["type"] = "boolean"
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, jsnum.Number:
		schema["type"] = g.numberType()
	}
	return schema, nil
}

func (g *Generator) enumSchema(sym *ast.Symbol, node *ast.Node) (Schema, error) {
	if sym == nil && node != nil {
		sym = node.Symbol()
	}
	if sym == nil || !isEnumSymbol(sym) {
		return Schema{}, nil
	}
	if node == nil {
		node = canonicalDeclaration(sym)
	}
	if node == nil || node.Kind != ast.KindEnumDeclaration {
		return Schema{}, nil
	}

	values := make([]any, 0, len(node.Members()))
	for _, member := range node.Members() {
		if member == nil || member.Kind != ast.KindEnumMember {
			continue
		}
		if value := g.checker.GetConstantValue(member); value != nil {
			values = append(values, normalizeSchemaScalar(value))
			continue
		}
		if value, ok := evaluateLiteral(member.Initializer()); ok {
			values = append(values, normalizeSchemaScalar(value))
		}
	}
	if len(values) == 0 {
		return Schema{}, nil
	}
	sort.Slice(values, func(i, j int) bool {
		left, _ := json.Marshal(values[i])
		right, _ := json.Marshal(values[j])
		return string(left) < string(right)
	})

	schema := Schema{}
	switch {
	case allSameType(values, "string"):
		schema["type"] = "string"
		sort.Slice(values, func(i, j int) bool {
			return values[i].(string) < values[j].(string)
		})
	case allSameType(values, "bool"):
		schema["type"] = "boolean"
	case allSameType(values, "num"):
		schema["type"] = g.numberType()
	default:
		kinds := enumValueTypes(values, g.numberType())
		if len(kinds) == 1 {
			schema["type"] = kinds[0]
		} else if len(kinds) > 1 {
			sort.Strings(kinds)
			types := make([]any, 0, len(kinds))
			for _, kind := range kinds {
				types = append(types, kind)
			}
			schema["type"] = types
		}
	}
	if len(values) == 1 {
		return withConst(schema, values[0], g.opts.ConstAsEnum), nil
	}
	schema["enum"] = values
	return schema, nil
}

func (g *Generator) instantiatedRefSchema(name string, t *checker.Type, node *ast.Node) (Schema, error) {
	name = normalizeDefinitionName(name)
	if name == "" {
		return g.concreteTypeSchema(t, node)
	}
	if g.inProgress[name] {
		return Schema{"$ref": g.refURI(name)}, nil
	}
	if _, ok := g.definitions[name]; ok {
		return Schema{"$ref": g.refURI(name)}, nil
	}
	g.inProgress[name] = true
	prev := g.inConcreteContext
	g.inConcreteContext = true
	def, err := g.concreteTypeSchema(t, node)
	g.inConcreteContext = prev
	delete(g.inProgress, name)
	if err != nil {
		return nil, err
	}
	if def == nil {
		def = Schema{}
	}
	g.definitions[name] = def
	return Schema{"$ref": g.refURI(name)}, nil
}

func (g *Generator) propertySchema(prop *ast.Symbol, propType *checker.Type, parentType *checker.Type, parentSym *ast.Symbol, location *ast.Node) (Schema, error) {
	if prop == nil || propType == nil {
		return nil, nil
	}
	if propType.Flags()&checker.TypeFlagsNever != 0 || propType.Flags()&checker.TypeFlagsUndefined != 0 {
		return nil, nil
	}
	if g.opts.TypeOfKeyword && g.checker.TypeHasCallOrConstructSignatures(propType) {
		return Schema{"typeof": "function"}, nil
	}

	decl := prop.ValueDeclaration
	if decl == nil && len(prop.Declarations) > 0 {
		decl = prop.Declarations[0]
	}
	// Check for @TJS-ignore on the property declaration and skip it entirely.
	if decl != nil {
		if docs := g.parseDocs(decl); docs != nil && docs.ignore {
			return nil, nil
		}
	}

	// When a property declaration has a @TJS-type annotation, the override
	// replaces the entire type resolution.  Build the schema from the
	// annotation alone so that the property is inlined rather than emitted
	// as a $ref to the original type definition.
	if decl != nil {
		if docs := g.parseDocs(decl); docs != nil && docs.fields != nil {
			if _, hasTypeOverride := docs.fields["type"]; hasTypeOverride {
				schema := Schema{}
				g.applyDocs(schema, docs)
				if init := declInitializer(decl); init != nil {
					if def, ok := evaluateLiteral(init); ok {
						schema["default"] = def
					}
				}
				return schema, nil
			}
		}
	}

	finish := func(schema Schema) (Schema, error) {
		if schema == nil {
			return nil, nil
		}
		if decl != nil {
			if docs := g.parseDocs(decl); docs != nil {
				g.applyDocs(schema, docs)
			}
		}
		// Post-process: evaluate require() expressions in examples fields.
		if exStr, ok := schema["examples"].(string); ok && strings.HasPrefix(exStr, "require(") {
			if val, ok := g.evaluateRequireExpr(exStr, decl); ok {
				schema["examples"] = val
			}
		}
		if init := declInitializer(decl); init != nil {
			if def, ok := evaluateLiteral(init); ok {
				schema["default"] = def
			}
		}
		// If the property's declared type references a type alias with
		// @nullable, apply nullable at this usage site.
		if typeNode := declaredTypeNode(decl); typeNode != nil {
			if aliasSym := symbolForTypeNode(g.checker, typeNode); aliasSym != nil {
				if g.isNullableAlias(aliasSym) {
					makeNullable(schema)
				}
			}
		}
		return schema, nil
	}
	if typeNode := declaredTypeNode(decl); typeNode != nil {
		switch {
		case isArrayTypeNode(typeNode), typeNode.Kind == ast.KindIntersectionType:
			schema, ok, err := g.schemaFromTypeNode(typeNode)
			if err != nil {
				return nil, err
			}
			if ok {
				if schema == nil {
					schema = Schema{}
				}
				return finish(schema)
			}
		case typeNode.Kind == ast.KindTypeLiteral:
			// When the resolved property type has a string index signature
			// that the AST-based TypeLiteral does not capture (e.g. mapped
			// types like [Key in keyof T]: { [k: string]: T[Key] } where
			// the checker synthesizes a PropertySignature-based TypeLiteral
			// for the inner value), use the checker-based objectSchema
			// which correctly emits additionalProperties for the string index.
			hasCheckerStringIndex := false
			for _, idx := range g.checker.GetIndexInfosOfType(propType) {
				if idx != nil && idx.KeyType() != nil && idx.KeyType().Flags()&checker.TypeFlagsString != 0 {
					hasCheckerStringIndex = true
					break
				}
			}
			if hasCheckerStringIndex {
				schema, err := g.objectSchema(propType, nil, decl)
				if err != nil {
					return nil, err
				}
				return finish(schema)
			}
			// Process anonymous type literals through the AST-based path
			// rather than the type-string path, because the type-string
			// renderer may lose array types inside structural expansions.
			schema, ok, err := g.schemaFromTypeNode(typeNode)
			if err != nil {
				return nil, err
			}
			if ok {
				if schema == nil {
					schema = Schema{}
				}
				return finish(schema)
			}
		case typeNode.Kind == ast.KindTypeReference:
			// When a property references a library interface (e.g. Function)
			// whose type resolves to "any" with a transient symbol (no lib
			// files loaded), try to resolve the real symbol from the global
			// scope.  If that fails, emit an empty schema (= any) as fallback.
			if refSym := symbolForTypeNode(g.checker, typeNode); refSym != nil && g.opts.Ref {
				resolved := g.declaredTypeForSymbol(refSym)
				isTransient := refSym.Flags&ast.SymbolFlagsTransient != 0
				isAny := resolved != nil && resolved.Flags()&checker.TypeFlagsAny != 0
				if isAny && isTransient && (isLibSymbol(refSym) || isGlobalInterfaceName(refSym.Name)) {
					name := refSym.Name
					if _, ok := g.definitions[name]; !ok {
						// Try to resolve the real symbol from the global scope
						// (available when lib files are loaded via bundled.WrapFS).
						if globalSym := g.checker.GetGlobalSymbol(name, ast.SymbolFlagsType, nil); globalSym != nil && len(globalSym.Declarations) > 0 {
							declType := g.checker.GetDeclaredTypeOfSymbol(globalSym)
							if declType != nil {
								schema, err := g.objectSchema(declType, globalSym, nil)
								if err == nil {
									g.definitions[name] = schema
								} else {
									g.definitions[name] = Schema{}
								}
							} else {
								g.definitions[name] = Schema{}
							}
						} else {
							// Lib files not available — emit empty schema (= any).
							g.definitions[name] = Schema{}
						}
					}
					return finish(Schema{"$ref": g.refURI(name)})
				}
			}
			// Check for the well-known "type integer = number" alias before
			// falling through to the generic type-string path.
			if aliasSym := symbolForTypeNode(g.checker, typeNode); g.isIntegerAlias(aliasSym) {
				return finish(Schema{"type": "integer"})
			}
			// Resolve type aliases that refer to array types so they are not
			// rendered as empty objects by the type-string path.  This handles
			// both inline expansion (when the alias is not ref'd) and ref'd
			// definitions (when AliasRef is set).
			if aliasSym := symbolForTypeNode(g.checker, typeNode); aliasSym != nil && aliasSym.Flags&ast.SymbolFlagsTypeAlias != 0 && isArrayAliasSymbol(aliasSym) {
				schema, ok, err := g.schemaFromTypeNode(typeNode)
				if err != nil {
					return nil, err
				}
				if ok {
					if schema == nil {
						schema = Schema{}
					}
					return finish(schema)
				}
			}
			// When the type reference points to a type alias (or interface/class)
			// that should be emitted as a $ref definition, delegate to the AST-based
			// resolution so the alias identity is preserved.  Without this, the
			// type-string path below would resolve the alias to its underlying type
			// and lose the alias name.
			// For generic instantiations of type aliases, also use schemaFromTypeNode
			// so alias names are properly resolved (e.g. SomeAlias<T> -> SomeGeneric<T,T>).
			if len(typeNode.TypeArguments()) == 0 {
				if refSym := symbolForTypeNode(g.checker, typeNode); refSym != nil && g.shouldRefSymbol(refSym) {
					schema, ok, err := g.schemaFromTypeNode(typeNode)
					if err != nil {
						return nil, err
					}
					if ok {
						if schema == nil {
							schema = Schema{}
						}
						return finish(schema)
					}
				}
			} else {
				// Generic instantiation of a type alias: route through
				// schemaFromTypeNode for proper alias resolution.
				if refSym := symbolForTypeNode(g.checker, typeNode); refSym != nil && refSym.Flags&ast.SymbolFlagsTypeAlias != 0 && g.shouldRefSymbol(refSym) {
					schema, ok, err := g.schemaFromTypeNode(typeNode)
					if err != nil {
						return nil, err
					}
					if ok {
						if schema == nil {
							schema = Schema{}
						}
						return finish(schema)
					}
				}
			}
		}
	}
	if concreteName := g.typeString(propType); concreteName != "" {
		concreteName = normalizeDefinitionName(concreteName)
		if looksLikeArrayTypeString(concreteName) {
			schema, ok, err := g.schemaFromTypeString(concreteName, decl)
			if err != nil {
				return nil, err
			}
			if ok {
				return finish(schema)
			}
		}
		if strings.Contains(concreteName, "<") {
			if sym := propType.Symbol(); sym != nil && !isLibSymbol(sym) && g.shouldRefSymbol(sym) {
				schema, err := g.instantiatedRefSchema(concreteName, propType, location)
				if err != nil {
					return nil, err
				}
				return finish(schema)
			}
		}
		if schema, ok, err := g.schemaFromTypeString(concreteName, decl); err != nil {
			return nil, err
		} else if ok {
			return finish(schema)
		}
	}
	if typeNode := declaredTypeNode(decl); typeNode != nil {
		schema, ok, err := g.schemaFromTypeNode(typeNode)
		if err != nil {
			return nil, err
		}
		if ok {
			if schema == nil {
				schema = Schema{}
			}
			return finish(schema)
		}
	}

	refSym := g.refSymbolForType(propType, decl)
	if refSym == nil && parentSym != nil {
		switch {
		case parentType != nil && propType == parentType:
			refSym = parentSym
		case propType.Symbol() != nil && propType.Symbol() == parentSym:
			refSym = parentSym
		}
	}
	schema, err := g.typeSchema(propType, refSym, decl, false)
	if err != nil {
		return nil, err
	}
	if schema == nil {
		return nil, nil
	}
	return finish(schema)
}

func namedRefSymbol(ch *checker.Checker, t *checker.Type) *ast.Symbol {
	if t == nil {
		return nil
	}
	if ch.IsArrayLikeType(t) || checker.IsTupleType(t) {
		return nil
	}
	sym := t.Symbol()
	if sym == nil {
		return nil
	}
	if isBuiltinDateSymbol(sym) || isBuiltInDateAlias(ch, sym) {
		return nil
	}
	if t.Flags()&checker.TypeFlagsObject == 0 {
		return nil
	}
	if isBuiltinDateType(ch, t, nil) {
		return nil
	}
	if sym.Flags&(ast.SymbolFlagsClass|ast.SymbolFlagsInterface|ast.SymbolFlagsTypeAlias|ast.SymbolFlagsRegularEnum|ast.SymbolFlagsConstEnum|ast.SymbolFlagsNamespaceModule|ast.SymbolFlagsValueModule) == 0 {
		return nil
	}
	// Types that only have numeric index signatures should be inlined.
	if isNumericIndexOnlyType(sym) {
		return nil
	}
	return sym
}

// isNumericIndexOnlyType returns true when a symbol's declarations contain
// only numeric index signatures.  Such types should be inlined rather than
// emitted as $ref definitions.
func isNumericIndexOnlyType(sym *ast.Symbol) bool {
	if sym == nil {
		return false
	}
	for _, decl := range sym.Declarations {
		if decl == nil {
			continue
		}
		var members []*ast.Node
		switch decl.Kind {
		case ast.KindInterfaceDeclaration:
			members = decl.AsInterfaceDeclaration().Members.Nodes
		case ast.KindTypeAliasDeclaration:
			typeNode := declaredTypeNode(decl)
			if typeNode != nil && typeNode.Kind == ast.KindTypeLiteral {
				members = typeNode.AsTypeLiteralNode().Members.Nodes
			} else if typeNode != nil && typeNode.Kind == ast.KindMappedType {
				// Mapped types like { [k in number]: T } are numeric index types.
				mt := typeNode.AsMappedTypeNode()
				if mt.TypeParameter != nil {
					tp := mt.TypeParameter.AsTypeParameter()
					if tp.Constraint != nil && tp.Constraint.Kind == ast.KindNumberKeyword {
						return true
					}
				}
				return false
			} else {
				return false
			}
		default:
			return false
		}
		if len(members) == 0 {
			return false
		}
		for _, m := range members {
			if m == nil {
				continue
			}
			if m.Kind != ast.KindIndexSignature {
				return false
			}
			// Check if the index is numeric
			sig := m.AsIndexSignatureDeclaration()
			if sig.Parameters.Nodes == nil || len(sig.Parameters.Nodes) == 0 {
				return false
			}
			param := sig.Parameters.Nodes[0]
			if param.Type() == nil || param.Type().Kind != ast.KindNumberKeyword {
				return false
			}
		}
	}
	return true
}

// descriptionForSymbol returns the JSDoc description for a symbol's
// canonical declaration, or "" if none is found.
func (g *Generator) descriptionForSymbol(sym *ast.Symbol) string {
	if sym == nil {
		return ""
	}
	decl := canonicalDeclaration(sym)
	if decl != nil {
		if docs := g.parseDocs(decl); docs != nil && docs.description != "" {
			return docs.description
		}
	}
	if sym.ValueDeclaration != nil {
		if docs := g.parseDocs(sym.ValueDeclaration); docs != nil && docs.description != "" {
			return docs.description
		}
	}
	for _, d := range sym.Declarations {
		if docs := g.parseDocs(d); docs != nil && docs.description != "" {
			return docs.description
		}
	}
	return ""
}

func (g *Generator) applyDocs(schema Schema, docs *docInfo) {
	if docs == nil {
		return
	}
	if docs.description != "" {
		schema["description"] = docs.description
	}
	if docs.title != "" {
		schema["title"] = docs.title
	}
	if docs.comment != "" {
		schema["$comment"] = docs.comment
	}
	if docs.id != "" {
		schema["$id"] = docs.id
	}
	if docs.ref != "" {
		schema["$ref"] = docs.ref
	}
	if docs.nullable {
		makeNullable(schema)
	}
	for k, v := range docs.fields {
		if _, ok := schema["$ref"]; ok && !isRefSafeDocField(k) {
			continue
		}
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

// applyDocsNonOverwrite is like applyDocs but only sets keys that are
// not already populated in the schema.  This is used when merging JSDoc
// annotations from a declaration whose type was already fully resolved
// (e.g. inside schemaFromObjectLiteralTypeNode), so that type-inferred
// values like "enum" are not clobbered by raw annotation text.
func (g *Generator) applyDocsNonOverwrite(schema Schema, docs *docInfo) {
	if docs == nil {
		return
	}
	if docs.description != "" {
		if _, ok := schema["description"]; !ok {
			schema["description"] = docs.description
		}
	}
	if docs.title != "" {
		if _, ok := schema["title"]; !ok {
			schema["title"] = docs.title
		}
	}
	if docs.comment != "" {
		if _, ok := schema["$comment"]; !ok {
			schema["$comment"] = docs.comment
		}
	}
	if docs.id != "" {
		if _, ok := schema["$id"]; !ok {
			schema["$id"] = docs.id
		}
	}
	if docs.ref != "" {
		if _, ok := schema["$ref"]; !ok {
			schema["$ref"] = docs.ref
		}
	}
	if docs.nullable {
		makeNullable(schema)
	}
	for k, v := range docs.fields {
		if _, ok := schema["$ref"]; ok && !isRefSafeDocField(k) {
			continue
		}
		if _, exists := schema[k]; exists {
			continue
		}
		schema[k] = v
	}
}

type docInfo struct {
	description string
	title       string
	comment     string
	id          string
	ref         string
	nullable    bool
	ignore      bool
	fields      map[string]any
}

func (g *Generator) parseDocs(node any) *docInfo {
	n := asNode(node)
	if n == nil {
		return nil
	}
	jsdocs := n.JSDoc(nil)
	if len(jsdocs) == 0 {
		return nil
	}
	info := &docInfo{fields: map[string]any{}}
	var descParts []string
	for _, jsdoc := range jsdocs {
		if jsdoc == nil {
			continue
		}
		if c := jsdoc.CommentList(); c != nil {
			txt := jsdocText(c)
			if txt != "" {
				descParts = append(descParts, txt)
			}
		}
		for _, tag := range jsdoc.Comments() {
			_ = tag
		}
		if jsdoc.AsJSDoc().Tags == nil {
			continue
		}
		for _, tag := range jsdoc.AsJSDoc().Tags.Nodes {
			if tag == nil {
				continue
			}
			name := tag.TagName().Text()
			text := jsdocText(tag.CommentList())
			if strings.HasPrefix(name, "TJS-") {
				name = strings.TrimPrefix(name, "TJS-")
			}
			if strings.Contains(name, ".") {
				head, tail, _ := strings.Cut(name, ".")
				if isSchemaKeyword(head) || strings.HasPrefix(head, "$") {
					if val, ok := parseAnnotationValue(text); ok {
						field := map[string]any{}
						if existing, ok := info.fields[head].(map[string]any); ok {
							field = existing
						}
						field[tail] = val
						info.fields[head] = field
						continue
					}
					if text != "" {
						field := map[string]any{}
						if existing, ok := info.fields[head].(map[string]any); ok {
							field = existing
						}
						field[tail] = text
						info.fields[head] = field
						continue
					}
				}
			}
			switch name {
			case "description":
				if text != "" {
					info.description = text
				}
			case "title":
				info.title = text
			case "comment", "$comment":
				info.comment = text
			case "$id", "id":
				info.id = text
			case "$ref", "ref":
				info.ref = text
			case "nullable":
				info.nullable = true
			case "ignore":
				info.ignore = true
			case "default":
				if val, ok := parseAnnotationValue(text); ok {
					info.fields["default"] = val
				} else if text != "" {
					info.fields["default"] = text
				}
			default:
				if v, ok := g.parseKeyword(name, text); ok {
					info.fields[name] = mergeDocFieldValue(info.fields[name], v)
				}
			}
		}
	}
	if len(descParts) > 0 {
		info.description = strings.TrimSpace(strings.Join(descParts, " "))
	}
	return info
}

func isSchemaKeyword(name string) bool {
	switch name {
	case "type", "format", "pattern", "minimum", "maximum", "minLength", "maxLength", "minItems", "maxItems", "uniqueItems", "additionalProperties", "examples", "items", "additionalItems", "enum", "const", "oneOf", "anyOf", "allOf", "properties", "definitions":
		return true
	default:
		return false
	}
}

func isRefSafeDocField(name string) bool {
	switch name {
	case "default", "examples", "description", "title", "$comment", "$id", "$ref", "nullable":
		return true
	default:
		return strings.HasPrefix(name, "$")
	}
}

func mergeDocFieldValue(existing any, next any) any {
	if existingMap, ok := existing.(map[string]any); ok {
		if nextMap, ok := next.(map[string]any); ok {
			for k, v := range nextMap {
				existingMap[k] = v
			}
			return existingMap
		}
	}
	if existing != nil {
		return existing
	}
	return next
}

// evaluateRequireExpr evaluates a `require('./path').exportName` expression
// by reading the referenced TypeScript file and extracting the exported const value.
func (g *Generator) evaluateRequireExpr(text string, contextNode *ast.Node) (any, bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "require(") {
		return nil, false
	}
	// Parse: require('path') or require('path').member
	rest := strings.TrimPrefix(text, "require(")
	closeIdx := strings.Index(rest, ")")
	if closeIdx < 0 {
		return nil, false
	}
	pathStr := strings.TrimSpace(rest[:closeIdx])
	member := strings.TrimSpace(rest[closeIdx+1:])
	if member != "" {
		member = strings.TrimPrefix(member, ".")
	}

	// Strip quotes from path
	pathStr = strings.Trim(pathStr, "'\"")

	// Resolve relative to the source file containing the annotation
	contextDir := g.rootDir
	if contextNode != nil {
		if sf := sourceFileForNode(contextNode); sf != nil {
			contextDir = filepath.Dir(sf.FileName())
		}
	}
	resolved := filepath.Join(contextDir, pathStr)

	// Try with .ts extension
	candidates := []string{resolved, resolved + ".ts", resolved + "/index.ts"}
	var targetFile string
	for _, candidate := range candidates {
		// Check in program source files
		for _, sf := range g.program.GetSourceFiles() {
			if sf != nil && sf.FileName() == candidate {
				targetFile = candidate
				break
			}
		}
		if targetFile != "" {
			break
		}
	}
	if targetFile == "" {
		// For self-references like require("."), check the annotation's own file
		if member != "" && contextNode != nil {
			if sf := sourceFileForNode(contextNode); sf != nil {
				for _, stmt := range sf.Statements.Nodes {
					if stmt == nil {
						continue
					}
					if stmt.Kind == ast.KindVariableStatement {
						if dl := stmt.AsVariableStatement().DeclarationList; dl != nil {
							for _, decl := range dl.AsVariableDeclarationList().Declarations.Nodes {
								if decl != nil {
									if dsym := decl.Symbol(); dsym != nil && dsym.Name == member {
										if val, ok := evaluateLiteral(decl.Initializer()); ok {
											return val, true
										}
									}
								}
							}
						}
					}
				}
			}
		}
		return nil, false
	}

	// Find the target source file
	var targetSF *ast.SourceFile
	for _, sf := range g.program.GetSourceFiles() {
		if sf != nil && sf.FileName() == targetFile {
			targetSF = sf
			break
		}
	}
	if targetSF == nil {
		return nil, false
	}

	// Look for the exported variable in the target file.
	// Helper to find a named variable initializer in a list of statements.
	findNamedInit := func(stmts []*ast.Node, varName string) *ast.Node {
		for _, stmt := range stmts {
			if stmt == nil {
				continue
			}
			// Check direct symbol name
			if sym := stmt.Symbol(); sym != nil && sym.Name == varName {
				if init := findVariableInitializer(stmt); init != nil {
					return init
				}
			}
			// For VariableStatements, check individual declarations
			if stmt.Kind == ast.KindVariableStatement {
				if dl := stmt.AsVariableStatement().DeclarationList; dl != nil {
					for _, decl := range dl.AsVariableDeclarationList().Declarations.Nodes {
						if decl != nil {
							if dsym := decl.Symbol(); dsym != nil && dsym.Name == varName {
								return decl.Initializer()
							}
						}
					}
				}
			}
		}
		return nil
	}
	if member != "" {
		if init := findNamedInit(targetSF.Statements.Nodes, member); init != nil {
			if val, ok := evaluateLiteral(init); ok {
				return val, true
			}
		}
	} else {
		// For `require('./path')` without member, look for default export
		for _, stmt := range targetSF.Statements.Nodes {
			if stmt == nil {
				continue
			}
			if stmt.Kind == ast.KindExportAssignment {
				if expr := stmt.Expression(); expr != nil {
					if val, ok := evaluateLiteral(expr); ok {
						return val, true
					}
					// If the expression is an identifier, resolve it
					if expr.Kind == ast.KindIdentifier {
						if init := findNamedInit(targetSF.Statements.Nodes, expr.Text()); init != nil {
							if val, ok := evaluateLiteral(init); ok {
								return val, true
							}
						}
					}
				}
			}
		}
	}

	return nil, false
}

// findVariableInitializer finds the initializer expression of a variable
// declaration statement.
func findVariableInitializer(node *ast.Node) *ast.Node {
	if node == nil {
		return nil
	}
	if node.Kind == ast.KindVariableStatement {
		if dl := node.AsVariableStatement().DeclarationList; dl != nil {
			for _, decl := range dl.AsVariableDeclarationList().Declarations.Nodes {
				if decl != nil {
					return decl.Initializer()
				}
			}
		}
	}
	return node.Initializer()
}

func (g *Generator) parseKeyword(name, text string) (any, bool) {
	if g.opts.ValidationKeywords != nil && g.opts.ValidationKeywords[name] {
		if name == "hide" && text == "" {
			return true, true
		}
		if val, ok := parseAnnotationValue(text); ok {
			return val, true
		}
		if text == "" {
			return "", true
		}
		return text, true
	}
	switch name {
	case "type", "format", "pattern", "minimum", "maximum", "minLength", "maxLength", "minItems", "maxItems", "uniqueItems", "additionalProperties", "examples", "items", "additionalItems", "enum", "const", "oneOf", "anyOf", "allOf", "properties", "definitions":
		if strings.Contains(text, ".") && !strings.HasPrefix(text, "{") && !strings.HasPrefix(text, "[") {
			parts := strings.SplitN(text, " ", 2)
			if len(parts) == 2 {
				subName := strings.TrimPrefix(parts[0], ".")
				subVal, _ := parseAnnotationValue(parts[1])
				return map[string]any{subName: subVal}, true
			}
		}
		val, ok := parseAnnotationValue(text)
		if !ok && text != "" {
			val = text
			ok = true
		}
		return val, ok
	}
	if strings.HasPrefix(name, "$") {
		if val, ok := parseAnnotationValue(text); ok {
			return val, true
		}
		return text, true
	}
	return nil, false
}

func (g *Generator) outputNameForSymbol(sym *ast.Symbol) string {
	if sym == nil {
		return ""
	}
	// Return cached name if we've already assigned one for this symbol.
	if cached, ok := g.symbolOutputName[sym]; ok {
		return cached
	}
	name := qualifiedSymbolName(sym)
	if name == "" {
		name = "anonymous"
	}
	// When AliasRef is on and the symbol is a non-exported interface that
	// has recursive references through an EXPORTED alias, use anonymous
	// naming like "interface-0" to match the original typescript-json-schema
	// behavior.  The interface is "anonymous" because it's only reachable
	// through the exported alias.
	if g.opts.AliasRef && sym.Flags&ast.SymbolFlagsInterface != 0 && !isExportedSymbol(sym) {
		if g.hasRecursiveExportedAliasRef(sym) {
			name = fmt.Sprintf("interface-%d", g.anonInterfaceCounter)
			g.anonInterfaceCounter++
		}
	}
	if g.opts.UniqueNames {
		if decl := canonicalDeclaration(sym); decl != nil {
			rel := uniqueNameRelativePath(declRelativePath(decl, g.rootDir))
			sum := md5.Sum([]byte(rel + strconv.Itoa(decl.Pos())))
			name = fmt.Sprintf("%s.%s", name, fmt.Sprintf("%x", sum[:])[:8])
		}
	}
	// Disambiguate when multiple distinct symbols share the same base name
	// (e.g. MyInterface imported from different files).
	candidate := name
	if owner, taken := g.outputNameOwner[candidate]; taken && owner != sym {
		suffix := 1
		for {
			candidate = fmt.Sprintf("%s_%d", name, suffix)
			if owner2, taken2 := g.outputNameOwner[candidate]; !taken2 || owner2 == sym {
				break
			}
			suffix++
		}
	}
	g.symbolOutputName[sym] = candidate
	g.outputNameOwner[candidate] = sym
	return candidate
}

func (g *Generator) refURI(name string) string {
	if g.opts.ID != "" {
		return g.opts.ID + "#/definitions/" + name
	}
	return "#/definitions/" + name
}

// hasRecursiveExportedAliasRef returns true when a non-exported interface
// has a recursive reference through an EXPORTED type alias.
func (g *Generator) hasRecursiveExportedAliasRef(sym *ast.Symbol) bool {
	name := sym.Name
	for _, decl := range sym.Declarations {
		if decl == nil || decl.Kind != ast.KindInterfaceDeclaration {
			continue
		}
		for _, member := range decl.AsInterfaceDeclaration().Members.Nodes {
			if member == nil {
				continue
			}
			typeNode := member.Type()
			if typeNode == nil || typeNode.Kind != ast.KindTypeReference {
				continue
			}
			refSym := symbolForTypeNode(g.checker, typeNode)
			if refSym == nil || refSym.Flags&ast.SymbolFlagsTypeAlias == 0 {
				continue
			}
			if !isExportedSymbol(refSym) {
				continue
			}
			// Check if this exported alias resolves back to our interface
			targetNode := declaredTypeNode(canonicalDeclaration(refSym))
			if targetNode != nil && targetNode.Kind == ast.KindTypeReference {
				targetName := entityNameText(targetNode.AsTypeReferenceNode().TypeName)
				if targetName == name {
					return true
				}
			}
		}
	}
	return false
}

func (g *Generator) reset() {
	g.definitions = map[string]Schema{}
	g.typeDefinitions = map[string]*tsType{}
	g.inProgress = map[string]bool{}
	g.nullableTypes = map[*ast.Symbol]bool{}
	g.symbolOutputName = map[*ast.Symbol]string{}
	g.outputNameOwner = map[string]*ast.Symbol{}
	g.anonInterfaceCounter = 0
	g.aliasTargetSymbols = map[*ast.Symbol]bool{}
	g.inConcreteContext = false
}

func (g *Generator) collectSymbols() {
	g.symbolsByName = map[string][]*ast.Symbol{}
	for _, sym := range g.collectTopLevelSymbols() {
		g.symbolsByName[sym.Name] = append(g.symbolsByName[sym.Name], sym)
	}
}

func (g *Generator) collectTopLevelSymbols() []*ast.Symbol {
	seen := map[*ast.Symbol]bool{}
	var out []*ast.Symbol
	for _, file := range g.program.GetSourceFiles() {
		if file == nil || file.IsDeclarationFile {
			continue
		}
		for _, stmt := range file.Statements.Nodes {
			if stmt == nil {
				continue
			}
			sym := stmt.Symbol()
			if sym == nil || seen[sym] {
				continue
			}
			switch stmt.Kind {
			case ast.KindInterfaceDeclaration, ast.KindClassDeclaration, ast.KindTypeAliasDeclaration, ast.KindEnumDeclaration, ast.KindModuleDeclaration, ast.KindVariableStatement:
				seen[sym] = true
				out = append(out, sym)
			}
		}
	}
	return out
}

func (g *Generator) lookupSymbols(name string) []*ast.Symbol {
	if strings.Contains(name, ".") {
		parts := splitQualifiedName(name)
		current := g.lookupSymbols(parts[0])
		for _, part := range parts[1:] {
			var next []*ast.Symbol
			for _, sym := range current {
				if child := lookupChildSymbol(sym, part); child != nil {
					next = append(next, child)
				}
			}
			current = next
		}
		return current
	}
	if syms := g.symbolsByName[name]; len(syms) > 0 {
		return syms
	}
	// Search inside module/namespace declarations for the symbol.
	for _, syms := range g.symbolsByName {
		for _, sym := range syms {
			if sym.Flags&ast.SymbolFlagsModule == 0 {
				continue
			}
			if child := lookupChildSymbol(sym, name); child != nil {
				return []*ast.Symbol{child}
			}
			// Also search module body AST for non-exported members.
			if child := lookupModuleBodySymbol(sym, name); child != nil {
				return []*ast.Symbol{child}
			}
		}
	}
	return nil
}

func lookupChildSymbol(sym *ast.Symbol, name string) *ast.Symbol {
	if sym == nil {
		return nil
	}
	if sym.Exports != nil {
		if child, ok := sym.Exports[name]; ok {
			return child
		}
	}
	if sym.Members != nil {
		if child, ok := sym.Members[name]; ok {
			return child
		}
	}
	return nil
}

// lookupModuleBodySymbol walks the AST body of a module/namespace declaration
// to find a symbol by name. This handles non-exported members that are not in
// the symbol's Exports or Members maps.
func lookupModuleBodySymbol(sym *ast.Symbol, name string) *ast.Symbol {
	if sym == nil {
		return nil
	}
	for _, decl := range sym.Declarations {
		if decl == nil || decl.Kind != ast.KindModuleDeclaration {
			continue
		}
		body := decl.AsModuleDeclaration().Body
		if body == nil || body.Kind != ast.KindModuleBlock {
			continue
		}
		for _, stmt := range body.AsModuleBlock().Statements.Nodes {
			if stmt == nil {
				continue
			}
			innerSym := stmt.Symbol()
			if innerSym != nil && innerSym.Name == name {
				return innerSym
			}
		}
	}
	return nil
}

// isIntegerAlias returns true when sym is the well-known
// "type integer = number" alias used to produce JSON Schema "integer".
func (g *Generator) isIntegerAlias(sym *ast.Symbol) bool {
	if sym == nil || sym.Flags&ast.SymbolFlagsTypeAlias == 0 || sym.Name != "integer" {
		return false
	}
	resolved := g.declaredTypeForSymbol(sym)
	return resolved != nil && resolved.Flags()&checker.TypeFlagsNumber != 0
}

func (g *Generator) numberType() string {
	if g.opts.DefaultNumberType == "integer" {
		return "integer"
	}
	return "number"
}

func declInitializer(node *ast.Node) *ast.Node {
	if node == nil {
		return nil
	}
	switch node.Kind {
	case ast.KindPropertyDeclaration, ast.KindPropertySignature, ast.KindParameter:
		return node.Initializer()
	}
	return nil
}

func evaluateLiteral(node *ast.Node) (any, bool) {
	if node == nil {
		return nil, false
	}
	switch node.Kind {
	case ast.KindTrueKeyword:
		return true, true
	case ast.KindFalseKeyword:
		return false, true
	case ast.KindNullKeyword:
		return nil, true
	case ast.KindStringLiteral, ast.KindNoSubstitutionTemplateLiteral:
		return node.Text(), true
	case ast.KindNumericLiteral:
		if f, err := strconv.ParseFloat(node.Text(), 64); err == nil {
			return f, true
		}
		return node.Text(), true
	case ast.KindArrayLiteralExpression:
		var arr []any
		for _, el := range node.Elements() {
			if el == nil {
				continue
			}
			v, ok := evaluateLiteral(el)
			if !ok {
				return nil, false
			}
			arr = append(arr, v)
		}
		return arr, true
	case ast.KindObjectLiteralExpression:
		obj := map[string]any{}
		for _, p := range node.Properties() {
			if p == nil {
				continue
			}
			if p.Kind != ast.KindPropertyAssignment {
				return nil, false
			}
			name := p.Name().Text()
			v, ok := evaluateLiteral(p.Initializer())
			if !ok {
				return nil, false
			}
			obj[name] = v
		}
		return obj, true
	case ast.KindAsExpression, ast.KindTypeAssertionExpression, ast.KindParenthesizedExpression:
		return evaluateLiteral(node.Expression())
	default:
		return nil, false
	}
}

func normalizeSchemaScalar(v any) any {
	switch x := v.(type) {
	case int:
		return float64(x)
	case int8:
		return float64(x)
	case int16:
		return float64(x)
	case int32:
		return float64(x)
	case int64:
		return float64(x)
	case uint:
		return float64(x)
	case uint8:
		return float64(x)
	case uint16:
		return float64(x)
	case uint32:
		return float64(x)
	case uint64:
		return float64(x)
	case float32:
		return float64(x)
	case jsnum.Number:
		return float64(x)
	default:
		return v
	}
}

func enumValueTypes(values []any, numberType string) []string {
	var out []string
	for _, value := range values {
		var kind string
		switch value.(type) {
		case nil:
			kind = "null"
		case string:
			kind = "string"
		case bool:
			kind = "boolean"
		case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, jsnum.Number:
			kind = numberType
		}
		if kind != "" {
			out = append(out, kind)
		}
	}
	return uniqueStringsStable(out)
}

func stringLiteralTypeKeys(node *ast.Node) []string {
	if node == nil {
		return nil
	}
	switch node.Kind {
	case ast.KindLiteralType:
		text := strings.Trim(node.AsLiteralTypeNode().Literal.Text(), `"'`)
		if text == "" {
			return nil
		}
		return []string{text}
	case ast.KindUnionType:
		var out []string
		for _, member := range node.AsUnionTypeNode().Types.Nodes {
			out = append(out, stringLiteralTypeKeys(member)...)
		}
		return uniqueStringsStable(out)
	default:
		return nil
	}
}

func pickObjectSchema(schema Schema, keys []string) Schema {
	if schema == nil || len(keys) == 0 {
		return schema
	}
	filtered := cloneSchema(schema)
	if props, ok := schema["properties"].(map[string]any); ok {
		nextProps := map[string]any{}
		for _, key := range keys {
			if prop, ok := props[key]; ok {
				nextProps[key] = prop
			}
		}
		filtered["properties"] = nextProps
	}
	if required := asStrings(schema["required"]); len(required) > 0 {
		keep := map[string]bool{}
		for _, key := range keys {
			keep[key] = true
		}
		var nextRequired []string
		for _, key := range required {
			if keep[key] {
				nextRequired = append(nextRequired, key)
			}
		}
		if len(nextRequired) > 0 {
			filtered["required"] = nextRequired
		} else {
			delete(filtered, "required")
		}
	}
	return filtered
}

func parseAnnotationValue(text string) (any, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", false
	}
	var v any
	if err := json.Unmarshal([]byte(trimmed), &v); err == nil {
		return v, true
	}
	if trimmed == "true" {
		return true, true
	}
	if trimmed == "false" {
		return false, true
	}
	if n, err := strconv.ParseFloat(trimmed, 64); err == nil {
		return n, true
	}
	return trimmed, false
}

func (g *Generator) useEnumFormat() bool {
	return g.opts.ConstAsEnum || g.inConcreteContext
}

func withConst(schema Schema, value any, constAsEnum bool) Schema {
	if constAsEnum {
		schema["enum"] = []any{value}
	} else {
		schema["const"] = value
	}
	return schema
}

func overlayParsedSchema(parsed Schema, overlay Schema) Schema {
	result := cloneSchema(parsed)
	if result == nil {
		result = Schema{}
	}
	nullable := false
	if overlay != nil {
		if docNullableWrapper(overlay) {
			nullable = true
			delete(overlay, "anyOf")
		}
		for k, v := range overlay {
			result[k] = v
		}
	}
	if nullable {
		makeNullable(result)
	}
	return result
}

func docNullableWrapper(schema Schema) bool {
	anyOf, ok := schema["anyOf"].([]any)
	if !ok || len(anyOf) != 2 {
		return false
	}
	first, ok := anyOf[0].(Schema)
	if !ok || len(first) != 0 {
		return false
	}
	second, ok := anyOf[1].(Schema)
	if !ok {
		return false
	}
	return len(second) == 1 && second["type"] == "null"
}

func makeNullable(schema Schema) {
	if schema == nil {
		return
	}
	if t, ok := schema["type"].(string); ok {
		// When the schema has structural properties beyond just "type"
		// (e.g. "items", "properties", "required", etc.), use anyOf
		// wrapping so the null branch is a separate schema object.
		if schemaHasStructuralProps(schema) {
			original := cloneSchema(schema)
			for k := range schema {
				delete(schema, k)
			}
			schema["anyOf"] = []any{original, Schema{"type": "null"}}
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
		schema["anyOf"] = []any{Schema{"$ref": ref}, Schema{"type": "null"}}
		return
	}
	if _, ok := schema["anyOf"]; ok {
		return
	}
	original := cloneSchema(schema)
	for k := range schema {
		delete(schema, k)
	}
	schema["anyOf"] = []any{original, Schema{"type": "null"}}
}

// schemaHasStructuralProps returns true if the schema contains properties
// beyond just "type" that define the structure of the type (items, properties,
// etc.).  When such properties exist, nullable wrapping should use anyOf
// rather than the type-array shorthand.
func schemaHasStructuralProps(schema Schema) bool {
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

func canonicalDeclaration(sym *ast.Symbol) *ast.Node {
	if sym == nil {
		return nil
	}
	if len(sym.Declarations) > 0 {
		return sym.Declarations[0]
	}
	return sym.ValueDeclaration
}

func declRelativePath(node *ast.Node, root string) string {
	if node == nil {
		return ""
	}
	file := sourceFileForNode(node)
	if file == nil {
		return ""
	}
	rel, err := filepath.Rel(root, file.FileName())
	if err != nil {
		return file.FileName()
	}
	return filepath.ToSlash(rel)
}

func uniqueNameRelativePath(rel string) string {
	return filepath.ToSlash(rel)
}

func sourceFileForNode(node *ast.Node) *ast.SourceFile {
	for n := node; n != nil; n = n.Parent {
		if n.Kind == ast.KindSourceFile {
			return n.AsSourceFile()
		}
	}
	return nil
}

func asNode(v any) *ast.Node {
	switch n := v.(type) {
	case *ast.Node:
		return n
	case *ast.Symbol:
		if n == nil {
			return nil
		}
		if n.ValueDeclaration != nil {
			return n.ValueDeclaration
		}
		if len(n.Declarations) > 0 {
			return n.Declarations[0]
		}
	}
	return nil
}

func jsdocText(list *ast.NodeList) string {
	if list == nil {
		return ""
	}
	var parts []string
	for _, n := range list.Nodes {
		if n == nil {
			continue
		}
		switch n.Kind {
		case ast.KindJSDocLink:
			parts = append(parts, "{@link "+jsdocLinkName(n.AsJSDocLink().Name())+"}")
		case ast.KindJSDocLinkCode:
			parts = append(parts, "{@linkcode "+jsdocLinkName(n.AsJSDocLinkCode().Name())+"}")
		case ast.KindJSDocLinkPlain:
			parts = append(parts, "{@linkplain "+jsdocLinkName(n.AsJSDocLinkPlain().Name())+"}")
		default:
			parts = append(parts, n.Text())
		}
	}
	return strings.TrimSpace(strings.Join(parts, ""))
}

func jsdocLinkName(node *ast.DeclarationName) string {
	if node == nil {
		return ""
	}
	return node.Text()
}

func cloneSchema(in Schema) Schema {
	if in == nil {
		return nil
	}
	out := Schema{}
	for k, v := range in {
		out[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, v2 := range val {
			out[k] = cloneValue(v2)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, v2 := range val {
			out[i] = cloneValue(v2)
		}
		return out
	default:
		return v
	}
}

// resolveAliasRefName resolves a type reference through aliases to get the
// underlying generic type name.  For example, SomeAlias<"alias"> where
// `type SomeAlias<T> = SomeGeneric<T, T>` resolves to SomeGeneric<"alias","alias">.
func resolveAliasRefName(ch *checker.Checker, node *ast.Node) string {
	if node == nil || node.Kind != ast.KindTypeReference {
		return ""
	}
	sym := symbolForTypeNode(ch, node)
	if sym == nil || sym.Flags&ast.SymbolFlagsTypeAlias == 0 {
		return typeNodeString(node)
	}
	// Get the alias body
	aliasDecl := canonicalDeclaration(sym)
	aliasBody := declaredTypeNode(aliasDecl)
	if aliasBody == nil || aliasBody.Kind != ast.KindTypeReference {
		return typeNodeString(node)
	}
	// Build a map from type parameters to actual arguments
	typeParams := typeAliasTypeParams(aliasDecl)
	typeArgs := node.TypeArguments()
	if len(typeParams) == 0 || len(typeParams) != len(typeArgs) {
		return typeNodeString(node)
	}
	paramMap := map[string]string{}
	for i, param := range typeParams {
		paramMap[param] = typeNodeString(typeArgs[i])
	}
	// Substitute parameters in the alias body
	return substituteTypeRefName(aliasBody, paramMap)
}

// typeAliasTypeParams extracts type parameter names from a type alias declaration.
func typeAliasTypeParams(node *ast.Node) []string {
	if node == nil || node.Kind != ast.KindTypeAliasDeclaration {
		return nil
	}
	tp := node.TypeParameters()
	if len(tp) == 0 {
		return nil
	}
	var names []string
	for _, param := range tp {
		if param != nil {
			names = append(names, param.Name().Text())
		}
	}
	return names
}

// substituteTypeRefName substitutes type parameter names in a TypeReference
// with the actual argument strings.
func substituteTypeRefName(body *ast.Node, paramMap map[string]string) string {
	if body == nil {
		return ""
	}
	switch body.Kind {
	case ast.KindTypeReference:
		name := entityNameText(body.AsTypeReferenceNode().TypeName)
		// Check if the name itself is a type parameter
		if replacement, ok := paramMap[name]; ok {
			return replacement
		}
		args := body.TypeArguments()
		if len(args) == 0 {
			return name
		}
		var argStrs []string
		for _, arg := range args {
			argStrs = append(argStrs, substituteTypeRefName(arg, paramMap))
		}
		return name + "<" + strings.Join(argStrs, ",") + ">"
	default:
		// For other node kinds, just substitute the text
		text := typeNodeString(body)
		if replacement, ok := paramMap[text]; ok {
			return replacement
		}
		return text
	}
}

func normalizeDefinitionName(s string) string {
	if s == "" {
		return s
	}
	// Replace single quotes with double quotes for consistency.
	s = strings.ReplaceAll(s, "'", "\"")
	var b strings.Builder
	depthAngle := 0
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch ch {
		case '<':
			depthAngle++
			b.WriteByte(ch)
		case '>':
			if depthAngle > 0 {
				depthAngle--
			}
			b.WriteByte(ch)
		case ',':
			b.WriteByte(ch)
			if depthAngle > 0 {
				for i+1 < len(s) && s[i+1] == ' ' {
					i++
				}
			}
		default:
			b.WriteByte(ch)
		}
	}
	return b.String()
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func uniqueStringsStable(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func asStrings(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		return anyToStrings(x)
	default:
		return nil
	}
}

func anyToStrings(v []any) []string {
	out := make([]string, 0, len(v))
	for _, x := range v {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func allSameType(vals []any, kind string) bool {
	if len(vals) == 0 {
		return false
	}
	for _, v := range vals {
		switch kind {
		case "string":
			if _, ok := v.(string); !ok {
				return false
			}
		case "bool":
			if _, ok := v.(bool); !ok {
				return false
			}
		case "num":
			switch v.(type) {
			case float64, float32, int, int32, int64, uint, uint32, uint64:
			default:
				return false
			}
		}
	}
	return true
}

// filterSubsumedEnumVals removes enum values that are covered by the given
// simple types.  For example, string literal enum values are subsumed by the
// "string" simple type.
func filterSubsumedEnumVals(enumVals []any, simpleTypes []string) []any {
	subsumes := map[string]bool{}
	for _, st := range simpleTypes {
		subsumes[st] = true
	}
	var out []any
	for _, v := range enumVals {
		switch v.(type) {
		case string:
			if subsumes["string"] {
				continue
			}
		case bool:
			if subsumes["boolean"] {
				continue
			}
		case float64, float32, int, int32, int64, uint, uint32, uint64:
			if subsumes["number"] || subsumes["integer"] {
				continue
			}
		}
		out = append(out, v)
	}
	return out
}

// templateLiteralPattern builds a JSON Schema "pattern" regex from a
// TypeScript template literal type.  Returns "" if the pattern cannot
// be expressed (e.g. when a span type is itself a union).
func templateLiteralPattern(t *checker.Type) string {
	tl := t.AsTemplateLiteralType()
	texts := tl.Texts()
	types := tl.Types()
	if len(texts) != len(types)+1 {
		return ""
	}
	var buf strings.Builder
	buf.WriteByte('^')
	for i, typ := range types {
		buf.WriteString(regexpQuoteMeta(texts[i]))
		switch {
		case typ.Flags()&checker.TypeFlagsString != 0:
			buf.WriteString(".*")
		case typ.Flags()&checker.TypeFlagsNumber != 0:
			buf.WriteString("[0-9]*")
		case typ.Flags()&checker.TypeFlagsBigInt != 0:
			buf.WriteString("[0-9]*")
		default:
			// Unsupported interpolation type.
			return ""
		}
	}
	buf.WriteString(regexpQuoteMeta(texts[len(texts)-1]))
	buf.WriteByte('$')
	return buf.String()
}

// regexpQuoteMeta escapes special regex characters in a literal string.
func regexpQuoteMeta(s string) string {
	const special = `\.+*?^${}()|[]`
	var buf strings.Builder
	for _, c := range s {
		if strings.ContainsRune(special, c) {
			buf.WriteByte('\\')
		}
		buf.WriteRune(c)
	}
	return buf.String()
}

func containsStr(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func removeStr(ss []string, s string) []string {
	var out []string
	for _, v := range ss {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

// isInternalSymbolName returns true if the name looks like a checker-internal
// symbol (e.g. "__type", "\ufffdtype") that should not be used as a definition key.
func isInternalSymbolName(name string) bool {
	if name == "" {
		return true
	}
	if strings.HasPrefix(name, "__") {
		return true
	}
	// The Go TypeScript checker uses \ufffd (Unicode replacement character)
	// as a prefix for synthesized/internal type names.
	if strings.ContainsRune(name, '\ufffd') {
		return true
	}
	return false
}

// isWellKnownSymbolProp returns true when the property name represents a
// well-known Symbol property (e.g. [Symbol.hasInstance], [Symbol.iterator]).
// The Go TypeScript checker encodes these with the \ufffd (Unicode replacement
// character) prefix. Such properties have no meaningful JSON Schema
// representation.
func isWellKnownSymbolProp(name string) bool {
	return strings.ContainsRune(name, '\ufffd')
}

func isPrivateProp(sym *ast.Symbol) bool {
	if sym == nil {
		return false
	}
	for _, decl := range sym.Declarations {
		if decl == nil {
			continue
		}
		if mods := decl.Modifiers(); mods != nil {
			for _, m := range mods.Nodes {
				if m != nil && m.Kind == ast.KindPrivateKeyword {
					return true
				}
			}
		}
	}
	return false
}

// extractInterfaceMembers gets property symbols from an interface's AST
// declarations when the checker API doesn't return them.
func extractInterfaceMembers(sym *ast.Symbol) []*ast.Symbol {
	if sym == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []*ast.Symbol
	// Try the symbol's own Members map.
	if sym.Members != nil {
		for name, memberSym := range sym.Members {
			if memberSym == nil || name == "" || strings.HasPrefix(name, "__") || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, memberSym)
		}
	}
	return out
}

// isGlobalInterfaceName returns true when the name matches a well-known global
// interface from the TypeScript standard library (e.g. Function, Object, Array).
// This is used as a fallback when the Go checker resolves a library interface
// to "any" with a transient symbol lacking declaration nodes (which happens
// when lib files are not loaded).
func isGlobalInterfaceName(name string) bool {
	switch name {
	case "Function", "Object", "Array", "String", "Boolean", "Number",
		"RegExp", "Error", "Date", "Promise", "Symbol", "Map", "Set",
		"WeakMap", "WeakSet", "ArrayBuffer", "DataView", "JSON", "Math":
		return true
	}
	return false
}

func sortSymbolsByDeclaration(symbols []*ast.Symbol) {
	sort.SliceStable(symbols, func(i, j int) bool {
		left := canonicalDeclaration(symbols[i])
		right := canonicalDeclaration(symbols[j])
		if left == nil || right == nil {
			return i < j
		}
		lf := declRelativePath(left, "")
		rf := declRelativePath(right, "")
		if lf != rf {
			return lf < rf
		}
		return left.Pos() < right.Pos()
	})
}

func isRequiredProp(sym *ast.Symbol, typ *checker.Type, schema Schema) bool {
	if sym == nil {
		return false
	}
	if sym.Flags&ast.SymbolFlagsOptional != 0 {
		return false
	}
	if decl := canonicalDeclaration(sym); decl != nil && typeNodeIncludesUndefinedOrVoid(declaredTypeNode(decl)) {
		return false
	}
	if typ != nil && (typ.Flags()&checker.TypeFlagsUndefined != 0 || typ.Flags()&checker.TypeFlagsVoid != 0) {
		return false
	}
	if schema != nil {
		if _, ok := schema["$ref"]; ok {
			return true
		}
	}
	return true
}

func typeNodeIncludesUndefinedOrVoid(node *ast.Node) bool {
	if node == nil {
		return false
	}
	switch node.Kind {
	case ast.KindUndefinedKeyword, ast.KindVoidKeyword:
		return true
	case ast.KindParenthesizedType:
		return typeNodeIncludesUndefinedOrVoid(node.AsParenthesizedTypeNode().Type)
	case ast.KindUnionType:
		for _, member := range node.AsUnionTypeNode().Types.Nodes {
			if typeNodeIncludesUndefinedOrVoid(member) {
				return true
			}
		}
	}
	return false
}

func nodeOrFallback(primary *ast.Node, fallback *ast.Node) *ast.Node {
	if primary != nil {
		return primary
	}
	return fallback
}

func (g *Generator) shouldRefSymbol(sym *ast.Symbol) bool {
	if sym == nil {
		return false
	}
	// Symbols with schema overrides are always emitted as $ref definitions.
	if _, hasOverride := g.overrides[sym.Name]; hasOverride {
		return true
	}
	if sym.Flags&ast.SymbolFlagsTypeParameter != 0 {
		return false
	}
	if isBuiltInDateAlias(g.checker, sym) {
		return false
	}
	// Library type alias symbols (e.g. ReturnType, Partial from .d.ts files)
	// should not be emitted as $ref definitions — they are resolved to their
	// concrete types.  Library interfaces/classes (e.g. Function) can still
	// be ref'd.
	if isLibSymbol(sym) && sym.Flags&ast.SymbolFlagsTypeAlias != 0 {
		return false
	}
	// Types that only have numeric index signatures (no string index, no
	// named properties) should be inlined rather than ref'd.
	if isNumericIndexOnlyType(sym) {
		return false
	}
	if sym.Flags&ast.SymbolFlagsTypeAlias != 0 {
		// Array type aliases are always inlined (never ref'd), regardless
		// of AliasRef / Ref settings.
		if isArrayAliasSymbol(sym) {
			return false
		}
		if g.opts.AliasRef {
			// When AliasRef is on, primitive aliases (e.g. type X = string)
			// that are not exported are inlined if the program has other
			// exported symbols.  This matches the original typescript-json-
			// schema behavior where private aliases stay inline.
			if isPrimitiveAliasSymbol(sym) && !isExportedSymbol(sym) && g.hasExportedSymbols() {
				return false
			}
			return true
		}
		// Primitive aliases (e.g. `type MyString = string`) are inlined
		// when AliasRef is off.
		if isPrimitiveAliasSymbol(sym) {
			return false
		}
		// Without AliasRef, simple reference aliases (type X = Y where Y
		// is a named type) and object literal aliases (type X = { ... })
		// are transparent/inlined.  Union/intersection aliases are still
		// emitted as definitions when Ref is on.
		if g.opts.Ref {
			// Merged namespace/type alias symbols always need definitions.
			if sym.Flags&(ast.SymbolFlagsNamespaceModule|ast.SymbolFlagsValueModule) != 0 {
				return true
			}
			// Simple reference aliases are transparent.
			if isSimpleReferenceAlias(sym) {
				return false
			}
			// Object literal aliases are inlined when AliasRef is off.
			if isObjectLiteralAlias(sym) {
				return false
			}
			return true
		}
		return false
	}
	return g.opts.Ref
}

func (g *Generator) shouldTopRefSymbol(sym *ast.Symbol) bool {
	return sym != nil && g.opts.TopRef
}

// hasExportedSymbols returns true if any top-level symbol in the program has
// the export keyword.  This is used to decide whether non-exported primitive
// type aliases should be inlined (when exports exist) or kept as definitions
// (when nothing is exported).
func (g *Generator) hasExportedSymbols() bool {
	for _, sym := range g.collectTopLevelSymbols() {
		if isExportedSymbol(sym) {
			return true
		}
	}
	return false
}

func (g *Generator) typeString(t *checker.Type) string {
	if t == nil {
		return ""
	}
	return g.checker.TypeToStringEx(t, nil, checker.TypeFormatFlagsUseStructuralFallback|checker.TypeFormatFlagsNoTruncation|checker.TypeFormatFlagsUseSingleQuotesForStringLiteralType)
}

func (g *Generator) schemaFromTypeString(text string, node *ast.Node) (Schema, bool, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, false, nil
	}
	if text == "any" || text == "unknown" {
		return Schema{}, true, nil
	}
	text = trimOuterParens(text)
	if text == "Date" && isBuiltinDateType(g.checker, nil, node) {
		return Schema{"type": "string", "format": "date-time"}, true, nil
	}
	if parts := splitTopLevel(text, '|'); len(parts) > 1 {
		return g.schemaFromUnionStrings(parts, node)
	}
	if parts := splitTopLevel(text, '&'); len(parts) > 1 {
		allOf := make([]any, 0, len(parts))
		for _, part := range parts {
			sub, ok, err := g.schemaFromTypeString(part, node)
			if err != nil {
				return nil, false, err
			}
			if !ok {
				return nil, false, nil
			}
			allOf = append(allOf, sub)
		}
		return Schema{"allOf": allOf}, true, nil
	}
	switch text {
	case "string":
		return Schema{"type": "string"}, true, nil
	case "number":
		return Schema{"type": g.numberType()}, true, nil
	case "boolean":
		return Schema{"type": "boolean"}, true, nil
	case "null":
		return Schema{"type": "null"}, true, nil
	case "object":
		return Schema{"type": "object", "properties": map[string]any{}, "additionalProperties": true}, true, nil
	case "bigint":
		return Schema{"type": g.numberType()}, true, nil
	}
	if text == "true" || text == "false" {
		s := Schema{"type": "boolean"}
		return withConst(s, text == "true", g.useEnumFormat()), true, nil
	}
	if len(text) >= 2 && ((text[0] == '\'' && text[len(text)-1] == '\'') || (text[0] == '"' && text[len(text)-1] == '"')) {
		s := Schema{"type": "string"}
		return withConst(s, text[1:len(text)-1], g.useEnumFormat()), true, nil
	}
	if _, err := strconv.ParseFloat(text, 64); err == nil {
		n, _ := strconv.ParseFloat(text, 64)
		s := Schema{"type": g.numberType()}
		return withConst(s, n, g.useEnumFormat()), true, nil
	}
	if inner, ok := extractGenericArgs(text, "Array<"); ok {
		elem, ok, err := g.schemaFromTypeString(inner, node)
		if err != nil || !ok {
			return nil, ok, err
		}
		return Schema{"type": "array", "items": elem}, true, nil
	}
	if inner, ok := extractGenericArgs(text, "ReadonlyArray<"); ok {
		elem, ok, err := g.schemaFromTypeString(inner, node)
		if err != nil || !ok {
			return nil, ok, err
		}
		return Schema{"type": "array", "items": elem}, true, nil
	}
	if strings.HasSuffix(text, "[]") {
		elem, ok, err := g.schemaFromTypeString(text[:len(text)-2], node)
		if err != nil || !ok {
			return nil, ok, err
		}
		return Schema{"type": "array", "items": elem}, true, nil
	}
	if strings.HasPrefix(text, "[") && strings.HasSuffix(text, "]") {
		return g.schemaFromTupleString(text[1:len(text)-1], node)
	}
	if strings.HasPrefix(text, "{") && strings.HasSuffix(text, "}") {
		return g.schemaFromObjectLiteralString(text[1:len(text)-1], node)
	}
	if refs := g.lookupSymbols(text); len(refs) == 1 {
		schema, err := g.typeSchema(g.declaredTypeForSymbol(refs[0]), refs[0], node, false)
		return schema, err == nil, err
	}
	return nil, false, nil
}

func (g *Generator) schemaFromUnionStrings(parts []string, node *ast.Node) (Schema, bool, error) {
	var simpleTypes []string
	var enumVals []any
	var anyOf []any
	seenSimple := map[string]bool{}
	for _, part := range parts {
		// Without strictNullChecks, strip null from unions.
		if !g.strictNullChecks && strings.TrimSpace(part) == "null" {
			continue
		}
		sub, ok, err := g.schemaFromTypeString(part, node)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			return nil, false, nil
		}
		if t, ok := sub["type"].(string); ok && len(sub) == 1 {
			if !seenSimple[t] {
				simpleTypes = append(simpleTypes, t)
				seenSimple[t] = true
			}
			continue
		}
		// Collect literal values (schemas with type + const/enum) into enumVals.
		if c, hasConst := sub["const"]; hasConst && len(sub) == 2 {
			enumVals = append(enumVals, c)
			continue
		}
		if e, hasEnum := sub["enum"]; hasEnum && len(sub) == 2 {
			if arr, ok := e.([]any); ok && len(arr) == 1 {
				enumVals = append(enumVals, arr[0])
				continue
			}
		}
		anyOf = append(anyOf, sub)
	}

	// When boolean appears alongside other literal values, decompose it.
	if len(enumVals) > 0 && containsStr(simpleTypes, "boolean") {
		enumVals = append(enumVals, true, false)
		simpleTypes = removeStr(simpleTypes, "boolean")
	}

	// When simple types subsume all enum values, drop the enum values.
	if len(enumVals) > 0 && len(simpleTypes) > 0 {
		enumVals = filterSubsumedEnumVals(enumVals, simpleTypes)
	}

	schema := Schema{}
	if len(enumVals) > 0 && len(anyOf) == 0 && len(simpleTypes) == 0 {
		if allSameType(enumVals, "bool") && len(enumVals) == 2 {
			return Schema{"type": "boolean"}, true, nil
		}
		if len(enumVals) == 1 && !g.opts.ConstAsEnum {
			schema["const"] = enumVals[0]
		} else {
			if allSameType(enumVals, "string") {
				sort.Slice(enumVals, func(i, j int) bool {
					return enumVals[i].(string) < enumVals[j].(string)
				})
			}
			schema["enum"] = enumVals
		}
		if allSameType(enumVals, "string") {
			schema["type"] = "string"
		} else if allSameType(enumVals, "bool") {
			schema["type"] = "boolean"
		} else if allSameType(enumVals, "num") {
			schema["type"] = g.numberType()
		}
		return schema, true, nil
	}
	if len(enumVals) > 0 && (len(simpleTypes) > 0 || len(anyOf) > 0) {
		// Preserve type information for enum/const schemas so that
		// e.g. "true | null" produces {"const":true,"type":"boolean"}
		// rather than a bare {"enum":[true]}.
		if len(enumVals) == 1 && !g.opts.ConstAsEnum {
			enumSchema := Schema{"const": enumVals[0]}
			if allSameType(enumVals, "string") {
				enumSchema["type"] = "string"
			} else if allSameType(enumVals, "bool") {
				enumSchema["type"] = "boolean"
			} else if allSameType(enumVals, "num") {
				enumSchema["type"] = g.numberType()
			}
			anyOf = append([]any{enumSchema}, anyOf...)
		} else {
			enumSchema := Schema{"enum": enumVals}
			anyOf = append([]any{enumSchema}, anyOf...)
		}
	}
	if len(simpleTypes) > 0 {
		simpleTypes = uniqueStringsStable(simpleTypes)
		sort.Strings(simpleTypes)
		if len(anyOf) == 0 {
			if len(simpleTypes) == 1 {
				return Schema{"type": simpleTypes[0]}, true, nil
			}
			types := make([]any, 0, len(simpleTypes))
			for _, t := range simpleTypes {
				types = append(types, t)
			}
			return Schema{"type": types}, true, nil
		}
		if len(simpleTypes) == 1 {
			schema["type"] = simpleTypes[0]
		} else {
			types := make([]any, 0, len(simpleTypes))
			for _, t := range simpleTypes {
				types = append(types, t)
			}
			schema["type"] = types
		}
	}
	if len(anyOf) == 1 && len(schema) == 0 {
		if s, ok := anyOf[0].(Schema); ok {
			return s, true, nil
		}
	}
	if len(anyOf) > 0 {
		combined := make([]any, 0, len(anyOf))
		combined = append(combined, anyOf...)
		if len(schema) == 0 {
			return Schema{"anyOf": combined}, true, nil
		}
		combined = append(combined, schema)
		return Schema{"anyOf": combined}, true, nil
	}
	return schema, true, nil
}

func (g *Generator) schemaFromTypeNode(node *ast.Node) (Schema, bool, error) {
	if node == nil {
		return nil, false, nil
	}
	switch node.Kind {
	case ast.KindAnyKeyword, ast.KindUnknownKeyword:
		return Schema{}, true, nil
	case ast.KindParenthesizedType:
		return g.schemaFromTypeNode(node.AsParenthesizedTypeNode().Type)
	case ast.KindUnionType:
		return g.schemaFromUnionTypeNode(node)
	case ast.KindIntersectionType:
		return g.schemaFromIntersectionTypeNode(node)
	case ast.KindArrayType:
		elem, ok, err := g.schemaFromTypeNode(node.AsArrayTypeNode().ElementType)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			return nil, ok, err
		}
		if elem == nil {
			elem = Schema{}
		}
		return Schema{"type": "array", "items": elem}, true, nil
	case ast.KindTypeReference:
		if isBuiltinDateTypeNode(g.checker, node) {
			return Schema{"type": "string", "format": "date-time"}, true, nil
		}
		name := entityNameText(node.AsTypeReferenceNode().TypeName)
		if (name == "Array" || name == "ReadonlyArray") && len(node.TypeArguments()) == 1 {
			elem, ok, err := g.schemaFromTypeNode(node.TypeArguments()[0])
			if err != nil {
				return nil, false, err
			}
			if !ok {
				return nil, ok, err
			}
			if elem == nil {
				elem = Schema{}
			}
			return Schema{"type": "array", "items": elem}, true, nil
		}
		if schema, ok, err := g.utilityTypeSchema(node); err != nil {
			return nil, false, err
		} else if ok {
			return schema, true, nil
		}
		if len(node.TypeArguments()) > 0 {
			if typ := g.checker.GetTypeAtLocation(node); typ != nil {
				if sym := symbolForTypeNode(g.checker, node); sym != nil {
					if !isLibSymbol(sym) && !isUtilityTypeName(name) && g.shouldRefSymbol(sym) {
						// Resolve through type alias bodies so that e.g.
						// SomeAlias<"alias"> becomes SomeGeneric<"alias","alias">.
						defName := resolveAliasRefName(g.checker, node)
						if defName == "" {
							defName = typeNodeString(node)
						}
						schema, err := g.instantiatedRefSchema(defName, typ, node)
						return schema, err == nil, err
					}
				}
				schema, err := g.concreteTypeSchema(typ, node)
				if err != nil {
					return nil, false, err
				}
				if len(schema) > 0 {
					return schema, true, nil
				}
			}
		}
		if sym := symbolForTypeNode(g.checker, node); sym != nil {
			// Recognise the well-known "type integer = number" alias used by
			// typescript-json-schema to produce JSON Schema "integer".
			if g.isIntegerAlias(sym) {
				return Schema{"type": "integer"}, true, nil
			}
			// When the type alias should be emitted as a $ref (e.g. Ref is
			// on), produce the definition now before any inlining path runs.
			if g.shouldRefSymbol(sym) {
				schema, err := g.typeSchema(g.declaredTypeForSymbol(sym), sym, canonicalDeclaration(sym), false)
				return schema, err == nil, err
			}
			if sym.Flags&ast.SymbolFlagsTypeAlias != 0 && !g.opts.AliasRef {
				if aliasNode := declaredTypeNode(canonicalDeclaration(sym)); aliasNode != nil && aliasNode.Kind == ast.KindTypeReference && isUtilityTypeName(entityNameText(aliasNode.AsTypeReferenceNode().TypeName)) {
					return g.schemaFromTypeNode(aliasNode)
				}
			}
			if sym.Flags&ast.SymbolFlagsTypeAlias != 0 && !g.shouldRefSymbol(sym) {
				name := g.outputNameForSymbol(sym)
				// If the alias is already being processed (recursive alias),
				// create a $ref definition instead of inlining to prevent
				// infinite recursion.
				if g.inProgress[name] {
					return Schema{"$ref": g.refURI(name)}, true, nil
				}
				aliasDecl := canonicalDeclaration(sym)
				if aliasNode := declaredTypeNode(aliasDecl); aliasNode != nil && aliasNode != node {
					g.inProgress[name] = true
					schema, ok, err := g.schemaFromTypeNode(aliasNode)
					delete(g.inProgress, name)
					if err != nil {
						return nil, false, err
					}
					if ok {
						// Inherit JSDoc annotations (e.g. @minItems, @maxItems)
						// from the type alias declaration.
						if schema != nil {
							if docs := g.parseDocs(aliasDecl); docs != nil {
								g.applyDocs(schema, docs)
							}
						}
						return schema, true, nil
					}
				}
			}
		}
	case ast.KindTypeQuery:
		if node.AsTypeQueryNode().ExprName != nil && entityNameText(node.AsTypeQueryNode().ExprName) == "globalThis" {
			return Schema{"type": "object"}, true, nil
		}
	case ast.KindTypeLiteral:
		return g.schemaFromObjectLiteralTypeNode(node)
	case ast.KindMappedType:
		if typ := g.checker.GetTypeAtLocation(node); typ != nil {
			schema, err := g.concreteTypeSchema(typ, node)
			return schema, err == nil, err
		}
	case ast.KindTupleType:
		return g.schemaFromTupleNode(node)
	}
	typ := g.checker.GetTypeFromTypeNode(node)
	if typ == nil {
		return nil, false, nil
	}
	return g.schemaFromTypeString(g.typeString(typ), node)
}

func (g *Generator) schemaFromUnionTypeNode(node *ast.Node) (Schema, bool, error) {
	schema := Schema{}
	var simpleTypes []string
	var anyOf []any
	var enumVals []any
	for _, memberNode := range node.AsUnionTypeNode().Types.Nodes {
		if memberNode == nil {
			continue
		}
		// Without strictNullChecks, strip null from unions.
		if !g.strictNullChecks && memberNode.Kind == ast.KindNullKeyword {
			continue
		}
		sub, ok, err := g.schemaFromTypeNode(memberNode)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			return nil, false, nil
		}
		if sub == nil {
			sub = Schema{}
		}
		if t, ok := sub["type"].(string); ok && len(sub) == 1 {
			simpleTypes = append(simpleTypes, t)
			continue
		}
		if c, ok := sub["const"]; ok && len(sub) == 2 {
			enumVals = append(enumVals, c)
			continue
		}
		anyOf = append(anyOf, sub)
	}
	if ref := recursiveUnionRef(anyOf, g.inProgress); ref != nil {
		return ref, true, nil
	}

	// When boolean appears alongside other literal values, decompose it.
	if len(enumVals) > 0 && containsStr(simpleTypes, "boolean") {
		enumVals = append(enumVals, true, false)
		simpleTypes = removeStr(simpleTypes, "boolean")
	}

	// When simple types subsume all enum values, drop the enum values.
	if len(enumVals) > 0 && len(simpleTypes) > 0 {
		enumVals = filterSubsumedEnumVals(enumVals, simpleTypes)
	}

	if len(enumVals) > 0 && len(anyOf) == 0 && len(simpleTypes) == 0 {
		if allSameType(enumVals, "bool") && len(enumVals) == 2 {
			return Schema{"type": "boolean"}, true, nil
		}
		schema := Schema{}
		switch {
		case allSameType(enumVals, "string"):
			schema["type"] = "string"
		case allSameType(enumVals, "bool"):
			schema["type"] = "boolean"
		case allSameType(enumVals, "num"):
			schema["type"] = g.numberType()
		}
		if len(enumVals) == 1 && !g.opts.ConstAsEnum {
			schema["const"] = enumVals[0]
			return schema, true, nil
		}
		if allSameType(enumVals, "string") {
			sort.Slice(enumVals, func(i, j int) bool {
				return enumVals[i].(string) < enumVals[j].(string)
			})
		}
		schema["enum"] = enumVals
		return schema, true, nil
	}
	// When enum values coexist with simple types or anyOf branches,
	// wrap them in an anyOf.
	if len(enumVals) > 0 && (len(simpleTypes) > 0 || len(anyOf) > 0) {
		enumSchema := Schema{"enum": enumVals}
		anyOf = append([]any{enumSchema}, anyOf...)
	}
	if len(simpleTypes) > 0 {
		simpleTypes = uniqueStringsStable(simpleTypes)
		sort.Strings(simpleTypes)
		if len(simpleTypes) == 1 {
			schema["type"] = simpleTypes[0]
		} else {
			types := make([]any, 0, len(simpleTypes))
			for _, t := range simpleTypes {
				types = append(types, t)
			}
			schema["type"] = types
		}
	}
	if len(anyOf) == 1 && len(schema) == 0 {
		if s, ok := anyOf[0].(Schema); ok {
			return s, true, nil
		}
	}
	if len(anyOf) > 0 {
		combined := make([]any, 0, len(anyOf))
		combined = append(combined, anyOf...)
		if len(schema) == 0 {
			return Schema{"anyOf": combined}, true, nil
		}
		combined = append(combined, schema)
		return Schema{"anyOf": combined}, true, nil
	}
	return schema, true, nil
}

func (g *Generator) schemaFromIntersectionTypeNode(node *ast.Node) (Schema, bool, error) {
	allOf := make([]any, 0, len(node.AsIntersectionTypeNode().Types.Nodes))
	properties := map[string]any{}
	var required []string
	mergeable := true
	for _, memberNode := range node.AsIntersectionTypeNode().Types.Nodes {
		if memberNode == nil {
			continue
		}
		var (
			sub Schema
			ok  bool
			err error
		)
		if typ := g.checker.GetTypeAtLocation(memberNode); typ != nil {
			sub, err = g.concreteTypeSchema(typ, memberNode)
			ok = len(sub) > 0
		}
		if !ok {
			sub, ok, err = g.schemaFromTypeNode(memberNode)
			if err != nil {
				return nil, false, err
			}
		}
		if !ok {
			return nil, false, nil
		}
		allOf = append(allOf, sub)
		subSchema := g.derefLocalSchema(sub)
		if !mergeable || subSchema == nil {
			mergeable = false
			continue
		}
		if t, ok := subSchema["type"].(string); !ok || t != "object" {
			mergeable = false
			continue
		}
		for k := range subSchema {
			if k != "type" && k != "properties" && k != "required" && k != "additionalProperties" {
				mergeable = false
				break
			}
		}
		if !mergeable {
			continue
		}
		if props, ok := subSchema["properties"].(map[string]any); ok {
			for k, v := range props {
				properties[k] = v
			}
		}
		required = append(required, asStrings(subSchema["required"])...)
	}
	if mergeable {
		schema := Schema{"type": "object", "properties": properties}
		if g.opts.NoExtraProps {
			schema["additionalProperties"] = false
		}
		if len(required) > 0 {
			req := uniqueStrings(required)
			schema["required"] = req
		}
		return schema, true, nil
	}
	return Schema{"allOf": allOf}, true, nil
}

func (g *Generator) schemaFromObjectLiteralTypeNode(node *ast.Node) (Schema, bool, error) {
	properties := map[string]any{}
	var required []string
	for _, member := range node.AsTypeLiteralNode().Members.Nodes {
		if member == nil || member.Kind != ast.KindPropertySignature {
			return nil, false, nil
		}
		name := member.Name().Text()

		// When a member has a @TJS-type annotation, the override replaces
		// the entire type resolution so the property is inlined rather
		// than emitted as a $ref to the original type definition.
		var sub Schema
		memberDocs := g.parseDocs(member)
		if memberDocs != nil && memberDocs.fields != nil {
			if _, hasTypeOverride := memberDocs.fields["type"]; hasTypeOverride {
				sub = Schema{}
				g.applyDocs(sub, memberDocs)
				memberDocs = nil // already applied
			}
		}
		if sub == nil {
			var ok bool
			var err error
			sub, ok, err = g.schemaFromTypeNode(member.Type())
			if err != nil {
				return nil, false, err
			}
			if !ok {
				return nil, false, nil
			}
			if sub == nil {
				sub = Schema{}
			}
		}
		// Apply JSDoc annotations from the property declaration.
		// The description always overwrites (property description takes
		// precedence over inlined type description), but other fields
		// use non-overwrite to avoid clobbering type-inferred values
		// (e.g. don't overwrite an enum array with a raw @enum value).
		if memberDocs != nil {
			if memberDocs.description != "" {
				sub["description"] = memberDocs.description
			}
			memberDocs.description = ""
			g.applyDocsNonOverwrite(sub, memberDocs)
		}
		properties[name] = sub
		if member.AsPropertySignatureDeclaration().PostfixToken == nil {
			required = append(required, name)
		}
	}
	schema := Schema{"type": "object", "properties": properties}
	if g.opts.NoExtraProps && len(properties) > 0 {
		schema["additionalProperties"] = false
	}
	if g.opts.Required && len(required) > 0 {
		schema["required"] = required
	}
	return schema, true, nil
}

func (g *Generator) schemaFromTupleNode(node *ast.Node) (Schema, bool, error) {
	schema := Schema{"type": "array"}
	items := make([]any, 0)
	minItems := 0

	for _, elem := range node.AsTupleTypeNode().Elements.Nodes {
		if elem == nil {
			continue
		}
		switch elem.Kind {
		case ast.KindNamedTupleMember:
			ntm := elem.AsNamedTupleMember()
			memberName := elem.Name().Text()
			memberType := elem.Type()
			isOptional := ntm.QuestionToken != nil
			// Check if this is a rest element (e.g. ...d: number[])
			if ntm.DotDotDotToken != nil {
				sub, ok, err := g.schemaForTupleRestElement(memberType, node)
				if err != nil || !ok {
					return nil, ok, err
				}
				sub["title"] = memberName
				schema["additionalItems"] = sub
				continue
			}
			sub, ok, err := g.schemaFromTypeNode(memberType)
			if err != nil || !ok {
				// Fallback to string-based approach
				sub, ok, err = g.schemaFromTypeString(typeNodeString(memberType), node)
				if err != nil || !ok {
					return nil, ok, err
				}
			}
			sub["title"] = memberName
			items = append(items, sub)
			if !isOptional {
				minItems++
			}
		case ast.KindRestType:
			restTypeNode := elem.Type()
			sub, ok, err := g.schemaForTupleRestElement(restTypeNode, node)
			if err != nil || !ok {
				return nil, ok, err
			}
			schema["additionalItems"] = sub
		default:
			isOptional := false
			elemNode := elem
			if elem.Kind == ast.KindOptionalType {
				isOptional = true
				elemNode = elem.Type()
			}
			sub, ok, err := g.schemaFromTypeNode(elemNode)
			if err != nil || !ok {
				sub, ok, err = g.schemaFromTypeString(typeNodeString(elemNode), node)
				if err != nil || !ok {
					return nil, ok, err
				}
			}
			items = append(items, sub)
			if !isOptional {
				minItems++
			}
		}
	}

	if len(items) > 0 {
		schema["items"] = items
	}
	schema["minItems"] = minItems
	if _, ok := schema["additionalItems"]; !ok {
		if g.opts.AliasRef && minItems == len(items) && len(items) > 0 {
			// When AliasRef is on and all items are required, emit
			// additionalItems as a union of all item types.  This
			// matches the original ts-json-schema-generator behavior
			// for tuple definitions.
			anyOf := make([]any, len(items))
			copy(anyOf, items)
			schema["additionalItems"] = Schema{"anyOf": anyOf}
		} else {
			schema["maxItems"] = len(items)
		}
	}
	return schema, true, nil
}

// schemaForTupleRestElement generates a schema for the element type of a
// rest element in a tuple. For example, for ...number[], it returns the
// schema for number (not the array schema).
func (g *Generator) schemaForTupleRestElement(restTypeNode *ast.Node, context *ast.Node) (Schema, bool, error) {
	if restTypeNode == nil {
		return nil, false, nil
	}
	// If the rest type is an ArrayType node (number[]), use the element type directly.
	if restTypeNode.Kind == ast.KindArrayType {
		elemTypeNode := restTypeNode.AsArrayTypeNode().ElementType
		sub, ok, err := g.schemaFromTypeNode(elemTypeNode)
		if err != nil || !ok {
			sub, ok, err = g.schemaFromTypeString(typeNodeString(elemTypeNode), context)
		}
		return sub, ok, err
	}
	// If it's a TypeReference to Array<T>, extract the type argument.
	if restTypeNode.Kind == ast.KindTypeReference {
		name := entityNameText(restTypeNode.AsTypeReferenceNode().TypeName)
		if name == "Array" || name == "ReadonlyArray" {
			args := restTypeNode.AsTypeReferenceNode().TypeArguments
			if args != nil && len(args.Nodes) == 1 {
				sub, ok, err := g.schemaFromTypeNode(args.Nodes[0])
				if err != nil || !ok {
					sub, ok, err = g.schemaFromTypeString(typeNodeString(args.Nodes[0]), context)
				}
				return sub, ok, err
			}
		}
	}
	// Fallback: generate from the string representation and unwrap if needed.
	typeStr := typeNodeString(restTypeNode)
	if strings.HasSuffix(typeStr, "[]") {
		typeStr = strings.TrimSuffix(typeStr, "[]")
	}
	return g.schemaFromTypeString(typeStr, context)
}

func (g *Generator) schemaFromTupleString(inner string, node *ast.Node) (Schema, bool, error) {
	parts := splitTopLevel(inner, ',')
	schema := Schema{"type": "array"}
	items := make([]any, 0, len(parts))
	minItems := 0
	for _, raw := range parts {
		part := strings.TrimSpace(raw)
		if part == "" {
			continue
		}
		if idx := indexTopLevel(part, ':'); idx >= 0 {
			part = strings.TrimSpace(part[idx+1:])
		}
		if strings.HasPrefix(part, "...") {
			restType := strings.TrimSpace(part[3:])
			// Unwrap array types: the rest element is an array (e.g. number[])
			// but additionalItems needs the element type (e.g. number).
			if strings.HasSuffix(restType, "[]") {
				restType = strings.TrimSuffix(restType, "[]")
			} else if inner, ok := extractGenericArgs(restType, "Array<"); ok {
				restType = inner
			}
			sub, ok, err := g.schemaFromTypeString(restType, node)
			if err != nil || !ok {
				return nil, ok, err
			}
			schema["additionalItems"] = sub
			continue
		}
		optional := strings.HasSuffix(part, "?")
		part = strings.TrimSuffix(part, "?")
		sub, ok, err := g.schemaFromTypeString(part, node)
		if err != nil || !ok {
			return nil, ok, err
		}
		items = append(items, sub)
		if !optional {
			minItems++
		}
	}
	if len(items) > 0 {
		schema["items"] = items
	}
	schema["minItems"] = minItems
	if _, ok := schema["additionalItems"]; !ok {
		schema["maxItems"] = len(items)
	}
	return schema, true, nil
}

func (g *Generator) schemaFromObjectLiteralString(inner string, node *ast.Node) (Schema, bool, error) {
	schema := Schema{"type": "object"}
	properties := map[string]any{}
	var required []string
	for _, raw := range splitObjectMembers(inner) {
		part := strings.TrimSpace(raw)
		if part == "" {
			continue
		}
		if strings.HasPrefix(part, "[") {
			return nil, false, nil
		}
		if strings.HasPrefix(part, "readonly ") {
			part = strings.TrimSpace(strings.TrimPrefix(part, "readonly "))
		}
		idx := indexTopLevel(part, ':')
		if idx < 0 {
			return nil, false, nil
		}
		name := strings.TrimSpace(part[:idx])
		name = strings.Trim(name, `"'`)
		optional := strings.HasSuffix(name, "?")
		name = strings.TrimSuffix(name, "?")
		sub, ok, err := g.schemaFromTypeString(strings.TrimSpace(part[idx+1:]), node)
		if err != nil || !ok {
			return nil, ok, err
		}
		properties[name] = sub
		if !optional {
			required = append(required, name)
		}
	}
	schema["properties"] = properties
	if g.opts.NoExtraProps {
		schema["additionalProperties"] = false
	}
	if g.opts.Required && len(required) > 0 {
		schema["required"] = required
	}
	return schema, true, nil
}

func (g *Generator) refSymbolForType(t *checker.Type, decl *ast.Node) *ast.Symbol {
	if sym := namedRefSymbol(g.checker, t); sym != nil {
		return sym
	}
	typeNode := declaredTypeNode(decl)
	if typeNode == nil || typeNode.Kind != ast.KindTypeReference {
		return nil
	}
	sym := symbolForTypeNode(g.checker, typeNode)
	if sym == nil || isBuiltinDateSymbol(sym) || isLibSymbol(sym) || !g.shouldRefSymbol(sym) {
		return nil
	}
	return sym
}

func (g *Generator) refSchemaForSymbol(sym *ast.Symbol) (Schema, error) {
	if sym == nil {
		return Schema{}, nil
	}
	name := g.outputNameForSymbol(sym)
	if g.inProgress[name] {
		return Schema{"$ref": g.refURI(name)}, nil
	}
	if _, ok := g.definitions[name]; !ok {
		g.inProgress[name] = true
		defNode := canonicalDeclaration(sym)
		def, err := g.emitType(g.declaredTypeForSymbol(sym), sym, defNode)
		delete(g.inProgress, name)
		if err != nil {
			return nil, err
		}
		if def == nil {
			def = Schema{}
		}
		g.definitions[name] = def
	}
	return Schema{"$ref": g.refURI(name)}, nil
}

func matchUnionMemberNode(ch *checker.Checker, mt *checker.Type, memberNodes []*ast.Node, used []bool) *ast.Node {
	for i, memberNode := range memberNodes {
		if i >= len(used) || used[i] || memberNode == nil {
			continue
		}
		if unionMemberMatches(ch, mt, memberNode) {
			used[i] = true
			return memberNode
		}
	}
	return nil
}

func unionMemberMatches(ch *checker.Checker, mt *checker.Type, node *ast.Node) bool {
	if ch == nil || mt == nil || node == nil {
		return false
	}
	if msym := namedRefSymbol(ch, mt); msym != nil {
		if nsym := symbolForTypeNode(ch, declaredTypeNode(node)); nsym != nil {
			return qualifiedSymbolName(msym) == qualifiedSymbolName(nsym)
		}
	}
	nodeType := ch.GetTypeFromTypeNode(node)
	if nodeType == nil {
		return false
	}
	return ch.TypeToStringEx(mt, nil, checker.TypeFormatFlagsUseStructuralFallback|checker.TypeFormatFlagsNoTruncation|checker.TypeFormatFlagsUseSingleQuotesForStringLiteralType) ==
		ch.TypeToStringEx(nodeType, nil, checker.TypeFormatFlagsUseStructuralFallback|checker.TypeFormatFlagsNoTruncation|checker.TypeFormatFlagsUseSingleQuotesForStringLiteralType)
}

func extendsTargetSymbol(ch *checker.Checker, node *ast.Node) *ast.Symbol {
	if ch == nil || node == nil {
		return nil
	}
	types := ast.GetExtendsHeritageClauseElements(node)
	if len(types) != 1 || types[0] == nil || types[0].Expression() == nil {
		return nil
	}
	sym := ch.GetSymbolAtLocation(types[0].Expression())
	if sym != nil && sym.Flags&ast.SymbolFlagsAlias != 0 {
		sym = ch.GetAliasedSymbol(sym)
	}
	return sym
}

func hasNoOwnMembers(node *ast.Node) bool {
	if node == nil {
		return false
	}
	switch node.Kind {
	case ast.KindInterfaceDeclaration:
		return len(node.AsInterfaceDeclaration().Members.Nodes) == 0
	case ast.KindClassDeclaration:
		return len(node.AsClassDeclaration().Members.Nodes) == 0
	default:
		return false
	}
}

func splitObjectMembers(s string) []string {
	var out []string
	var current strings.Builder
	depthParen, depthBracket, depthBrace, depthAngle := 0, 0, 0, 0
	var quote rune
	for _, r := range s {
		if quote != 0 {
			current.WriteRune(r)
			if r == quote {
				quote = 0
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case '(':
			depthParen++
		case ')':
			depthParen--
		case '[':
			depthBracket++
		case ']':
			depthBracket--
		case '{':
			depthBrace++
		case '}':
			depthBrace--
		case '<':
			depthAngle++
		case '>':
			depthAngle--
		case ';', ',':
			if depthParen == 0 && depthBracket == 0 && depthBrace == 0 && depthAngle == 0 {
				out = append(out, current.String())
				current.Reset()
				continue
			}
		}
		current.WriteRune(r)
	}
	if strings.TrimSpace(current.String()) != "" {
		out = append(out, current.String())
	}
	return out
}

func declaredTypeNode(node *ast.Node) *ast.Node {
	if node == nil {
		return nil
	}
	if typ := node.Type(); typ != nil {
		return typ
	}
	if ast.IsTypeNode(node) {
		return node
	}
	return nil
}

func symbolForTypeNode(ch *checker.Checker, node *ast.Node) *ast.Symbol {
	if ch == nil || node == nil {
		return nil
	}
	switch node.Kind {
	case ast.KindParenthesizedType:
		return symbolForTypeNode(ch, node.AsParenthesizedTypeNode().Type)
	case ast.KindTypeReference:
		sym := ch.GetSymbolAtLocation(node.AsTypeReferenceNode().TypeName)
		if sym != nil && sym.Flags&ast.SymbolFlagsAlias != 0 {
			sym = ch.GetAliasedSymbol(sym)
		}
		return sym
	}
	return nil
}

func qualifiedSymbolName(sym *ast.Symbol) string {
	if sym == nil {
		return ""
	}
	name := sym.Name
	if name == "" || strings.HasPrefix(name, ast.InternalSymbolNamePrefix) {
		return name
	}
	if sym.Parent == nil || sym.Parent.Name == "" || sym.Parent.IsExternalModule() {
		return name
	}
	parent := qualifiedSymbolName(sym.Parent)
	if parent == "" {
		return name
	}
	return parent + "." + name
}

// isModuleQualifiedSymbol returns true if the symbol lives inside a
// module or namespace (i.e. its qualified name contains a dot).
func isModuleQualifiedSymbol(sym *ast.Symbol) bool {
	if sym == nil {
		return false
	}
	return sym.Parent != nil && sym.Parent.Name != "" && !sym.Parent.IsExternalModule() && sym.Parent.Flags&ast.SymbolFlagsModule != 0
}

func isExportedSymbol(sym *ast.Symbol) bool {
	for _, decl := range sym.Declarations {
		if decl == nil {
			continue
		}
		if hasModifier(decl, ast.KindExportKeyword) {
			return true
		}
	}
	return false
}

func isBuiltinDateType(ch *checker.Checker, t *checker.Type, node *ast.Node) bool {
	if ch == nil {
		return false
	}
	if t == nil && node != nil {
		t = ch.GetTypeFromTypeNode(node)
	}
	if t == nil {
		return false
	}
	if ch.TypeToStringEx(t, nil, checker.TypeFormatFlagsUseStructuralFallback|checker.TypeFormatFlagsNoTruncation) != "Date" {
		return false
	}
	sym := t.Symbol()
	if sym == nil && node != nil {
		sym = symbolForTypeNode(ch, node)
	}
	return isLibSymbol(sym)
}

func isBuiltinDateTypeNode(ch *checker.Checker, node *ast.Node) bool {
	if node == nil {
		return false
	}
	if node.Kind == ast.KindParenthesizedType {
		return isBuiltinDateTypeNode(ch, node.AsParenthesizedTypeNode().Type)
	}
	if node.Kind == ast.KindTypeReference {
		name := entityNameText(node.AsTypeReferenceNode().TypeName)
		sym := symbolForTypeNode(ch, node)
		if isBuiltinDateSymbol(sym) || isBuiltInDateAlias(ch, sym) {
			return true
		}
		if name == "Date" && sym == nil {
			return true
		}
	}
	if isBuiltinDateType(ch, nil, node) {
		return true
	}
	sym := symbolForTypeNode(ch, node)
	return isBuiltInDateAlias(ch, sym)
}

func isBuiltInDateAlias(ch *checker.Checker, sym *ast.Symbol) bool {
	if ch == nil || sym == nil || sym.Flags&ast.SymbolFlagsTypeAlias == 0 {
		return false
	}
	node := declaredTypeNode(canonicalDeclaration(sym))
	if node == nil {
		return false
	}
	if node.Kind == ast.KindTypeReference && entityNameText(node.AsTypeReferenceNode().TypeName) == "Date" {
		target := symbolForTypeNode(ch, node)
		return target == nil || isBuiltinDateSymbol(target)
	}
	return false
}

func isLibSymbol(sym *ast.Symbol) bool {
	if sym == nil {
		return false
	}
	for _, decl := range sym.Declarations {
		if decl == nil {
			continue
		}
		if file := sourceFileForNode(decl); file != nil && file.IsDeclarationFile {
			return true
		}
	}
	if sym.ValueDeclaration != nil {
		if file := sourceFileForNode(sym.ValueDeclaration); file != nil && file.IsDeclarationFile {
			return true
		}
	}
	return false
}

func isBuiltinDateSymbol(sym *ast.Symbol) bool {
	return sym != nil && sym.Name == "Date" && (isLibSymbol(sym) || (sym.Parent == nil && len(sym.Declarations) == 0))
}

func shouldPreferTypeNodeSchema(ch *checker.Checker, t *checker.Type, sym *ast.Symbol) bool {
	if t == nil {
		return true
	}
	if t.Flags()&(checker.TypeFlagsAny|checker.TypeFlagsUnknown) != 0 {
		return true
	}
	return sym != nil && sym.Flags&ast.SymbolFlagsTypeAlias != 0 &&
		(t.Flags()&(checker.TypeFlagsUnion|checker.TypeFlagsIntersection) != 0 || (ch != nil && len(ch.GetPropertiesOfType(t)) == 0))
}

func recursiveUnionRef(items []any, inProgress map[string]bool) Schema {
	for _, item := range items {
		schema, ok := item.(Schema)
		if !ok || len(schema) != 1 {
			continue
		}
		ref, ok := schema["$ref"].(string)
		if !ok || !strings.HasPrefix(ref, "#/definitions/") {
			continue
		}
		name := strings.TrimPrefix(ref, "#/definitions/")
		if inProgress[name] {
			return schema
		}
	}
	return nil
}

// isSimpleReferenceAlias returns true if the type alias body is a simple
// reference to another named type (e.g. `type MyAlias = MyObject`).  Such
// aliases are transparent when AliasRef is off.
func isSimpleReferenceAlias(sym *ast.Symbol) bool {
	if sym == nil || sym.Flags&ast.SymbolFlagsTypeAlias == 0 {
		return false
	}
	decl := canonicalDeclaration(sym)
	node := declaredTypeNode(decl)
	if node == nil {
		return false
	}
	// Generic type aliases (with type parameters) are not simple references.
	if decl != nil && decl.Kind == ast.KindTypeAliasDeclaration && len(decl.TypeParameters()) > 0 {
		return false
	}
	return node.Kind == ast.KindTypeReference
}

func isPrimitiveAliasSymbol(sym *ast.Symbol) bool {
	node := declaredTypeNode(canonicalDeclaration(sym))
	if node == nil {
		return false
	}
	switch node.Kind {
	case ast.KindStringKeyword, ast.KindNumberKeyword, ast.KindBooleanKeyword, ast.KindNullKeyword, ast.KindAnyKeyword, ast.KindUnknownKeyword, ast.KindTrueKeyword, ast.KindFalseKeyword, ast.KindLiteralType:
		return true
	}
	return false
}

// isObjectLiteralAlias returns true when the type alias body is an inline
// object literal (e.g. `type Foo = { bar: string }`).  Such aliases are
// inlined rather than emitted as $ref definitions when AliasRef is off.
func isObjectLiteralAlias(sym *ast.Symbol) bool {
	if sym == nil || sym.Flags&ast.SymbolFlagsTypeAlias == 0 {
		return false
	}
	node := declaredTypeNode(canonicalDeclaration(sym))
	if node == nil {
		return false
	}
	return node.Kind == ast.KindTypeLiteral
}

func hasModifier(node *ast.Node, kind ast.Kind) bool {
	if node == nil || node.Modifiers() == nil {
		return false
	}
	for _, mod := range node.Modifiers().Nodes {
		if mod != nil && mod.Kind == kind {
			return true
		}
	}
	return false
}

func looksLikeArrayTypeString(text string) bool {
	text = trimOuterParens(strings.TrimSpace(text))
	return strings.HasPrefix(text, "Array<") ||
		strings.HasPrefix(text, "ReadonlyArray<") ||
		strings.HasSuffix(text, "[]") ||
		(strings.HasPrefix(text, "[") && strings.HasSuffix(text, "]"))
}

func isArraySchema(schema Schema) bool {
	if schema == nil {
		return false
	}
	t, ok := schema["type"].(string)
	return ok && t == "array"
}

func shouldExpandArrayAnnotation(ch *checker.Checker, g *Generator, node *ast.Node) bool {
	if node == nil {
		return false
	}
	if isArrayTypeNode(node) {
		return true
	}
	if sym := symbolForTypeNode(ch, node); sym != nil && sym.Flags&ast.SymbolFlagsTypeAlias != 0 && !g.shouldRefSymbol(sym) {
		return isArrayAliasSymbol(sym)
	}
	return false
}

func isArrayAliasSymbol(sym *ast.Symbol) bool {
	node := declaredTypeNode(canonicalDeclaration(sym))
	if node == nil {
		return false
	}
	switch node.Kind {
	case ast.KindArrayType, ast.KindTupleType:
		return true
	case ast.KindParenthesizedType:
		return isArrayTypeNode(node.AsParenthesizedTypeNode().Type)
	default:
		return isArrayTypeNode(node)
	}
}

func isArrayTypeNode(node *ast.Node) bool {
	if node == nil {
		return false
	}
	switch node.Kind {
	case ast.KindArrayType, ast.KindTupleType:
		return true
	case ast.KindParenthesizedType:
		return isArrayTypeNode(node.AsParenthesizedTypeNode().Type)
	case ast.KindTypeReference:
		name := entityNameText(node.AsTypeReferenceNode().TypeName)
		return name == "Array" || name == "ReadonlyArray"
	default:
		return false
	}
}

func entityNameText(node *ast.Node) string {
	if node == nil {
		return ""
	}
	switch node.Kind {
	case ast.KindIdentifier:
		return node.Text()
	case ast.KindQualifiedName:
		left := entityNameText(node.AsQualifiedName().Left)
		right := node.AsQualifiedName().Right.Text()
		if left == "" {
			return right
		}
		return left + "." + right
	case ast.KindPropertyAccessExpression:
		left := entityNameText(node.Expression())
		right := node.Name().Text()
		if left == "" {
			return right
		}
		return left + "." + right
	default:
		return ""
	}
}

func typeNodeString(node *ast.Node) string {
	if node == nil {
		return ""
	}
	switch node.Kind {
	case ast.KindParenthesizedType:
		return "(" + typeNodeString(node.AsParenthesizedTypeNode().Type) + ")"
	case ast.KindArrayType:
		return typeNodeString(node.AsArrayTypeNode().ElementType) + "[]"
	case ast.KindTypeReference:
		name := entityNameText(node.AsTypeReferenceNode().TypeName)
		if len(node.TypeArguments()) == 0 {
			return name
		}
		args := make([]string, 0, len(node.TypeArguments()))
		for _, arg := range node.TypeArguments() {
			args = append(args, typeNodeString(arg))
		}
		return name + "<" + strings.Join(args, ",") + ">"
	case ast.KindUnionType:
		parts := make([]string, 0, len(node.AsUnionTypeNode().Types.Nodes))
		for _, part := range node.AsUnionTypeNode().Types.Nodes {
			parts = append(parts, typeNodeString(part))
		}
		return strings.Join(parts, " | ")
	case ast.KindIntersectionType:
		parts := make([]string, 0, len(node.AsIntersectionTypeNode().Types.Nodes))
		for _, part := range node.AsIntersectionTypeNode().Types.Nodes {
			parts = append(parts, typeNodeString(part))
		}
		return strings.Join(parts, " & ")
	case ast.KindLiteralType:
		lit := node.AsLiteralTypeNode().Literal
		// String literals need to be quoted in type names.
		if lit.Kind == ast.KindStringLiteral {
			return "\"" + lit.Text() + "\""
		}
		return lit.Text()
	case ast.KindTypeLiteral:
		var members []string
		for _, member := range node.AsTypeLiteralNode().Members.Nodes {
			if member == nil {
				continue
			}
			members = append(members, typeElementString(member))
		}
		return "{ " + strings.Join(members, "; ") + " }"
	case ast.KindTupleType:
		var elems []string
		for _, elem := range node.AsTupleTypeNode().Elements.Nodes {
			elems = append(elems, typeNodeString(elem))
		}
		return "[" + strings.Join(elems, ", ") + "]"
	case ast.KindNamedTupleMember:
		name := node.Name().Text()
		if node.AsNamedTupleMember().QuestionToken != nil {
			return name + "?: " + typeNodeString(node.Type())
		}
		return name + ": " + typeNodeString(node.Type())
	case ast.KindRestType:
		return "..." + typeNodeString(node.Type())
	case ast.KindOptionalType:
		return typeNodeString(node.AsOptionalTypeNode().Type) + "?"
	case ast.KindStringKeyword:
		return "string"
	case ast.KindNumberKeyword:
		return "number"
	case ast.KindBooleanKeyword:
		return "boolean"
	case ast.KindNullKeyword:
		return "null"
	case ast.KindAnyKeyword:
		return "any"
	case ast.KindUnknownKeyword:
		return "unknown"
	case ast.KindVoidKeyword:
		return "void"
	case ast.KindTrueKeyword:
		return "true"
	case ast.KindFalseKeyword:
		return "false"
	default:
		if typ := node.Type(); typ != nil && typ != node {
			return typeNodeString(typ)
		}
		if typ := node.Symbol(); typ != nil {
			return typ.Name
		}
		return ""
	}
}

func typeElementString(node *ast.Node) string {
	if node == nil {
		return ""
	}
	switch node.Kind {
	case ast.KindPropertySignature:
		name := node.Name().Text()
		if node.AsPropertySignatureDeclaration().PostfixToken != nil {
			name += "?"
		}
		return name + ": " + typeNodeString(node.Type())
	default:
		return ""
	}
}

func splitTopLevel(s string, sep rune) []string {
	var out []string
	var current strings.Builder
	depthParen, depthBracket, depthBrace, depthAngle := 0, 0, 0, 0
	var quote rune
	for _, r := range s {
		if quote != 0 {
			current.WriteRune(r)
			if r == quote {
				quote = 0
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case '(':
			depthParen++
		case ')':
			depthParen--
		case '[':
			depthBracket++
		case ']':
			depthBracket--
		case '{':
			depthBrace++
		case '}':
			depthBrace--
		case '<':
			depthAngle++
		case '>':
			depthAngle--
		default:
			if r == sep && depthParen == 0 && depthBracket == 0 && depthBrace == 0 && depthAngle == 0 {
				out = append(out, strings.TrimSpace(current.String()))
				current.Reset()
				continue
			}
		}
		current.WriteRune(r)
	}
	if strings.TrimSpace(current.String()) != "" {
		out = append(out, strings.TrimSpace(current.String()))
	}
	return out
}

func trimOuterParens(s string) string {
	for strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		inner := s[1 : len(s)-1]
		if len(splitTopLevel(inner, ')')) != 1 {
			return s
		}
		s = strings.TrimSpace(inner)
	}
	return s
}

// extractGenericArgs checks whether text starts with the given prefix (e.g.
// "Array<") and ends with the matching closing '>'. It returns the content
// between the brackets and true, or ("", false) when the pattern does not
// match or the brackets are unbalanced. Unlike a naive HasSuffix(text, ">"),
// this correctly handles nested generics such as "Array<Map<string, number>>".
func extractGenericArgs(text, prefix string) (string, bool) {
	if !strings.HasPrefix(text, prefix) {
		return "", false
	}
	// prefix must end with '<'; start scanning after it.
	inner := text[len(prefix):]
	depth := 1
	var quote rune
	for i, r := range inner {
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case '<':
			depth++
		case '>':
			depth--
			if depth == 0 {
				// The matching '>' must be the very last character.
				if i == len(inner)-1 {
					return inner[:i], true
				}
				return "", false
			}
		}
	}
	return "", false
}

// splitQualifiedName splits a qualified name like "A.B<C.D>.E" on '.'
// delimiters that are NOT inside angle brackets, so that generic type
// parameters are kept intact.
func splitQualifiedName(name string) []string {
	var parts []string
	depth := 0
	var quote rune
	start := 0
	for i, r := range name {
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case '<':
			depth++
		case '>':
			depth--
		case '.':
			if depth == 0 {
				parts = append(parts, name[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, name[start:])
	return parts
}

func indexTopLevel(s string, sep rune) int {
	depthParen, depthBracket, depthBrace, depthAngle := 0, 0, 0, 0
	var quote rune
	for i, r := range s {
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case '(':
			depthParen++
		case ')':
			depthParen--
		case '[':
			depthBracket++
		case ']':
			depthBracket--
		case '{':
			depthBrace++
		case '}':
			depthBrace--
		case '<':
			depthAngle++
		case '>':
			depthAngle--
		default:
			if r == sep && depthParen == 0 && depthBracket == 0 && depthBrace == 0 && depthAngle == 0 {
				return i
			}
		}
	}
	return -1
}
