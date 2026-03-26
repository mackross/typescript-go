package toolbox

// ParamsType is a facade that hides TSType internals behind a clean public API.
type ParamsType struct {
	inner *TSType
}

// NewParamsType wraps an existing TSType in the facade.
func NewParamsType(t *TSType) *ParamsType {
	if t == nil {
		return nil
	}
	return &ParamsType{inner: t}
}

// Inner returns the underlying TSType.  Use sparingly; prefer facade methods.
func (t *ParamsType) Inner() *TSType {
	if t == nil {
		return nil
	}
	return t.inner
}

// --- Rendering ---

// ToJSONSchema converts the type to a JSON Schema map.
func (t *ParamsType) ToJSONSchema() map[string]any {
	if t == nil || t.inner == nil {
		return map[string]any{}
	}
	return TSTypeToJSON(t.inner)
}

// ToTS renders the type as TypeScript source text.
func (t *ParamsType) ToTS() string {
	if t == nil || t.inner == nil {
		return "any"
	}
	return TSTypeToTS(t.inner)
}

// Declarations emits type alias declarations for all definitions
// in the type (e.g. "type Foo = ...;\n"). Returns empty string when
// there are no definitions.
func (t *ParamsType) Declarations() string {
	if t == nil || t.inner == nil {
		return ""
	}
	return TSTypeDeclarationsToTS(t.inner)
}

// --- Inspection ---

// PropertyNames returns the names of all properties on an object type.
// Returns nil for non-object types.
func (t *ParamsType) PropertyNames() []string {
	if t == nil || t.inner == nil || t.inner.Kind != TSTypeObject {
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
	if t == nil || t.inner == nil || t.inner.Kind != TSTypeObject {
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
	return t.inner.Kind == TSTypeObject
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

	// Shallow copy the inner TSType.
	cp := *t.inner

	// Filter properties.
	filtered := make([]TSProperty, 0, len(cp.Properties))
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

	// Shallow copy the inner TSType.
	cp := *t.inner

	primType := inferPrimitiveType(value)
	litType := &TSType{
		Kind:          TSTypeLiteral,
		LiteralValue:  value,
		PrimitiveType: primType,
	}

	// Copy properties, replacing the matching one.
	newProps := make([]TSProperty, len(cp.Properties))
	copy(newProps, cp.Properties)
	found := false
	for i, p := range newProps {
		if p.Name == name {
			newProps[i] = TSProperty{Name: name, Schema: litType}
			found = true
			break
		}
	}
	if !found {
		newProps = append(newProps, TSProperty{Name: name, Schema: litType})
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

	// Shallow copy the inner TSType.
	cp := *t.inner

	var schema *TSType
	if typ != nil {
		schema = typ.inner
	}

	// Copy properties, replacing the matching one.
	newProps := make([]TSProperty, len(cp.Properties))
	copy(newProps, cp.Properties)
	found := false
	for i, p := range newProps {
		if p.Name == name {
			newProps[i] = TSProperty{Name: name, Schema: schema}
			found = true
			break
		}
	}
	if !found {
		newProps = append(newProps, TSProperty{Name: name, Schema: schema})
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

// ---------------------------------------------------------------------------
// FuncSig facade
// ---------------------------------------------------------------------------

// FuncSig is a facade that hides TSFuncSig internals behind a clean public API.
type FuncSig struct {
	inner *TSFuncSig
}

// NewFuncSig wraps an existing TSFuncSig in the facade.
func NewFuncSig(sig *TSFuncSig) *FuncSig {
	if sig == nil {
		return nil
	}
	return &FuncSig{inner: sig}
}

// Inner returns the underlying TSFuncSig.
func (f *FuncSig) Inner() *TSFuncSig {
	if f == nil {
		return nil
	}
	return f.inner
}

// Description returns the function's JSDoc description.
func (f *FuncSig) Description() string {
	if f == nil || f.inner == nil {
		return ""
	}
	return f.inner.Description
}

// FuncParam is a facade over a single function parameter.
type FuncParam struct {
	name string
	typ  *ParamsType
}

// Name returns the parameter name.
func (p *FuncParam) Name() string { return p.name }

// Type returns the parameter type as a ParamsType facade.
func (p *FuncParam) Type() *ParamsType { return p.typ }

// Params returns the function's parameters as facade types.
func (f *FuncSig) Params() []FuncParam {
	if f == nil || f.inner == nil {
		return nil
	}
	out := make([]FuncParam, len(f.inner.Params))
	for i, p := range f.inner.Params {
		out[i] = FuncParam{
			name: p.Name,
			typ:  NewParamsType(p.Type),
		}
	}
	return out
}

// Return returns the function's return type as a ParamsType facade.
func (f *FuncSig) Return() *ParamsType {
	if f == nil || f.inner == nil || f.inner.ReturnType == nil {
		return nil
	}
	return &ParamsType{inner: f.inner.ReturnType}
}

// ToTS renders the function signature as a TypeScript function type expression.
func (f *FuncSig) ToTS() string {
	if f == nil || f.inner == nil {
		return "() => void"
	}
	return TSFuncSigToTS(f.inner)
}

// ToJSONSchema converts the first parameter's type to a JSON Schema map.
func (f *FuncSig) ToJSONSchema() map[string]any {
	if f == nil || f.inner == nil {
		return nil
	}
	return TSFuncSigToJSON(f.inner)
}
