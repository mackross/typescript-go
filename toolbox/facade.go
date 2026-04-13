package toolbox

// TSType is a facade that hides tsType internals behind a clean public API.
type TSType struct {
	inner *tsType
}

// --- Rendering ---

// ToJSONSchema converts the type to a JSON Schema map.
func (t *TSType) ToJSONSchema() map[string]any {
	if t == nil || t.inner == nil {
		return map[string]any{}
	}
	return tsTypeToJSON(t.inner)
}

// ToTS renders the type as TypeScript source text.
func (t *TSType) ToTS() string {
	if t == nil || t.inner == nil {
		return "any"
	}
	return tsTypeToTS(t.inner)
}

// Declarations emits type alias declarations for all definitions
// in the type (e.g. "type Foo = ...;\n"). Returns empty string when
// there are no definitions.
func (t *TSType) Declarations() string {
	if t == nil || t.inner == nil {
		return ""
	}
	return tsTypeDeclarationsToTS(t.inner)
}

// CannotJSON reports why this type cannot be faithfully supplied over a JSON
// transport. It returns nil when the type is JSON-safe.
func (t *TSType) CannotJSON() []string {
	if t == nil || t.inner == nil {
		return nil
	}
	reasons := cannotJSONType(t.inner, t.inner.Definitions, "", false, map[*tsType]bool{})
	if len(reasons) == 0 {
		return nil
	}
	return dedupeStrings(reasons)
}

// --- Inspection ---

// PropertyInfo describes a single property of an object TSType.
type PropertyInfo struct {
	Name        string
	Type        *TSType
	Description string
	Optional    bool
}

// Properties returns the properties of an object TSType with their
// names, types, descriptions, and optionality.
func (t *TSType) Properties() []PropertyInfo {
	if t == nil || t.inner == nil {
		return nil
	}
	requiredSet := make(map[string]bool, len(t.inner.Required))
	for _, r := range t.inner.Required {
		requiredSet[r] = true
	}
	out := make([]PropertyInfo, len(t.inner.Properties))
	for i, p := range t.inner.Properties {
		var desc string
		if p.Schema != nil && p.Schema.Annotations != nil {
			desc = p.Schema.Annotations.Description
		}
		optional := p.Optional
		if requiredSet[p.Name] {
			optional = false
		} else if len(requiredSet) > 0 {
			optional = true
		}
		out[i] = PropertyInfo{
			Name:        p.Name,
			Type:        &TSType{inner: p.Schema},
			Description: desc,
			Optional:    optional,
		}
	}
	return out
}

// ObjectProperties returns the properties of an object TSType, or nil if the
// receiver is nil, not an object, or has no properties. This is a convenience
// that combines the nil/IsObject/empty check with Properties().
func (t *TSType) ObjectProperties() []PropertyInfo {
	if t == nil || t.inner == nil || t.inner.Kind != tsTypeObject {
		return nil
	}
	props := t.Properties()
	if len(props) == 0 {
		return nil
	}
	return props
}

// PropertyNames returns the names of all properties on an object type.
// Returns nil for non-object types.
func (t *TSType) PropertyNames() []string {
	if t == nil || t.inner == nil || t.inner.Kind != tsTypeObject {
		return nil
	}
	names := make([]string, len(t.inner.Properties))
	for i, p := range t.inner.Properties {
		names[i] = p.Name
	}
	return names
}

// HasProperty reports whether the type has a property with the given name.
// Returns false for non-object types.
func (t *TSType) HasProperty(name string) bool {
	if t == nil || t.inner == nil || t.inner.Kind != tsTypeObject {
		return false
	}
	for _, p := range t.inner.Properties {
		if p.Name == name {
			return true
		}
	}
	return false
}

// Description returns the type's own description (from JSDoc on the type declaration).
func (t *TSType) Description() string {
	if t == nil || t.inner == nil || t.inner.Annotations == nil {
		return ""
	}
	return t.inner.Annotations.Description
}

// IsObject reports whether the underlying type is an object.
func (t *TSType) IsObject() bool {
	if t == nil || t.inner == nil {
		return false
	}
	return t.inner.Kind == tsTypeObject
}

// DefinitionTypes returns the $ref definition types as a map of name to TSType.
func (t *TSType) DefinitionTypes() map[string]*TSType {
	if t == nil || t.inner == nil || len(t.inner.Definitions) == 0 {
		return nil
	}
	out := make(map[string]*TSType, len(t.inner.Definitions))
	for name, def := range t.inner.Definitions {
		out[name] = &TSType{inner: def}
	}
	return out
}

// --- Promise unwrapping ---

// UnwrapPromise returns the inner type T if the receiver represents Promise<T>.
// If not a Promise, returns the receiver unchanged.
func (t *TSType) UnwrapPromise() *TSType {
	if t == nil || t.inner == nil {
		return t
	}
	if t.inner.PromiseInner != nil {
		return &TSType{inner: t.inner.PromiseInner}
	}
	return t
}

