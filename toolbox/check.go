package toolbox

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"sync/atomic"

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

// CheckSession holds a reusable LSP session for incremental type-checking.
// Create one via Check with a nil session, then pass it back on subsequent
// calls to reuse the parsed program state. Call Close when done.
type CheckSession struct {
	session          *project.Session
	entryURI         lsproto.DocumentUri
	entryPath        string
	currentDirectory string
	version          atomic.Int32
}

func (cs *CheckSession) Close() error {
	if cs.session != nil {
		cs.session.Close()
	}
	return nil
}

// Verify CheckSession implements io.Closer.
var _ io.Closer = (*CheckSession)(nil)

// Check type-checks the entry file. If session is nil a new one is created.
// The returned CheckSession can be passed to subsequent calls to avoid
// re-parsing unchanged source files. The caller must Close the session.
func Check(ctx context.Context, input CheckInput, session *CheckSession) ([]Diagnostic, *CheckSession, error) {
	if input.Files == nil {
		return nil, session, fmt.Errorf("toolbox: files are required")
	}
	if input.Entry == "" {
		return nil, session, fmt.Errorf("toolbox: entry is required")
	}

	currentDirectory := input.CurrentDirectory
	if currentDirectory == "" {
		currentDirectory = "/"
	}

	entryPath := rootedPath(currentDirectory, input.Entry)

	if session == nil {
		s, err := newCheckSession(ctx, input, currentDirectory, entryPath)
		if err != nil {
			return nil, nil, err
		}
		session = s
	} else {
		files, err := rootedFiles(input.Files, currentDirectory)
		if err != nil {
			return nil, session, err
		}
		entryContent, ok := files[entryPath]
		if !ok {
			return nil, session, fmt.Errorf("toolbox: entry %q not found", input.Entry)
		}
		version := session.version.Add(1)
		session.session.DidChangeFile(ctx, session.entryURI, version, []lsproto.TextDocumentContentChangePartialOrWholeDocument{
			{WholeDocument: &lsproto.TextDocumentContentChangeWholeDocument{Text: entryContent}},
		})
	}

	diags, err := getDiagnostics(ctx, session)
	if err != nil {
		return nil, session, err
	}
	return diags, session, nil
}

func newCheckSession(ctx context.Context, input CheckInput, currentDirectory, entryPath string) (*CheckSession, error) {
	files, err := rootedFiles(input.Files, currentDirectory)
	if err != nil {
		return nil, err
	}
	ensureDefaultTSConfig(files, currentDirectory)

	entryContent, ok := files[entryPath]
	if !ok {
		return nil, fmt.Errorf("toolbox: entry %q not found", input.Entry)
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

	uri := lsproto.DocumentUri("file://" + entryPath)
	session.DidOpenFile(ctx, uri, 1, entryContent, languageKind(entryPath))

	cs := &CheckSession{
		session:          session,
		entryURI:         uri,
		entryPath:        entryPath,
		currentDirectory: currentDirectory,
	}
	cs.version.Store(1)
	return cs, nil
}

func getDiagnostics(ctx context.Context, cs *CheckSession) ([]Diagnostic, error) {
	languageService, err := cs.session.GetLanguageService(ctx, cs.entryURI)
	if err != nil {
		return nil, err
	}

	program := languageService.GetProgram()
	file := program.GetSourceFile(cs.entryPath)
	if file == nil {
		return nil, fmt.Errorf("toolbox: source file %q not found in program", cs.entryPath)
	}

	diagnostics := make([]*ast.Diagnostic, 0)
	diagnostics = append(diagnostics, program.GetSyntacticDiagnostics(ctx, file)...)
	diagnostics = append(diagnostics, program.GetSemanticDiagnostics(ctx, file)...)

	out := make([]Diagnostic, 0, len(diagnostics))
	for _, d := range diagnostics {
		item := Diagnostic{
			Message: d.Localize(locale.Default),
			Code:    d.Code(),
			Pos:     d.Pos(),
			End:     d.End(),
		}
		if d.File() != nil {
			item.File = d.File().FileName()
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
