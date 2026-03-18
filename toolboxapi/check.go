package toolboxapi

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/bundled"
	"github.com/microsoft/typescript-go/internal/locale"
	"github.com/microsoft/typescript-go/internal/lsp/lsproto"
	"github.com/microsoft/typescript-go/internal/project"
	"github.com/microsoft/typescript-go/internal/vfs/vfstest"
)

type CheckInput struct {
	Files                     fs.FS
	Entry                     string
	CurrentDirectory          string
	UseCaseSensitiveFileNames bool
}

type Diagnostic struct {
	File    string
	Message string
	Code    int32
	Pos     int
	End     int
}

func Check(ctx context.Context, input CheckInput) ([]Diagnostic, error) {
	if input.Files == nil {
		return nil, fmt.Errorf("toolboxapi: files are required")
	}
	if input.Entry == "" {
		return nil, fmt.Errorf("toolboxapi: entry is required")
	}

	currentDirectory := input.CurrentDirectory
	if currentDirectory == "" {
		currentDirectory = "/"
	}

	files, err := rootedFiles(input.Files, currentDirectory)
	if err != nil {
		return nil, err
	}
	ensureDefaultTSConfig(files, currentDirectory)

	entryPath := rootedPath(currentDirectory, input.Entry)
	entryContent, ok := files[entryPath]
	if !ok {
		return nil, fmt.Errorf("toolboxapi: entry %q not found", input.Entry)
	}

	session := project.NewSession(&project.SessionInit{
		BackgroundCtx: ctx,
		FS:            bundled.WrapFS(vfstest.FromMap(files, input.UseCaseSensitiveFileNames)),
		Options: &project.SessionOptions{
			CurrentDirectory:   currentDirectory,
			DefaultLibraryPath: bundled.LibPath(),
			PositionEncoding:   lsproto.PositionEncodingKindUTF8,
			WatchEnabled:       false,
			LoggingEnabled:     false,
		},
	})
	defer session.Close()

	uri := lsproto.DocumentUri("file://" + entryPath)
	session.DidOpenFile(ctx, uri, 1, entryContent, languageKind(entryPath))

	languageService, err := session.GetLanguageService(ctx, uri)
	if err != nil {
		return nil, err
	}

	program := languageService.GetProgram()
	file := program.GetSourceFile(entryPath)
	if file == nil {
		return nil, fmt.Errorf("toolboxapi: source file %q not found in program", entryPath)
	}

	diagnostics := make([]*ast.Diagnostic, 0)
	diagnostics = append(diagnostics, program.GetSyntacticDiagnostics(ctx, file)...)
	diagnostics = append(diagnostics, program.GetSemanticDiagnostics(ctx, file)...)

	out := make([]Diagnostic, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		item := Diagnostic{
			Message: diagnostic.Localize(locale.Default),
			Code:    diagnostic.Code(),
			Pos:     diagnostic.Pos(),
			End:     diagnostic.End(),
		}
		if diagnostic.File() != nil {
			item.File = diagnostic.File().FileName()
		}
		out = append(out, item)
	}

	return out, nil
}

func rootedFiles(input fs.FS, currentDirectory string) (map[string]string, error) {
	out := map[string]string{}
	err := fs.WalkDir(input, ".", func(file string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || file == "." {
			return nil
		}

		content, err := fs.ReadFile(input, file)
		if err != nil {
			return err
		}

		out[rootedPath(currentDirectory, file)] = string(content)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func ensureDefaultTSConfig(files map[string]string, currentDirectory string) {
	configPath := rootedPath(currentDirectory, "tsconfig.json")
	if _, ok := files[configPath]; ok {
		return
	}

	files[configPath] = `{
  "compilerOptions": {
    "strict": true,
    "module": "esnext",
    "target": "esnext",
    "moduleResolution": "bundler",
    "allowImportingTsExtensions": true
  }
}`
}

func rootedPath(currentDirectory string, file string) string {
	if strings.HasPrefix(file, "/") {
		return path.Clean(file)
	}
	return path.Clean(path.Join(currentDirectory, file))
}

func languageKind(file string) lsproto.LanguageKind {
	switch path.Ext(file) {
	case ".js", ".mjs", ".cjs":
		return lsproto.LanguageKindJavaScript
	case ".jsx":
		return lsproto.LanguageKindJavaScriptReact
	case ".tsx":
		return lsproto.LanguageKindTypeScriptReact
	default:
		return lsproto.LanguageKindTypeScript
	}
}
