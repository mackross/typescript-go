package toolbox

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/microsoft/typescript-go/internal/bundled"
	"github.com/microsoft/typescript-go/internal/compiler"
	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/tsoptions"
	"github.com/microsoft/typescript-go/internal/vfs/vfstest"
)

type Options struct {
	Ref                bool
	AliasRef           bool
	TopRef             bool
	Required           bool
	NoExtraProps       bool
	ExcludePrivate     bool
	UniqueNames        bool
	RejectDateType     bool
	TypeOfKeyword      bool
	DefaultNumberType  string
	ID                 string
	ConstAsEnum        bool
	ValidationKeywords map[string]bool
}

func DefaultOptions() Options {
	return Options{
		Ref:                true,
		DefaultNumberType:  "number",
		ValidationKeywords: map[string]bool{},
	}
}

type Fixture struct {
	Name        string
	Path        string
	Spec        FixtureSpec
	SchemaFiles []string
}

type FixtureSpec struct {
	Root        string
	Options     Options
	Compiler    FixtureCompilerOptions
	UseConfig   bool
	ExpectError string
}

type FixtureCompilerOptions struct {
	StrictNullChecks bool
}

type Result struct {
	Schemas map[string]any
}

var fixtureSpecs = buildFixtureSpecs()

func buildFixtureSpecs() map[string]FixtureSpec {
	specs := map[string]FixtureSpec{}
	addDefaults := func(root string, names ...string) {
		for _, name := range names {
			specs[name] = defaultFixtureSpec(root)
		}
	}

	addDefaults("MyObject",
		"abstract-extends",
		"annotation-default",
		"annotation-id",
		"annotation-items",
		"annotation-ref",
		"annotation-required",
		"annotation-title",
		"any-unknown",
		"array-and-description",
		"class-extends",
		"class-single",
		"comments",
		"comments-comment",
		"comments-from-lib",
		"comments-inline-tags",
		"comments-override",
		"default-properties",
		"dates",
		"enums-mixed",
		"enums-number",
		"enums-string",
		"enums-value-in-interface",
		"extra-properties",
		"force-type",
		"force-type-imported",
		"generic-anonymous",
		"generic-arrays",
		"generic-hell",
		"generic-multiargs",
		"generic-multiple",
		"generic-simple",
		"ignored-required",
		"imports",
		"interface-extra-props",
		"interface-extends",
		"interface-multi",
		"interface-single",
		"map-types",
		"module-interface-single",
		"optionals",
		"private-members",
		"prop-override",
		"strict-null-checks",
		"string-literals",
		"string-literals-inline",
		"string-template-literal",
		"symbol",
		"type-alias-or",
		"type-alias-schema-override",
		"type-anonymous",
		"type-function",
		"type-intersection",
		"type-nullable",
		"type-primitives",
		"type-union",
		"type-union-strict-null-keep-description",
		"typeof-keyword",
		"undefined-property",
		"user-validation-keywords",
	)
	addDefaults("AbstractBase", "abstract-class")
	addDefaults("MyReadOnlyArray", "array-readonly")
	addDefaults("MyEmptyArray", "array-empty")
	addDefaults("MyArray", "array-types", "type-aliases-multitype-array")
	addDefaults("Ext.Foo", "builtin-names")
	addDefaults("Object", "const-keyword")
	addDefaults("foo.Bar", "custom-dates")
	addDefaults("Enum", "enums-compiled-compute", "enums-number-initialized")
	addDefaults("MyObject", "generic-recursive", "interface-recursion", "type-aliases", "type-aliases-partial", "type-aliases-local-namsepace", "type-aliases-local-namespace", "type-aliases-recursive-export", "type-aliases-recursive-object-topref", "type-aliases-mixed", "type-aliases-anonymous", "type-literals")
	addDefaults("MyTuple", "type-aliases-tuple", "type-aliases-tuple-of-variable-length", "type-aliases-tuple-with-names", "type-aliases-tuple-with-rest-element")
	addDefaults("MyObject", "const-as-enum")
	addDefaults("MyObjectFromAbstract", "abstract-extends")
	addDefaults("Main", "key-in-key-of-single", "key-in-key-of-multi", "key-in-key-of-multi-underscores")
	addDefaults("Def", "module-interface-deep")
	addDefaults("Type", "namespace")
	addDefaults("RootNamespace.Def", "namespace-deep-1")
	addDefaults("RootNamespace.SubNamespace.HelperA", "namespace-deep-2")
	addDefaults("Never", "never")
	addDefaults("NumericKeysAndOthers", "numeric-keys-and-others")
	addDefaults("IndexInterface", "object-numeric-index")
	addDefaults("Target", "object-numeric-index-as-property")
	addDefaults("MyDerived", "optionals-derived")
	addDefaults("Specific", "satisfies-keyword")
	addDefaults("MyAlias", "type-aliases-alias-ref", "type-aliases-alias-ref-topref", "type-aliases-object", "type-aliases-recursive-anonymous", "type-aliases-recursive-alias-topref", "type-no-aliases-recursive-topref")
	addDefaults("MyString", "type-alias-single", "type-alias-single-annotated", "type-aliases-primitive")
	addDefaults("MyFixedSizeArray", "type-aliases-fixed-size-array")
	addDefaults("MyUnion", "type-aliases-union")
	addDefaults("MyModel", "type-aliases-union-namespace")
	addDefaults("MyMappedType", "type-mapped-types")
	addDefaults("MyUndefined", "type-alias-undefined")
	addDefaults("MyNever", "type-alias-never")
	addDefaults("Foo", "type-intersection-recursive")
	addDefaults("MyLinkedList", "type-intersection-recursive-no-additional")
	addDefaults("Shape", "type-union-tagged")
	addDefaults("TestChildren", "type-recursive")
	addDefaults("Test", "type-globalThis")
	addDefaults("*", "generate-all-types", "type-default-number-as-integer", "user-symbols")

	// type-function: expected has required but no additionalProperties
	spec := FixtureSpec{
		Root:    "MyObject",
		Options: DefaultOptions(),
	}
	spec.Options.Required = true
	specs["type-function"] = spec

	// generic-hell: expected has required but no additionalProperties
	spec = FixtureSpec{
		Root:    "MyObject",
		Options: DefaultOptions(),
	}
	spec.Options.Required = true
	specs["generic-hell"] = spec

	// interface-extra-props: expected wraps in $ref with string index sig
	spec = defaultFixtureSpec("MyObject")
	spec.Options.TopRef = true
	spec.Options.NoExtraProps = false
	specs["interface-extra-props"] = spec

	spec = defaultFixtureSpec("MyObject")
	spec.Options.ID = "someSchemaId"
	specs["argument-id"] = spec

	spec = defaultFixtureSpec("MyObject")
	spec.Options.ValidationKeywords = validationKeywords("hide")
	specs["annotation-tjs"] = spec

	spec = defaultFixtureSpec("MyObject")
	spec.Options.ConstAsEnum = true
	specs["const-as-enum"] = spec

	spec = defaultFixtureSpec("MyObject")
	spec.Options.TopRef = true
	specs["generic-recursive"] = spec

	spec = defaultFixtureSpec("MyObject")
	spec.Options.TopRef = true
	specs["interface-recursion"] = spec

	spec = defaultFixtureSpec("MyModule")
	spec.Options.Ref = false
	spec.Options.AliasRef = false
	spec.Options.TopRef = false
	specs["no-ref"] = spec

	spec = defaultFixtureSpec("Target")
	spec.Options.Required = false
	specs["object-numeric-index-as-property"] = spec

	spec = defaultFixtureSpec("MyObject")
	spec.Options.ExcludePrivate = true
	specs["private-members"] = spec

	spec = defaultFixtureSpec("MyObject")
	spec.Compiler.StrictNullChecks = true
	specs["strict-null-checks"] = spec

	spec = defaultFixtureSpec("MyObject")
	spec.Options.AliasRef = true
	specs["comments-imports"] = spec

	spec = defaultFixtureSpec("MyObject")
	spec.Options.AliasRef = true
	specs["type-aliases"] = spec

	spec = defaultFixtureSpec("MyObject")
	spec.Options.AliasRef = true
	spec.Options.TopRef = true
	specs["type-aliases-anonymous"] = spec

	spec = defaultFixtureSpec("MyObject")
	spec.Options.AliasRef = true
	spec.Compiler.StrictNullChecks = true
	specs["type-aliases-local-namsepace"] = spec

	spec = defaultFixtureSpec("MyObject")
	spec.Options.AliasRef = true
	specs["type-aliases-partial"] = spec

	spec = defaultFixtureSpec("MyAlias")
	spec.Options.AliasRef = true
	specs["type-aliases-alias-ref"] = spec

	spec = FixtureSpec{
		Root:    "MyAlias",
		Options: DefaultOptions(),
	}
	spec.Options.Required = true
	spec.Options.AliasRef = true
	spec.Options.TopRef = true
	specs["type-aliases-alias-ref-topref"] = spec

	spec = defaultFixtureSpec("MyObject")
	spec.Options.AliasRef = true
	spec.Options.TopRef = true
	specs["type-aliases-local-namespace"] = spec
	specs["type-aliases-mixed"] = spec
	specs["type-aliases-recursive-export"] = spec
	specs["type-aliases-recursive-object-topref"] = spec

	spec = defaultFixtureSpec("MyTuple")
	spec.Options.AliasRef = true
	spec.Options.TopRef = true
	specs["type-aliases-tuple"] = spec

	spec = defaultFixtureSpec("MyAlias")
	spec.Options.AliasRef = true
	spec.Options.TopRef = true
	specs["type-aliases-object"] = spec
	specs["type-aliases-recursive-anonymous"] = spec

	spec = defaultFixtureSpec("MyString")
	spec.Options.AliasRef = true
	spec.Options.TopRef = true
	specs["type-aliases-primitive"] = spec

	spec = defaultFixtureSpec("MyUnion")
	spec.Options.AliasRef = true
	spec.Options.TopRef = true
	specs["type-aliases-union"] = spec

	spec = defaultFixtureSpec("MyAlias")
	spec.Options.TopRef = true
	specs["type-no-aliases-recursive-topref"] = spec

	spec = FixtureSpec{
		Root:    "MyAlias",
		Options: DefaultOptions(),
	}
	spec.Options.Required = true
	spec.Options.AliasRef = true
	spec.Options.TopRef = true
	specs["type-aliases-recursive-alias-topref"] = spec

	spec = defaultFixtureSpec("MyObject")
	spec.Compiler.StrictNullChecks = true
	specs["type-union-strict-null-keep-description"] = spec

	spec = defaultFixtureSpec("RootNamespace.Def")
	spec.Options.TopRef = true
	specs["namespace-deep-1"] = spec

	spec = defaultFixtureSpec("RootNamespace.SubNamespace.HelperA")
	spec.Options.TopRef = true
	specs["namespace-deep-2"] = spec

	spec = defaultFixtureSpec("Foo")
	spec.Options.TopRef = true
	specs["type-intersection-recursive"] = spec

	spec = defaultFixtureSpec("MyLinkedList")
	spec.Options.TopRef = true
	specs["type-intersection-recursive-no-additional"] = spec

	spec = defaultFixtureSpec("TestChildren")
	spec.Options.TopRef = true
	specs["type-recursive"] = spec

	spec = defaultFixtureSpec("MyObject")
	spec.Options.TypeOfKeyword = true
	specs["typeof-keyword"] = spec

	spec = defaultFixtureSpec("MyObject")
	spec.Options.UniqueNames = true
	specs["unique-names"] = spec
	specs["unique-names-multiple-subdefinitions"] = spec

	spec = defaultFixtureSpec("MyObject")
	spec.Options.ValidationKeywords = validationKeywords("chance", "important")
	specs["user-validation-keywords"] = spec

	spec = FixtureSpec{
		Root:    "*",
		Options: DefaultOptions(),
	}
	spec.Options.DefaultNumberType = "integer"
	specs["type-default-number-as-integer"] = spec

	spec = FixtureSpec{
		Root:        "*",
		Options:     DefaultOptions(),
		UseConfig:   true,
		ExpectError: "",
	}
	specs["no-unrelated-definitions"] = spec

	spec = FixtureSpec{
		Root:      "",
		Options:   DefaultOptions(),
		UseConfig: true,
	}
	specs["tsconfig"] = spec

	spec = FixtureSpec{
		Root:    "MyObject",
		Options: DefaultOptions(),
	}
	specs["type-alias-schema-override"] = spec

	spec = defaultFixtureSpec("MyNever")
	spec.ExpectError = "Unsupported type: never"
	specs["type-alias-never"] = spec

	spec = defaultFixtureSpec("MyUndefined")
	spec.ExpectError = "Not supported: root type undefined"
	specs["type-alias-undefined"] = spec

	return specs
}