// --- Manipulation (returns new copies) ---

// RemoveProperties returns a new TSType with the named properties removed.
// The original is not modified.
func (t *TSType) RemoveProperties(names ...string) *TSType {
	if t == nil || t.inner == nil {
		return t
	}
	remove := make(map[string]bool, len(names))
	for _, n := range names {
		remove[n] = true
	}

	// Shallow copy the inner tsType.
	cp := *t.inner

	// Filter properties.
	filtered := make([]tsProperty, 0, len(cp.Properties))
	for _, p := range cp.Properties {
		if !remove[p.Name] {
			filtered = append(filtered, p)
		}
	}
	cp.Properties = filtered

	// Filter required.
	filteredReq := make([]string, 0, len(cp.Required))
	for _, r := range cp.Required {
		if !remove[r] {
			filteredReq = append(filteredReq, r)
		}
	}
	cp.Required = filteredReq

	return &TSType{inner: &cp}
}

// SetPropertyLiteral returns a new TSType with the named property's
// schema replaced by a literal value. The primitive type is inferred:
// string -> "string", float64/int/int64 -> "number", bool -> "boolean".
// The original is not modified.
func (t *TSType) SetPropertyLiteral(name string, value any) *TSType {
	if t == nil || t.inner == nil {
		return t
	}

	// Shallow copy the inner tsType.
	cp := *t.inner

	primType := inferPrimitiveType(value)
	litType := &tsType{
		Kind:          tsTypeLiteral,
		LiteralValue:  value,
		PrimitiveType: primType,
	}

	// Copy properties, replacing the matching one.
	newProps := make([]tsProperty, len(cp.Properties))
	copy(newProps, cp.Properties)
	found := false
	for i, p := range newProps {
		if p.Name == name {
			newProps[i] = tsProperty{Name: name, Schema: litType}
			found = true
			break
		}
	}
	if !found {
		newProps = append(newProps, tsProperty{Name: name, Schema: litType})
	}
	cp.Properties = newProps

	return &TSType{inner: &cp}
}

// SetPropertyType returns a new TSType with the named property's
// schema replaced by the given TSType. The original is not modified.
func (t *TSType) SetPropertyType(name string, typ *TSType) *TSType {
	if t == nil || t.inner == nil {
		return t
	}

	// Shallow copy the inner tsType.
	cp := *t.inner

	var schema *tsType
	if typ != nil {
		schema = typ.inner
	}

	// Copy properties, replacing the matching one.
	newProps := make([]tsProperty, len(cp.Properties))
	copy(newProps, cp.Properties)
	found := false
	for i, p := range newProps {
		if p.Name == name {
			newProps[i] = tsProperty{Name: name, Schema: schema}
			found = true
			break
		}
	}
	if !found {
		newProps = append(newProps, tsProperty{Name: name, Schema: schema})
	}
	cp.Properties = newProps

	return &TSType{inner: &cp}
}

// inferPrimitiveType returns the JSON Schema primitive type for a Go value.
func inferPrimitiveType(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case float64, int, int64:
		return "number"
	case bool:
		return "boolean"
	default:
		return ""
	}
}

// NewStringLiteralUnion creates a TSType representing a union of string literal
// types. For a single value it returns a literal type (renders as `"val"` in TS
// and `{"type":"string","enum":["val"]}` in JSON Schema). For multiple values it
// returns a union with CollapseLiterals so JSON Schema collapses to a single enum.
// Returns nil for nil or empty input.
func NewStringLiteralUnion(values []string) *TSType {
	if len(values) == 0 {
		return nil
	}
	if len(values) == 1 {
		return &TSType{inner: &tsType{
			Kind:             tsTypeUnion,
			CollapseLiterals: true,
			Types: []*tsType{
				{Kind: tsTypeLiteral, LiteralValue: values[0], PrimitiveType: "string"},
			},
		}}
	}
	children := make([]*tsType, len(values))
	for i, v := range values {
		children[i] = &tsType{Kind: tsTypeLiteral, LiteralValue: v, PrimitiveType: "string"}
	}
	return &TSType{inner: &tsType{
		Kind:             tsTypeUnion,
		CollapseLiterals: true,
		Types:            children,
	}}
}

// wrapTSType wraps a tsType in a TSType facade.
func wrapTSType(t *tsType) *TSType {
	if t == nil {
		return nil
	}
	return &TSType{inner: t}
}

// ---------------------------------------------------------------------------
// FuncSignature facade
// ---------------------------------------------------------------------------

// FuncSignature is a facade that hides tsFuncSig internals behind a clean public API.
type FuncSignature struct {
	inner *tsFuncSig
}

// Description returns the function's JSDoc description.
func (f *FuncSignature) Description() string {
	if f == nil || f.inner == nil {
		return ""
	}
	return f.inner.Description
}

