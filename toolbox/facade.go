package toolbox

// ParamsType is a facade that hides tsType internals behind a clean public API.
type ParamsType struct {
	inner *tsType
}

// --- Rendering ---

// ToJSONSchema converts the type to a JSON Schema map.
func (t *ParamsType) ToJSONSchema() map[string]any {
	if t == nil || t.inner == nil {
		return map[string]any{}
	}
	return tsTypeToJSON(t.inner)
}

// ToTS renders the type as TypeScript source text.
func (t *ParamsType) ToTS() string {
	if t == nil || t.inner == nil {
		return "any"
	}
	return tsTypeToTS(t.inner)
}

// Declarations emits type alias declarations for all definitions
// in the type (e.g. "type Foo = ...;\n"). Returns empty string when
// there are no definitions.
func (t *ParamsType) Declarations() string {
	if t == nil || t.inner == nil {
		return ""
	}
	return tsTypeDeclarationsToTS(t.inner)
}

// --- Inspection ---

// PropertyNames returns the names of all properties on an object type.
// Returns nil for non-object types.
func (t *ParamsType) PropertyNames() []string {
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
func (t *ParamsType) HasProperty(name string) bool {
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

// IsObject reports whether the underlying type is an object.
func (t *ParamsType) IsObject() bool {
	if t == nil || t.inner == nil {
		return false
	}
	return t.inner.Kind == tsTypeObject
}

// --- Promise unwrapping ---

// UnwrapPromise returns the inner type T if the receiver represents Promise<T>.
// If not a Promise, returns the receiver unchanged.
func (t *ParamsType) UnwrapPromise() *ParamsType {
	if t == nil || t.inner == nil {
		return t
	}
	if t.inner.PromiseInner != nil {
		return &ParamsType{inner: t.inner.PromiseInner}
	}
	return t
}

// --- Manipulation (returns new copies) ---

// RemoveProperties returns a new ParamsType with the named properties removed.
// The original is not modified.
func (t *ParamsType) RemoveProperties(names ...string) *ParamsType {
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

	return &ParamsType{inner: &cp}
}

// SetPropertyLiteral returns a new ParamsType with the named property's
// schema replaced by a literal value. The primitive type is inferred:
// string -> "string", float64/int/int64 -> "number", bool -> "boolean".
// The original is not modified.
func (t *ParamsType) SetPropertyLiteral(name string, value any) *ParamsType {
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

	return &ParamsType{inner: &cp}
}

// SetPropertyType returns a new ParamsType with the named property's
// schema replaced by the given ParamsType. The original is not modified.
func (t *ParamsType) SetPropertyType(name string, typ *ParamsType) *ParamsType {
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

	return &ParamsType{inner: &cp}
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


// wrapParamsType wraps a tsType in a ParamsType facade.
func wrapParamsType(t *tsType) *ParamsType {
	if t == nil {
		return nil
	}
	return &ParamsType{inner: t}
}

// ---------------------------------------------------------------------------
// FuncSig facade
// ---------------------------------------------------------------------------

// FuncSig is a facade that hides tsFuncSig internals behind a clean public API.
type FuncSig struct {
	inner *tsFuncSig
}

// Description returns the function's JSDoc description.
func (f *FuncSig) Description() string {
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
func (f *FuncSig) Tags() []JSDocTag {
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
	typ         *ParamsType
	description string
	optional    bool
}

// Name returns the parameter name.
func (p *FuncParam) Name() string { return p.name }

// Type returns the parameter type as a ParamsType facade.
func (p *FuncParam) Type() *ParamsType { return p.typ }

// Description returns the parameter's JSDoc description.
func (p *FuncParam) Description() string { return p.description }

// Optional reports whether the parameter is optional (has a ? token or default value).
func (p *FuncParam) Optional() bool { return p.optional }

// Params returns the function's parameters as facade types.
func (f *FuncSig) Params() []FuncParam {
	if f == nil || f.inner == nil {
		return nil
	}
	out := make([]FuncParam, len(f.inner.Params))
	for i, p := range f.inner.Params {
		out[i] = FuncParam{
			name:        p.Name,
			typ:         wrapParamsType(p.Type),
			description: p.Description,
			optional:    p.Optional,
		}
	}
	return out
}

// Return returns the function's return type as a ParamsType facade.
// For async functions this includes the Promise wrapper (e.g. Promise<string>).
func (f *FuncSig) Return() *ParamsType {
	if f == nil || f.inner == nil || f.inner.ReturnType == nil {
		return nil
	}
	return &ParamsType{inner: f.inner.ReturnType}
}


// CombinedParamsType returns a synthetic object ParamsType that combines all
// function parameters into a single object type. Each param becomes a property,
// with optional params excluded from "required". This is the shape needed for
// MCP JSON Schema.
func (f *FuncSig) CombinedParamsType() *ParamsType {
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
	return &ParamsType{inner: combined}
}

// ToTS renders the function signature as a TypeScript function type expression.
func (f *FuncSig) ToTS() string {
	if f == nil || f.inner == nil {
		return "() => void"
	}
	return tsFuncSigToTS(f.inner)
}

// ToJSONSchema converts the first parameter's type to a JSON Schema map.
func (f *FuncSig) ToJSONSchema() map[string]any {
	if f == nil || f.inner == nil {
		return nil
	}
	return tsFuncSigToJSON(f.inner)
}