func defaultFixtureSpec(root string) FixtureSpec {
	return FixtureSpec{
		Root:    root,
		Options: defaultFixtureOptions(),
	}
}

func defaultFixtureOptions() Options {
	opts := DefaultOptions()
	opts.Required = true
	opts.NoExtraProps = true
	return opts
}

func validationKeywords(names ...string) map[string]bool {
	keywords := map[string]bool{}
	for _, name := range names {
		keywords[name] = true
	}
	return keywords
}

func DiscoverFixtures(root string) ([]Fixture, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(absRoot)
	if err != nil {
		return nil, err
	}

	var fixtures []Fixture
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(absRoot, entry.Name())
		hasSchema := false
		schemaFiles := make([]string, 0)
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			if strings.HasPrefix(filepath.Base(p), "schema") && strings.HasSuffix(p, ".json") {
				hasSchema = true
				schemaFiles = append(schemaFiles, filepath.Base(p))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if _, ok := os.Stat(filepath.Join(dir, "tsconfig.json")); ok == nil {
			hasSchema = true
		}
		if !hasSchema {
			continue
		}
		sort.Strings(schemaFiles)
		spec, ok := fixtureSpecs[entry.Name()]
		if !ok {
			return nil, fmt.Errorf("fixture %q missing harness spec", entry.Name())
		}
		fixtures = append(fixtures, Fixture{
			Name:        entry.Name(),
			Path:        dir,
			Spec:        spec,
			SchemaFiles: schemaFiles,
		})
	}

	sort.Slice(fixtures, func(i, j int) bool { return fixtures[i].Name < fixtures[j].Name })
	return fixtures, nil
}

