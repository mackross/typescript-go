package toolbox

// tsType is a checker-independent intermediate representation of a TypeScript
// type.  Unlike the previous implementation, it captures TS-level type
// information directly from checker.Type rather than going through JSON Schema
// as an intermediate step.
//
// Build a tsType tree via Generator.extractTSType (requires a checker), then
// render to JSON Schema via tsTypeToJSON (no checker needed).
type tsType struct {
	// Kind selects which variant of the type this node represents.
	Kind tsTypeKind

	// --- Primitive ---

	// PrimitiveType is the TS primitive: "string", "number", "boolean",
	// "null", "any", "unknown", "bigint", "symbol", "integer".
	// Set when Kind == tsTypePrimitive.
	PrimitiveType string

	// --- Literal ---

	// LiteralValue holds the literal value when Kind == tsTypeLiteral.
	// For string literals: string, for number literals: float64,
	// for boolean literals: bool, for null: nil.
	LiteralValue any

	// --- Ref ---

	// Ref is the $ref URI string when Kind == tsTypeRef.
	Ref string

	// --- Object ---

	// Properties maps property names to their tsType schemas.
	Properties []tsProperty
	// Required lists required property names.
	Required []string
	// AdditionalProperties is the schema for additional properties:
	//   - nil means omitted
	//   - *tsType{} means a type constraint (e.g. string index signature)
	AdditionalProperties     *tsType
	AdditionalPropertiesBool *bool
	// PatternProperties maps regex patterns to schemas (e.g. numeric index).
	PatternProperties []tsPatternProperty
	// EmptyObject flags objects with no own properties but not "any" (e.g. {}).
	EmptyObject bool
	// WildcardObject flags the "object" type (additionalProperties: true).
	WildcardObject bool

	// --- Array ---

	// Items is the element type for homogeneous arrays.
	Items *tsType

	// --- Tuple ---

	// TupleItems is the per-element types for tuple types.
	TupleItems []*tsType
	// AdditionalItems is the schema for additional tuple items.
	AdditionalItems *tsType
	// MinItems / MaxItems for tuple types.
	MinItems *int
	MaxItems *int

	// --- Union ---

	// Types holds the branches when Kind is tsTypeUnion or tsTypeIntersection.
	Types []*tsType
	// CollapseLiterals is set on tsTypeUnion nodes that were produced by
	// walking checker types directly.  When true, tsTypeToJSON will
	// collapse homogeneous literal children into {"enum": [...]} instead
	// of rendering each literal as a separate anyOf branch.
	CollapseLiterals bool

	// --- Template literal ---

	// Pattern is the regex pattern for template literal types.
	Pattern string

	// --- Format ---

	// Format is a JSON Schema format hint (e.g. "date-time" for Date).
	Format string

	// --- Nullable ---

	// Nullable indicates that the type includes null (e.g. T | null).
	// This is tracked separately so that tsTypeToJSON can decide
	// whether to use type-array or anyOf wrapping.
	Nullable bool

	// --- Annotations (from JSDoc / @TJS-* tags) ---

	Annotations *tsAnnotations

	// --- Definitions ---

	// Definitions maps definition names to their tsType schemas.
	// Populated on root schemas that contain $ref'd sub-types.
	Definitions map[string]*tsType

	// --- Top-level schema keys ---

	// SchemaURI is the "$schema" value for root schemas.
	SchemaURI string
	// ID is the "$id" value.
	ID string

	// --- Doc type override ---

	// DocTypeOverride is set when a @TJS-type annotation overrides the
	// inferred type (e.g. @TJS-type number on a class).
	DocTypeOverride string

	// --- Enum ---

	// EnumValues holds the values when the type is a TS enum declaration
	// (not a union of literals, but an actual `enum Foo { ... }`).
	EnumValues []any

	// --- Extra fields for doc annotations ---

	// ExtraFields captures arbitrary additional JSON Schema keys from
	// @TJS-* annotations (e.g. "hide", "chance", "important", "typeof").
	ExtraFields map[string]any

	// PromiseInner holds the inner type T when this type represents Promise<T>.
	// nil when the type is not a Promise.
	PromiseInner *tsType
}

// tsTypeKind discriminates the different shapes a tsType can take.
type tsTypeKind int

const (
	// tsTypePrimitive represents a primitive type (string, number, boolean, null, etc.).
	tsTypePrimitive tsTypeKind = iota
	// tsTypeLiteral represents a literal type ("hello", 42, true, etc.).
	tsTypeLiteral
	// tsTypeRef is a $ref pointer to a definition.
	tsTypeRef
	// tsTypeObject is an object with properties.
	tsTypeObject
	// tsTypeArray is a homogeneous array (T[]).
	tsTypeArray
	// tsTypeTuple is a tuple type ([T, U, V]).
	tsTypeTuple
	// tsTypeUnion is a union type (A | B | C).
	tsTypeUnion
	// tsTypeIntersection is an intersection type (A & B).
	tsTypeIntersection
	// tsTypeAny represents any/unknown (empty schema {}).
	tsTypeAny
	// tsTypeTemplateLiteral represents a template literal type.
	tsTypeTemplateLiteral
	// tsTypeEnum represents a TS enum declaration (enum Foo { A, B }).
	tsTypeEnum
)

// tsProperty represents a single property in an object type.
type tsProperty struct {
	Name   string
	Schema *tsType
}

// tsPatternProperty represents a pattern property in an object type.
type tsPatternProperty struct {
	Pattern string
	Schema  *tsType
}

// tsAnnotations holds documentation annotations from JSDoc.
type tsAnnotations struct {
	Description string
	Title       string
	Comment     string // $comment
	ID          string // $id from annotation
	Ref         string // $ref from annotation
	Default     any
	HasDefault  bool // true when Default was explicitly set (even to nil/null)
	Nullable    bool // true when @nullable was set
	// Extra holds arbitrary @TJS-* and validation keyword annotations.
	Extra map[string]any
}

// jsdocTag represents a single raw JSDoc tag (e.g. @param, @accessMode).
type jsdocTag struct {
	Name string
	Text string
}

// tsFuncSig wraps a function signature's parameter types as tsType nodes,
// along with the function's description.
type tsFuncSig struct {
	Description string
	Params      []tsFuncParam
	ReturnType  *tsType
	Tags        []jsdocTag
}

// tsFuncParam represents a single function parameter with its name and type.
type tsFuncParam struct {
	Name        string
	Type        *tsType
	Description string
	Optional    bool
}

// inferNumberType returns the number type string to use for enum rendering.
// It checks the literal children's PrimitiveType to determine whether
// "number" or "integer" should be used.
func (t *tsType) inferNumberType() string {
	if t == nil {
		return "number"
	}
	for _, child := range t.Types {
		if child != nil && child.Kind == tsTypeLiteral && (child.PrimitiveType == "integer" || child.PrimitiveType == "number") {
			return child.PrimitiveType
		}
	}
	return "number"
}