// JSDocTag represents a single raw JSDoc tag.
type JSDocTag struct {
	Name string
	Text string
}

// Tags returns all raw JSDoc tags from the function's documentation.
func (f *FuncSignature) Tags() []JSDocTag {
	if f == nil || f.inner == nil {
		return nil
	}
	out := make([]JSDocTag, len(f.inner.Tags))
	for i, t := range f.inner.Tags {
		out[i] = JSDocTag{Name: t.Name, Text: t.Text}
	}
	return out
}

// FuncParam is a facade over a single function parameter.
type FuncParam struct {
	name        string
	typ         *TSType
	description string
	optional    bool
}

// Name returns the parameter name.
func (p *FuncParam) Name() string { return p.name }

// Type returns the parameter type as a TSType facade.
func (p *FuncParam) Type() *TSType { return p.typ }

// Description returns the parameter's JSDoc description.
func (p *FuncParam) Description() string { return p.description }

// Optional reports whether the parameter is optional (has a ? token or default value).
func (p *FuncParam) Optional() bool { return p.optional }

// Params returns the function's parameters as facade types.
func (f *FuncSignature) Params() []FuncParam {
	if f == nil || f.inner == nil {
		return nil
	}
	out := make([]FuncParam, len(f.inner.Params))
	for i, p := range f.inner.Params {
		out[i] = FuncParam{
			name:        p.Name,
			typ:         wrapTSType(p.Type),
			description: p.Description,
			optional:    p.Optional,
		}
	}
	return out
}

// Return returns the function's return type as a TSType facade.
// For async functions this includes the Promise wrapper (e.g. Promise<string>).
func (f *FuncSignature) Return() *TSType {
	if f == nil || f.inner == nil || f.inner.ReturnType == nil {
		return nil
	}
	return &TSType{inner: f.inner.ReturnType}
}

// ParamsAsObject synthesizes an object TSType from all function parameters.
// Each param becomes a property, with optional params excluded from "required".
// This is the shape needed for MCP JSON Schema.
func (f *FuncSignature) ParamsAsObject() *TSType {
	if f == nil || f.inner == nil || len(f.inner.Params) == 0 {
		return nil
	}
	props := make([]tsProperty, 0, len(f.inner.Params))
	var required []string
	for _, p := range f.inner.Params {
		propType := p.Type
		// Copy param description into the property's type annotations.
		if p.Description != "" && propType != nil {
			cp := *propType
			if cp.Annotations == nil {
				cp.Annotations = &tsAnnotations{}
			} else {
				annCopy := *cp.Annotations
				cp.Annotations = &annCopy
			}
			cp.Annotations.Description = p.Description
			propType = &cp
		}
		props = append(props, tsProperty{Name: p.Name, Schema: propType})
		if !p.Optional {
			required = append(required, p.Name)
		}
	}
	// Collect definitions from all parameter types.
	var defs map[string]*tsType
	for _, p := range f.inner.Params {
		if p.Type != nil && len(p.Type.Definitions) > 0 {
			if defs == nil {
				defs = make(map[string]*tsType)
			}
			for name, def := range p.Type.Definitions {
				defs[name] = def
			}
		}
	}
	combined := &tsType{
		Kind:        tsTypeObject,
		Properties:  props,
		Required:    required,
		Definitions: defs,
	}
	if f.inner.Description != "" {
		combined.Annotations = &tsAnnotations{Description: f.inner.Description}
	}
	return &TSType{inner: combined}
}

// AddParam returns a new FuncSignature with an additional parameter appended.
// The receiver is not modified. Returns nil if the receiver is nil.
func (f *FuncSignature) AddParam(name string, typ *TSType, description string, optional bool) *FuncSignature {
	if f == nil || f.inner == nil {
		return nil
	}
	// Shallow-copy the inner sig.
	cp := *f.inner
	// Copy the params slice so we don't mutate the original.
	newParams := make([]tsFuncParam, len(cp.Params), len(cp.Params)+1)
	copy(newParams, cp.Params)
	var inner *tsType
	if typ != nil {
		inner = typ.inner
	}
	newParams = append(newParams, tsFuncParam{
		Name:        name,
		Type:        inner,
		Description: description,
		Optional:    optional,
	})
	cp.Params = newParams
	return &FuncSignature{inner: &cp}
}

// ToTS renders the function signature as a TypeScript function type expression.
func (f *FuncSignature) ToTS() string {
	if f == nil || f.inner == nil {
		return "() => void"
	}
	return tsFuncSigToTS(f.inner)
}

// ToJSONSchema converts the first parameter's type to a JSON Schema map.
func (f *FuncSignature) ToJSONSchema() map[string]any {
	if f == nil || f.inner == nil {
		return nil
	}
	return tsFuncSigToJSON(f.inner)
}