func BuildProgram(ctx context.Context, fixture Fixture) (*compiler.Program, error) {
	_ = ctx
	return buildProgramForFixture(fixture)
}

func GenerateFixture(ctx context.Context, fixture Fixture) (*Result, error) {
	program, err := buildProgramForFixture(fixture)
	if err != nil {
		return nil, err
	}

	gen, err := NewGenerator(program, fixture.Spec.Options)
	if err != nil {
		return nil, err
	}

	out := &Result{Schemas: map[string]any{}}
	root := fixture.Spec.Root
	if root == "" && len(fixture.SchemaFiles) == 1 && fixture.SchemaFiles[0] == "schema.json" {
		symbols := gen.collectTopLevelSymbols()
		if len(symbols) == 0 {
			return nil, fmt.Errorf("fixture %q missing root type metadata", fixture.Name)
		}
		root = symbols[len(symbols)-1].Name
	}
	for _, schemaFile := range fixture.SchemaFiles {
		switch schemaFile {
		case "schema.program.json":
			schemas, err := gen.GenerateProgramSchema()
			if err != nil {
				return nil, err
			}
			out.Schemas[schemaFile] = schemas
		case "schema.json":
			schema, err := gen.GenerateSchemaForName(ctx, root)
			if err != nil {
				return nil, err
			}
			out.Schemas[schemaFile] = schema
		default:
			root := strings.TrimSuffix(strings.TrimPrefix(schemaFile, "schema."), ".json")
			if fixture.Spec.Options.UniqueNames {
				parts := strings.Split(root, ".")
				if len(parts) > 1 {
					if len(parts[len(parts)-1]) == 8 {
						root = strings.Join(parts[:len(parts)-1], ".")
					}
				}
			}
			schema, err := gen.GenerateSchemaForName(ctx, root)
			if err != nil {
				return nil, err
			}
			out.Schemas[schemaFile] = schema
		}
	}

	if len(out.Schemas) == 0 && fixture.Spec.Root != "" {
		schema, err := gen.GenerateSchemaForName(ctx, fixture.Spec.Root)
		if err != nil {
			return nil, err
		}
		out.Schemas["schema.json"] = schema
	}

	return out, nil
}

