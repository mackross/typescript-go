package toolbox

// TSType is a checker-independent intermediate representation of a TypeScript
// type.  Unlike the previous implementation, it captures TS-level type
// information directly from checker.Type rather than going through JSON Schema
// as an intermediate step.
//
// Build a TSType tree via Generator.extractTSType (requires a checker), then
// render to JSON Schema via TSTypeToJSON (no checker needed).
type TSType struct {
	// Kind selects which variant of the type this node represents.
	Kind TSTypeKind

	// --- Primitive ---

	// PrimitiveType is the TS primitive: "string", "number", "boolean",
	// "null", "any", "unknown", "bigint", "symbol", "integer".
	// Set when Kind == TSTypePrimitive.
	PrimitiveType string

	// --- Literal ---

	// LiteralValue holds the literal value when Kind == TSTypeLiteral.
	// For string literals: string, for number literals: float64,
	// for boolean literals: bool, for null: nil.
	LiteralValue any

	// --- Ref ---

	// Ref is the $ref URI string when Kind == TSTypeRef.
	Ref string

	// --- Object ---

	// Properties maps property names to their TSType schemas.
	Properties []TSProperty
	// Required lists required property names.
	Required []string
	// AdditionalProperties is the schema for additional properties:
	//   - nil means omitted
	//   - *TSType{} means a type constraint (e.g. string index signature)
	AdditionalProperties     *TSType
	AdditionalPropertiesBool *bool
	// PatternProperties maps regex patterns to schemas (e.g. numeric index).
	PatternProperties []TSPatternProperty
	// EmptyObject flags objects with no own properties but not "any" (e.g. {}).
	EmptyObject bool
	// WildcardObject flags the "object" type (additionalProperties: true).
	WildcardObject bool

	// --- Array ---

	// Items is the element type for homogeneous arrays.
	Items *TSType

	// --- Tuple ---

	// TupleItems is the per-element types for tuple types.
	TupleItems []*TSType
	// AdditionalItems is the schema for additional tuple items.
	AdditionalItems *TSType
	// MinItems / MaxItems for tuple types.
	MinItems *int
	MaxItems *int

	// --- Union ---

	// Types holds the branches when Kind is TSTypeUnion or TSTypeIntersection.
	Types []*TSType
	// CollapseLiterals is set on TSTypeUnion nodes that were produced by
	// walking checker types directly.  When true, TSTypeToJSON will
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
	// This is tracked separately so that TSTypeToJSON can decide
	// whether to use type-array or anyOf wrapping.
	Nullable bool

	// --- Annotations (from JSDoc / @TJS-* tags) ---

	Annotations *TSAnnotations

	// --- Definitions ---

	// Definitions maps definition names to their TSType schemas.
	// Populated on root schemas that contain $ref'd sub-types.
	Definitions map[string]*TSType

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
}

// TSTypeKind discriminates the different shapes a TSType can take.
type TSTypeKind int

const (
	// TSTypePrimitive represents a primitive type (string, number, boolean, null, etc.).
	TSTypePrimitive TSTypeKind = iota
	// TSTypeLiteral represents a literal type ("hello", 42, true, etc.).
	TSTypeLiteral
	// TSTypeRef is a $ref pointer to a definition.
	TSTypeRef
	// TSTypeObject is an object with properties.
	TSTypeObject
	// TSTypeArray is a homogeneous array (T[]).
	TSTypeArray
	// TSTypeTuple is a tuple type ([T, U, V]).
	TSTypeTuple
	// TSTypeUnion is a union type (A | B | C).
	TSTypeUnion
	// TSTypeIntersection is an intersection type (A & B).
	TSTypeIntersection
	// TSTypeAny represents any/unknown (empty schema {}).
	TSTypeAny
	// TSTypeTemplateLiteral represents a template literal type.
	TSTypeTemplateLiteral
	// TSTypeEnum represents a TS enum declaration (enum Foo { A, B }).
	TSTypeEnum
)

// TSProperty represents a single property in an object type.
type TSProperty struct {
	Name   string
	Schema *TSType
}

// TSPatternProperty represents a pattern property in an object type.
type TSPatternProperty struct {
	Pattern string
	Schema  *TSType
}

// TSAnnotations holds documentation annotations from JSDoc.
type TSAnnotations struct {
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

// TSFuncSig wraps a function signature's parameter types as TSType nodes,
// along with the function's description.
type TSFuncSig struct {
	Description string
	Params      []TSFuncParam
}

// TSFuncParam represents a single function parameter with its name and type.
type TSFuncParam struct {
	Name string
	Type *TSType
}

// inferNumberType returns the number type string to use for enum rendering.
// It checks the literal children's PrimitiveType to determine whether
// "number" or "integer" should be used.
func (t *TSType) inferNumberType() string {
	if t == nil {
		return "number"
	}
	for _, child := range t.Types {
		if child != nil && child.Kind == TSTypeLiteral && (child.PrimitiveType == "integer" || child.PrimitiveType == "number") {
			return child.PrimitiveType
		}
	}
	return "number"
}
