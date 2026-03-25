package toolbox

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/bundled"
	"github.com/microsoft/typescript-go/internal/checker"
	"github.com/microsoft/typescript-go/internal/compiler"
	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/tsoptions"
	"github.com/microsoft/typescript-go/internal/vfs/vfstest"
)

type ToolMetadata struct {
	Description  string
	ParamsSchema Schema
	// ParamsTSType is the structured intermediate representation of the
	// first parameter's type.  It is populated alongside ParamsSchema and
	// can be manipulated (e.g. to splice in literal types for bound
	// parameters) before being rendered to JSON Schema via TSTypeToJSON.
	ParamsTSType *TSType
	// FuncSig provides the full function signature as structured TSType
	// nodes, including all parameters and their names.
	FuncSig *TSFuncSig
}

type ExtractInput struct {
	Files                     fs.FS
	Entry                     string
	CurrentDirectory          string
	UseCaseSensitiveFileNames bool
}

func ExtractToolMetadata(ctx context.Context, input ExtractInput) (*ToolMetadata, error) {
	if input.Files == nil {
		return nil, fmt.Errorf("toolbox: files are required")
	}
	if input.Entry == "" {
		return nil, fmt.Errorf("toolbox: entry is required")
	}

	currentDirectory := input.CurrentDirectory
	if currentDirectory == "" {
		currentDirectory = "/"
	}

	files, err := rootedFiles(input.Files, currentDirectory)
	if err != nil {
		return nil, err
	}
	entryPath := rootedPath(currentDirectory, input.Entry)
	if _, ok := files[entryPath]; !ok {
		return nil, fmt.Errorf("toolbox: entry %q not found", input.Entry)
	}

	configPath := path.Join(currentDirectory, "tsconfig.json")
	if _, ok := files[configPath]; !ok {
		var sourceFiles []string
		for f := range files {
			ext := path.Ext(f)
			if ext == ".ts" || ext == ".tsx" || ext == ".mts" || ext == ".cts" {
				rel := strings.TrimPrefix(f, currentDirectory+"/")
				if rel == "" {
					rel = strings.TrimPrefix(f, currentDirectory)
				}
				sourceFiles = append(sourceFiles, rel)
			}
		}
		files[configPath] = syntheticExtractTSConfig(sourceFiles)
	}

	vfs := bundled.WrapFS(vfstest.FromMap(files, input.UseCaseSensitiveFileNames))
	host := compiler.NewCompilerHost(currentDirectory, vfs, bundled.LibPath(), nil, nil)

	parsed, diags := tsoptions.GetParsedCommandLineOfConfigFile(configPath, &core.CompilerOptions{}, nil, host, nil)
	if len(diags) > 0 {
		return nil, fmt.Errorf("toolbox: parse tsconfig: %v", diags[0])
	}
	if parsed == nil {
		return nil, fmt.Errorf("toolbox: parse tsconfig failed")
	}

	program := compiler.NewProgram(compiler.ProgramOptions{
		Host:   host,
		Config: parsed,
	})

	ch, done := program.GetTypeChecker(ctx)
	defer done()

	file := program.GetSourceFile(entryPath)
	if file == nil {
		return nil, fmt.Errorf("toolbox: source file %q not found in program", entryPath)
	}

	defaultExport := findDefaultExportFunction(ch, file)
	if defaultExport == nil {
		return nil, fmt.Errorf("toolbox: no default export function found in %q", input.Entry)
	}

	gen, err := NewGenerator(program, DefaultOptions())
	if err != nil {
		return nil, fmt.Errorf("toolbox: create generator: %w", err)
	}
	defer gen.Close()

	meta := &ToolMetadata{}

	if docs := gen.parseDocs(defaultExport); docs != nil {
		meta.Description = docs.description
	}

	functionType := ch.GetTypeOfSymbol(defaultExport.Symbol())
	signatures := ch.GetSignaturesOfType(functionType, checker.SignatureKindCall)
	if len(signatures) > 0 {
		sig := signatures[0]
		params := sig.Parameters()

		funcSig := &TSFuncSig{Description: meta.Description}
		for _, param := range params {
			paramType := ch.GetTypeOfSymbolAtLocation(param, sig.Declaration())
			tsType, err := gen.extractTSType(paramType, param, sig.Declaration())
			if err != nil {
				return nil, fmt.Errorf("toolbox: generate param schema: %w", err)
			}
			funcSig.Params = append(funcSig.Params, TSFuncParam{
				Name: param.Name,
				Type: tsType,
			})
		}
		meta.FuncSig = funcSig

		if len(params) > 0 {
			paramType := funcSig.Params[0].Type

			// Attach any definitions gathered during extraction.
			// extractTSType populates g.definitions when it encounters
			// types that should be emitted as definitions (e.g. type
			// aliases, interfaces), but the returned TSType only has
			// $ref pointers — the definitions map must be attached to
			// the root TSType so callers can resolve them.
			if len(gen.definitions) > 0 && paramType != nil {
				if paramType.Definitions == nil {
					paramType.Definitions = make(map[string]*TSType, len(gen.definitions))
				}
				for name, def := range gen.definitions {
					if _, exists := paramType.Definitions[name]; !exists {
						paramType.Definitions[name] = schemaMapToTSType(def)
					}
				}
			}

			meta.ParamsSchema = TSTypeToJSON(paramType)
			meta.ParamsTSType = paramType
		}
	}

	return meta, nil
}

func findDefaultExportFunction(ch *checker.Checker, file *ast.SourceFile) *ast.Node {
	moduleSym := file.AsNode().Symbol()
	if moduleSym == nil {
		return nil
	}
	exports := ch.GetExportsOfModule(moduleSym)
	for _, exp := range exports {
		if exp.Name == "default" {
			decls := exp.Declarations
			for _, decl := range decls {
				if decl == nil {
					continue
				}
				if decl.Kind == ast.KindFunctionDeclaration {
					return decl
				}
				if decl.Kind == ast.KindExportAssignment {
					expr := decl.AsExportAssignment().Expression
					if expr != nil && expr.Kind == ast.KindFunctionExpression {
						return expr
					}
					if expr != nil && expr.Kind == ast.KindIdentifier {
						sym := ch.GetSymbolAtLocation(expr)
						if sym != nil {
							for _, d := range sym.Declarations {
								if d != nil && d.Kind == ast.KindFunctionDeclaration {
									return d
								}
							}
						}
					}
				}
			}
		}
	}
	return nil
}

func syntheticExtractTSConfig(files []string) string {
	b := strings.Builder{}
	b.WriteString("{\n")
	b.WriteString("  \"compilerOptions\": {\n")
	b.WriteString("    \"strict\": true,\n")
	b.WriteString("    \"module\": \"esnext\",\n")
	b.WriteString("    \"target\": \"esnext\",\n")
	b.WriteString("    \"moduleResolution\": \"bundler\",\n")
	b.WriteString("    \"allowImportingTsExtensions\": true\n")
	b.WriteString("  },\n")
	b.WriteString("  \"files\": [\n")
	for i, file := range files {
		if i > 0 {
			b.WriteString(",\n")
		}
		b.WriteString(fmt.Sprintf("    %q", file))
	}
	b.WriteString("\n  ]\n")
	b.WriteString("}\n")
	return b.String()
}