func buildProgramForFixture(fixture Fixture) (*compiler.Program, error) {
	files, err := readFixtureFiles(fixture.Path)
	if err != nil {
		return nil, err
	}

	cwd := fixture.Path
	fs := bundled.WrapFS(vfstest.FromMap(files, true))
	host := compiler.NewCompilerHost(cwd, fs, bundled.LibPath(), nil, nil)

	configPath := filepath.Join(cwd, "tsconfig.json")
	if _, ok := files[configPath]; !ok && !fixture.Spec.UseConfig {
		sourceFiles := sourceFilesForConfig(files, cwd)
		files[configPath] = syntheticTSConfig(sourceFiles, fixture.Spec.Compiler)
		fs = bundled.WrapFS(vfstest.FromMap(files, true))
		host = compiler.NewCompilerHost(cwd, fs, bundled.LibPath(), nil, nil)
	}

	parsed, diags := tsoptions.GetParsedCommandLineOfConfigFile(configPath, &core.CompilerOptions{}, nil, host, nil)
	if len(diags) > 0 {
		return nil, fmt.Errorf("parse tsconfig: %v", diags[0])
	}
	if parsed == nil {
		return nil, fmt.Errorf("parse tsconfig: no parsed command line for %s", configPath)
	}

	program := compiler.NewProgram(compiler.ProgramOptions{
		Host:   host,
		Config: parsed,
	})
	return program, nil
}

