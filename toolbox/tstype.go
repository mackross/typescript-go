package toolbox

// TSType is a checker-independent intermediate representation of a TypeScript
// type.  It captures everything needed to render a JSON Schema without holding
// a reference to the checker or any internal/ packages.
//
// Build a TSType tree via ExtractTSType (requires a checker), then render to
// JSON Schema via TSTypeToJSON (no checker needed).
type TSType struct {
	// Kind selects which branch of the union this node represents.
	Kind TSTypeKind

	// --- Primitive / simple ---

	// Type is the JSON Schema "type" value: "string", "number", "integer",
	// "boolean", "null", "object", "array".  May also be a list of types
	// when Kind == TSTypeSimple and multiple primitive types are unioned.
	Type any // string or []any (for multi-type)

	// --- Literal / const / enum ---

	// ConstValue holds the literal value when Kind == TSTypeLiteral.
	ConstValue any
	// UseEnum controls whether the literal is rendered as "enum":[v]
	// instead of "const":v.
	UseEnum bool
	// EnumValues holds the values when Kind == TSTypeEnum.
	EnumValues []any

	// --- Ref ---

	// Ref is the $ref URI string when Kind == TSTypeRef.
	Ref string

	// --- Object ---

	// Properties maps property names to their TSType schemas.
	Properties []TSProperty
	// HasProperties is true when the "properties" key was explicitly present
	// in the schema (even if empty).  This preserves `"properties": {}` in
	// round-tripped output.
	HasProperties bool
	// Required lists required property names.
	Required []string
	// AdditionalProperties is the schema for additional properties:
	//   - nil means omitted
	//   - *TSType{} means a type constraint
	//   - use AdditionalPropertiesBool for true/false
	AdditionalProperties     *TSType
	AdditionalPropertiesBool *bool
	// PatternProperties maps regex patterns to schemas.
	PatternProperties []TSPatternProperty

	// --- Array ---

	// Items is the single items schema for homogeneous arrays.
	Items *TSType
	// TupleItems is the per-element schemas for tuple types.
	TupleItems []*TSType
	// AdditionalItems is the schema for additional tuple items.
	AdditionalItems *TSType
	// MinItems / MaxItems for tuple types.
	MinItems *int
	MaxItems *int

	// --- Composite ---

	// AnyOf holds the branches for union types (anyOf).
	AnyOf []*TSType
	// AllOf holds the branches for intersection types (allOf).
	AllOf []*TSType

	// --- Template literal ---

	// Pattern is the JSON Schema "pattern" for template literal types.
	Pattern string
	// Format is the JSON Schema "format" (e.g. "date-time").
	Format string

	// --- Nullable ---

	// Nullable, when true, indicates the type should be made nullable
	// in the JSON Schema output.
	Nullable bool

	// --- Annotations (from JSDoc / @TJS-* tags) ---

	Annotations *TSAnnotations

	// --- Definitions ---

	// Definitions maps definition names to their TSType schemas.  This is
	// populated on root schemas that contain $ref'd sub-types.
	Definitions map[string]*TSType

	// --- Top-level schema keys ---

	// SchemaURI is the "$schema" value for root schemas.
	SchemaURI string
	// ID is the "$id" value.
	ID string

	// --- Extra fields for doc annotations ---

	// ExtraFields captures arbitrary additional JSON Schema keys from
	// @TJS-* annotations that don't map to a named field above (e.g.
	// "hide", "chance", "important", "typeof", custom validation keywords).
	ExtraFields map[string]any
}

// TSTypeKind discriminates the different shapes a TSType can take.
type TSTypeKind int

const (
	// TSTypeSimple is a primitive or multi-type node (just "type": ...).
	TSTypeSimple TSTypeKind = iota
	// TSTypeLiteral is a const/enum literal value.
	TSTypeLiteral
	// TSTypeEnum is a set of enumerated values.
	TSTypeEnum
	// TSTypeRef is a $ref pointer.
	TSTypeRef
	// TSTypeObject is an object with properties.
	TSTypeObject
	// TSTypeArray is a homogeneous array.
	TSTypeArray
	// TSTypeTuple is a tuple array (items is a list of schemas).
	TSTypeTuple
	// TSTypeUnion is an anyOf composite.
	TSTypeUnion
	// TSTypeIntersection is an allOf composite.
	TSTypeIntersection
	// TSTypeAny represents any/unknown (empty schema {}).
	TSTypeAny
)

// TSProperty represents a single property in an object schema.
type TSProperty struct {
	Name   string
	Schema *TSType
}

// TSPatternProperty represents a pattern property in an object schema.
type TSPatternProperty struct {
	Pattern string
	Schema  *TSType
}

// TSAnnotations holds documentation annotations from JSDoc.
type TSAnnotations struct {
	Description string
	Title       string
	Comment     string // $comment
	ID          string // $id from annotation (distinct from top-level ID)
	Ref         string // $ref from annotation
	Default     any
	HasDefault  bool // true when Default was explicitly set (even to nil/null)
	// Extra holds arbitrary @TJS-* and validation keyword annotations.
	Extra map[string]any
}

// TSFuncSig wraps a function signature's parameter types as TSType nodes,
// along with the function's description.  This is the structured output
// from ExtractToolMetadata.
type TSFuncSig struct {
	Description string
	Params      []TSFuncParam
}

// TSFuncParam represents a single function parameter with its name and type.
type TSFuncParam struct {
	Name string
	Type *TSType
}
