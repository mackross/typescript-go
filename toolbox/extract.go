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
	ParamsSchema Schema         // keep for backward compat
	ParamsType   *TSType    // facade wrapper around the first param's tsType
	Sig          *FuncSignature       // facade wrapper around the full function signature
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

	var rawTags []jsdocTag
	if docs := gen.parseDocs(defaultExport); docs != nil {
		meta.Description = docs.description
	}

	// Collect all raw JSDoc tags from the default export node.
	if n := asNode(defaultExport); n != nil {
		for _, jsdoc := range n.JSDoc(nil) {
			if jsdoc == nil || jsdoc.AsJSDoc().Tags == nil {
				continue
			}
			for _, tag := range jsdoc.AsJSDoc().Tags.Nodes {
				if tag == nil {
					continue
				}
				tagName := tag.TagName().Text()
				commentText := jsdocText(tag.CommentList())

				// For @param tags, the parameter name is parsed
				// separately by the TS parser. Reconstruct the full
				// raw text by prepending the param name.
				if tag.Kind == ast.KindJSDocParameterTag {
					paramTag := tag.AsJSDocParameterOrPropertyTag()
					if paramTag.Name() != nil {
						pName := paramTag.Name().Text()
						if commentText != "" {
							commentText = pName + " " + commentText
						} else {
							commentText = pName
						}
					}
				}

				rawTags = append(rawTags, jsdocTag{
					Name: tagName,
					Text: commentText,
				})
			}
		}
	}

	functionType := ch.GetTypeOfSymbol(defaultExport.Symbol())
	signatures := ch.GetSignaturesOfType(functionType, checker.SignatureKindCall)
	if len(signatures) > 0 {
		sig := signatures[0]
		params := sig.Parameters()

		// Build a map of param name -> description from @param tags.
		paramDescs := map[string]string{}
		for _, tag := range rawTags {
			if tag.Name == "param" && tag.Text != "" {
				text := tag.Text
				// Strip leading dash: "paramName - description" or "paramName description"
				paramName, rest, _ := strings.Cut(text, " ")
				rest = strings.TrimSpace(rest)
				rest = strings.TrimPrefix(rest, "- ")
				rest = strings.TrimPrefix(rest, "-")
				rest = strings.TrimSpace(rest)
				paramDescs[paramName] = rest
			}
		}

		funcSig := &tsFuncSig{Description: meta.Description, Tags: rawTags}
		for _, param := range params {
			paramType := ch.GetTypeOfSymbolAtLocation(param, sig.Declaration())
			tsType, err := gen.extractTSType(paramType, param, sig.Declaration())
			if err != nil {
				return nil, fmt.Errorf("toolbox: generate param schema: %w", err)
			}
			optional := false
			if len(param.Declarations) > 0 {
				decl := param.Declarations[0]
				if decl.QuestionToken() != nil || decl.Initializer() != nil {
					optional = true
				}
			}
			funcSig.Params = append(funcSig.Params, tsFuncParam{
				Name:        param.Name,
				Type:        tsType,
				Description: paramDescs[param.Name],
				Optional:    optional,
			})
		}

		// Extract the return type.
		retType := ch.GetReturnTypeOfSignature(sig)
		if retType != nil {
			rt, err := gen.extractTSType(retType, nil, sig.Declaration())
			if err == nil {
				// For async functions the checker returns Promise<T>;
				// store unwrapped T inside the tsType so callers can
				// access it via UnwrapPromise().
				if promised := ch.GetPromisedTypeOfPromise(retType); promised != nil {
					urt, err2 := gen.extractTSType(promised, nil, sig.Declaration())
					if err2 == nil {
						rt.PromiseInner = urt
					}
				}
				funcSig.ReturnType = rt
			}
		}
		if len(params) > 0 {
			paramType := funcSig.Params[0].Type

			// Attach any definitions gathered during extraction.
			// extractTSType populates g.definitions when it encounters
			// types that should be emitted as definitions (e.g. type
			// aliases, interfaces), but the returned tsType only has
			// $ref pointers — the definitions map must be attached to
			// the root tsType so callers can resolve them.
			if len(gen.definitions) > 0 && paramType != nil {
				if paramType.Definitions == nil {
					paramType.Definitions = make(map[string]*tsType, len(gen.definitions))
				}
				for name, def := range gen.definitions {
					if _, exists := paramType.Definitions[name]; !exists {
						paramType.Definitions[name] = schemaMapToTSType(def)
					}
				}
			}

			meta.ParamsSchema = tsTypeToJSON(paramType)
			meta.ParamsType = &TSType{inner: paramType}
		}
		meta.Sig = &FuncSignature{inner: funcSig}
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