func readFixtureFiles(dir string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[path] = string(content)
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Scan .ts files for imports that reference sibling fixture directories
	// (e.g. `from "../abstract-class/main"`) and include those files too.
	readExtraDirs(dir, out)
	return out, nil
}

// readExtraDirs scans .ts files for cross-directory imports and adds
// referenced sibling fixture directories to the file map.
func readExtraDirs(dir string, files map[string]string) {
	seen := map[string]bool{}
	for path, content := range files {
		if !strings.HasSuffix(path, ".ts") {
			continue
		}
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if !strings.Contains(line, "from") || !strings.Contains(line, "..") {
				continue
			}
			// Match: import ... from "..." or import ... from '...'
			idx := strings.Index(line, "from")
			if idx < 0 {
				continue
			}
			rest := strings.TrimSpace(line[idx+4:])
			if len(rest) < 2 {
				continue
			}
			q := rest[0]
			if q != '\'' && q != '"' {
				continue
			}
			end := strings.IndexByte(rest[1:], q)
			if end < 0 {
				continue
			}
			importPath := rest[1 : end+1]
			if !strings.Contains(importPath, "..") {
				continue
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(path), importPath))
			// Try with .ts extension
			candidates := []string{resolved + ".ts", resolved + ".tsx", resolved + "/index.ts"}
			for _, candidate := range candidates {
				candidateDir := filepath.Dir(candidate)
				if seen[candidateDir] {
					continue
				}
				if _, err := os.Stat(candidate); err == nil {
					seen[candidateDir] = true
					_ = filepath.WalkDir(candidateDir, func(p string, d fs.DirEntry, walkErr error) error {
						if walkErr != nil || d.IsDir() {
							return walkErr
						}
						if _, exists := files[p]; !exists {
							if content, err := os.ReadFile(p); err == nil {
								files[p] = string(content)
							}
						}
						return nil
					})
				}
			}
		}
	}
}

func sourceFilesForConfig(files map[string]string, cwd string) []string {
	var out []string
	for file := range files {
		if strings.HasSuffix(file, ".ts") || strings.HasSuffix(file, ".tsx") || strings.HasSuffix(file, ".mts") || strings.HasSuffix(file, ".cts") || strings.HasSuffix(file, ".d.ts") {
			if filepath.Base(file) == "schema.json" || strings.HasPrefix(filepath.Base(file), "schema.") {
				continue
			}
			rel, err := filepath.Rel(cwd, file)
			if err != nil {
				continue
			}
			out = append(out, filepath.ToSlash(rel))
		}
	}
	sort.Strings(out)
	return out
}

func syntheticTSConfig(files []string, opts FixtureCompilerOptions) string {
	b := strings.Builder{}
	b.WriteString("{\n")
	b.WriteString("  \"compilerOptions\": {\n")
	b.WriteString("    \"noEmit\": true,\n")
	b.WriteString("    \"emitDecoratorMetadata\": true,\n")
	b.WriteString("    \"experimentalDecorators\": true,\n")
	b.WriteString("    \"target\": \"es5\",\n")
	b.WriteString("    \"module\": \"commonjs\",\n")
	b.WriteString("    \"allowUnusedLabels\": true")
	if opts.StrictNullChecks {
		b.WriteString(",\n")
		b.WriteString("    \"strictNullChecks\": true\n")
	} else {
		b.WriteString("\n")
	}
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
