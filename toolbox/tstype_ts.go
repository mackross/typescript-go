package toolbox

// TSTypeToTS renders a TSType tree back to TypeScript source text.
// The output is semantically equivalent to the original type, though
// not necessarily character-for-character identical.
//
// This function does NOT depend on the checker or any internal/ packages.
func TSTypeToTS(t *TSType) string {
	return ""
}

// TSFuncSigToTS renders a TSFuncSig as a TypeScript function signature.
// Example output: (paramName: paramType, ...) => returnType
//
// This function does NOT depend on the checker or any internal/ packages.
func TSFuncSigToTS(sig *TSFuncSig) string {
	return ""
}
