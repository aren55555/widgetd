// Command widgetc builds widgets. A widget is a folder, or the same folder
// written as one .widget file (see single.go); every command takes either,
// and treats them the same.
//
//	<widget>/
//	  widget.toml          manifest: name, version, title, icon, config message, network request
//	  buf.yaml, buf.lock   the schema's buf module (proto/ plus its deps); buf.lock
//	                       is written by `buf dep update` if missing
//	  proto/...            the schema: State, Delta, Config
//	  server/              the server half: a Go package exporting Run
//	  view/view.ts         the view half: calls defineWidget
//	  view/view.css        optional
//	  view/plugins.ts      optional: Bun build plugins, `export default [...]`
//	  view/package.json    optional: the view's own dependencies (bun install)
//
//	widgetc build <dir | x.widget>...   build
//	widgetc test  <dir | x.widget>...   generate, then run the widget's tests
//	widgetc split <x.widget> <dir>      write a .widget out as a folder
//	widgetc join  <dir> <x.widget>      write a folder as one .widget
//
// `widgetc build <dir>` generates the schema's Go code into <dir>/gen and its
// TypeScript into <dir>/view/gen (from the same .proto, so the two halves
// can't disagree), bundles the view into <dir>/dist (see buildview.ts: one
// classic script, NODE_ENV=production, the view's plugins applied), and writes
// <dir>/widget_gen.go, which exposes the whole widget as a widget.Spec.
// It also compiles the server half for the sandbox (GOOS=wasip1) and writes
// <dir>/<name>.wpkg: schema, server.wasm and view in one file, which is
// what a host loads; widget_gen.go just embeds it. A .widget builds through
// a scratch folder, and gets <name>.wpkg and <name>_widget_gen.go beside it.
//
// widget.toml's `icon` names a Lucide icon ("cloud-sun"; https://lucide.dev),
// looked up in the view's own @widgetd/sdk/icons the way <wd-icon> finds
// one. Its shapes go into the package, and widgetd's pages draw it beside
// the widget's title.
//
// `widgetc test` runs `go test ./...` if the widget has Go tests, and
// `bun test` in view/ if it has script tests.
//
// Every feature must work the same in both forms; TestParity checks that
// converting the examples loses nothing and builds byte-identical packages.
package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/BurntSushi/toml"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/aren55555/widgetd/host"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

type manifest struct {
	Name    string `toml:"name"`
	Version string `toml:"version"`
	Title   string `toml:"title"`
	// Icon is a Lucide icon's name ("cloud-sun"), drawn beside the title on
	// widgetd's own pages.
	Icon    string   `toml:"icon"`
	Config  string   `toml:"config"`
	Secrets []string `toml:"secrets"`
	Network struct {
		Hosts   []string `toml:"hosts"`
		Methods []string `toml:"methods"`
		Why     string   `toml:"why"`
	} `toml:"network"`
	Uses []widget.Use `toml:"uses"`
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

const usage = `usage:
  widgetc build <dir | x.widget>...   build widgets
  widgetc test  <dir | x.widget>...   run widgets' tests
  widgetc split <x.widget> <dir>      write a .widget out as a folder
  widgetc join  <dir> <x.widget>      write a folder as one .widget`

func main() {
	flag.Usage = func() { fmt.Fprintln(os.Stderr, usage) }
	flag.Parse()
	args := flag.Args()
	if len(args) < 2 {
		flag.Usage()
		os.Exit(2)
	}
	fail := func(what string, err error) {
		fmt.Fprintf(os.Stderr, "widgetc: %s: %v\n", what, err)
		os.Exit(1)
	}
	switch args[0] {
	case "build", "test":
		for _, w := range args[1:] {
			var err error
			if args[0] == "build" {
				err = buildAny(w)
			} else {
				err = testAny(w)
			}
			if err != nil {
				fail(w, err)
			}
		}
	case "split", "join":
		if len(args) != 3 {
			flag.Usage()
			os.Exit(2)
		}
		var err error
		if args[0] == "split" {
			err = split(args[1], args[2])
		} else {
			err = join(args[1], args[2])
		}
		if err != nil {
			fail(args[1], err)
		}
	default:
		flag.Usage()
		os.Exit(2)
	}
}

func isWidgetFile(p string) bool { return strings.HasSuffix(p, ".widget") }

// buildAny builds a widget folder or a .widget.
func buildAny(w string) error {
	if isWidgetFile(w) {
		return buildSingleFile(w, true)
	}
	dir, err := filepath.Abs(w)
	if err != nil {
		return err
	}
	m, err := build(dir)
	if err != nil {
		return err
	}
	return writeSpec(dir, m)
}

// testAny runs a widget folder's or a .widget's tests.
func testAny(w string) error {
	dir := w
	if isWidgetFile(w) {
		var err error
		if dir, err = prepareSingleFile(w, true); err != nil {
			return err
		}
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if _, _, err := prepare(dir); err != nil {
		return err
	}
	return runTests(dir)
}

// prepare checks a widget folder, fetches what it depends on, and generates
// its code: everything a build or a test needs first.
func prepare(dir string) (manifest, string, error) {
	var m manifest
	if err := readManifest(dir, &m); err != nil {
		return m, "", err
	}
	if !nameRE.MatchString(m.Name) {
		return m, "", fmt.Errorf("widget.toml: name %q: want lowercase letters, digits and dashes", m.Name)
	}
	if m.Version == "" {
		return m, "", errors.New("widget.toml: version is required")
	}
	importPath, err := goImportPath(dir)
	if err != nil {
		return m, "", err
	}
	protos, err := protoFiles(filepath.Join(dir, "proto"))
	if err != nil {
		return m, "", err
	}
	if len(protos) == 0 {
		return m, "", errors.New("no .proto files under proto/")
	}
	if _, err := os.Stat(filepath.Join(dir, "buf.lock")); errors.Is(err, fs.ErrNotExist) {
		if err := run(dir, "buf", "dep", "update"); err != nil {
			return m, "", err
		}
	}
	// The view's own dependencies. In a bun workspace this installs the
	// workspace, which is a no-op when it's up to date.
	if _, err := os.Stat(filepath.Join(dir, "view", "package.json")); err == nil {
		if err := run(filepath.Join(dir, "view"), "bun", "install"); err != nil {
			return m, "", err
		}
	}
	if err := generate(dir, importPath); err != nil {
		return m, "", err
	}
	return m, importPath, nil
}

// build builds a widget folder into <dir>/<name>.wpkg.
func build(dir string) (manifest, error) {
	m, importPath, err := prepare(dir)
	if err != nil {
		return m, err
	}
	protos, err := protoFiles(filepath.Join(dir, "proto"))
	if err != nil {
		return m, err
	}
	icon, err := bundleView(dir, m.Icon)
	if err != nil {
		return m, err
	}
	return m, pack(dir, importPath, m, icon, protos)
}

// runTests runs the Go tests anywhere in a widget folder, and the script
// tests in its view.
func runTests(dir string) error {
	srcs, err := readSources(dir)
	if err != nil {
		return err
	}
	var goTests, viewTests bool
	for p := range srcs {
		goTests = goTests || strings.HasSuffix(p, "_test.go")
		viewTests = viewTests || (strings.HasPrefix(p, "view/") && isScript(p) && strings.Contains(path.Base(p), ".test."))
	}
	if goTests {
		if err := run(dir, "go", "test", "./..."); err != nil {
			return err
		}
	}
	if viewTests {
		return run(filepath.Join(dir, "view"), "bun", "test")
	}
	return nil
}

// goImportPath is the Go import path of dir, from the enclosing go.mod.
func goImportPath(dir string) (string, error) {
	for d := dir; ; d = filepath.Dir(d) {
		b, err := os.ReadFile(filepath.Join(d, "go.mod"))
		if err == nil {
			mod := regexp.MustCompile(`(?m)^module\s+(\S+)`).FindSubmatch(b)
			if mod == nil {
				return "", fmt.Errorf("%s/go.mod has no module line", d)
			}
			rel, err := filepath.Rel(d, dir)
			if err != nil {
				return "", err
			}
			return strings.TrimSuffix(string(mod[1])+"/"+filepath.ToSlash(rel), "/."), nil
		}
		if filepath.Dir(d) == d {
			return "", errors.New("not inside a Go module")
		}
	}
}

func protoFiles(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".proto") {
			rel, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// generate runs buf with a template built here, so a widget folder carries
// no generation config of its own: Go goes to gen/ (as package <import>/gen/...),
// TypeScript to view/gen/ with buf/validate included so its imports resolve.
func generate(dir, importPath string) error {
	// The view's own protoc-gen-es if it has one, else whatever bun fetches.
	esPlugin := []string{"bun", "x", "--package", "@bufbuild/protoc-gen-es", "protoc-gen-es"}
	if es, err := findUp(filepath.Join(dir, "view"), "node_modules/.bin/protoc-gen-es"); err == nil {
		esPlugin = []string{"bun", es}
	}
	tmpl := map[string]any{
		"version": "v2",
		"clean":   true,
		"managed": map[string]any{
			"enabled": true,
			"disable": []map[string]string{{"module": "buf.build/bufbuild/protovalidate"}},
			"override": []map[string]string{
				{"file_option": "go_package_prefix", "value": importPath + "/gen"},
			},
		},
		"plugins": []map[string]any{
			{"local": []string{"go", "run", "google.golang.org/protobuf/cmd/protoc-gen-go"}, "out": "gen", "opt": "paths=source_relative"},
			{"local": esPlugin, "out": "view/gen", "include_imports": true, "opt": []string{"target=ts"}},
		},
	}
	b, _ := json.Marshal(tmpl)
	return run(dir, "buf", "generate", "--template", string(b))
}

func findUp(dir, rel string) (string, error) {
	for d := dir; ; d = filepath.Dir(d) {
		p := filepath.Join(d, rel)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		if filepath.Dir(d) == d {
			return "", fs.ErrNotExist
		}
	}
}

// buildViewScript bundles a view; see buildview.ts.
//
//go:embed buildview.ts
var buildViewScript []byte

// iconNameRE is the form of a Lucide icon name, as widget.toml's `icon`.
var iconNameRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// bundleView bundles view/view.ts (and copies view/view.css) into dist/, and
// resolves the manifest's icon, if any, from the view's @widgetd/sdk/icons.
// The bundle is a classic script: the card frame runs in an opaque origin,
// where a module script would need CORS.
func bundleView(dir, iconName string) (*widget.Icon, error) {
	if iconName != "" && !iconNameRE.MatchString(iconName) {
		return nil, fmt.Errorf("widget.toml: icon %q: want a Lucide name like \"cloud-sun\"", iconName)
	}
	dist := filepath.Join(dir, "dist")
	if err := os.RemoveAll(dist); err != nil {
		return nil, err
	}
	work, err := os.MkdirTemp("", "widgetc-view")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	script := filepath.Join(work, "buildview.ts")
	if err := os.WriteFile(script, buildViewScript, 0o644); err != nil {
		return nil, err
	}
	iconJSON := filepath.Join(work, "icon.json")
	if err := run(dir, "bun", script, filepath.Join(dir, "view"), dist, iconJSON, iconName); err != nil {
		return nil, err
	}
	var icon *widget.Icon
	if iconName != "" {
		b, err := os.ReadFile(iconJSON)
		if err != nil {
			return nil, err
		}
		icon = new(widget.Icon)
		if err := json.Unmarshal(b, icon); err != nil {
			return nil, fmt.Errorf("icon %s: %w", iconName, err)
		}
	}
	css, err := os.ReadFile(filepath.Join(dir, "view", "view.css"))
	if errors.Is(err, fs.ErrNotExist) {
		return icon, nil
	}
	if err != nil {
		return nil, err
	}
	return icon, os.WriteFile(filepath.Join(dist, "view.css"), css, 0o644)
}

// mainTmpl is the sandboxed server half's entry point: the widget's own
// server package, run under the SDK's guest Host.
var mainTmpl = template.Must(template.New("main").Parse(`// Code generated by widgetc. DO NOT EDIT.

//go:build wasip1

package main

import (
	"github.com/aren55555/widgetd/sdk/go/widget"

	"{{.ImportPath}}/server"
)

func main() {
	widget.Main(server.Run{{if .Choices}}, widget.WithChoices(server.Choices){{end}}{{if .Verify}}, widget.WithVerify(server.Verify){{end}})
}
`))

// pack builds server.wasm and the schema, and writes <name>.wpkg: the one
// file a host needs to run the widget.
func pack(dir, importPath string, m manifest, icon *widget.Icon, protos []string) error {
	work, err := os.MkdirTemp("", "widgetc")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	mainDir := filepath.Join(dir, "wasm")
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		return err
	}
	var src bytes.Buffer
	choices, err := exportsFunc(filepath.Join(dir, "server"), "Choices")
	if err != nil {
		return err
	}
	verify, err := exportsFunc(filepath.Join(dir, "server"), "Verify")
	if err != nil {
		return err
	}
	var modes []string
	if choices {
		modes = append(modes, "choices")
	}
	if verify {
		modes = append(modes, "verify")
	}
	if err := mainTmpl.Execute(&src, map[string]any{"ImportPath": importPath, "Choices": choices, "Verify": verify}); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(mainDir, "main_gen.go"), src.Bytes(), 0o644); err != nil {
		return err
	}
	wasmPath := filepath.Join(work, "server.wasm")
	build := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "-o", wasmPath, "./wasm")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("build server.wasm: %w", err)
	}
	wasm, err := os.ReadFile(wasmPath)
	if err != nil {
		return err
	}

	// The schema as buf compiles it, imports included: exactly what both
	// halves were generated from.
	imagePath := filepath.Join(work, "schema.binpb")
	if err := run(dir, "buf", "build", "-o", imagePath); err != nil {
		return err
	}
	raw, err := os.ReadFile(imagePath)
	if err != nil {
		return err
	}
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(raw, &set); err != nil {
		return err
	}
	reg, err := protodesc.NewFiles(&set)
	if err != nil {
		return err
	}
	var own []protoreflect.FileDescriptor
	for _, p := range protos {
		fd, err := reg.FindFileByPath(p)
		if err != nil {
			return err
		}
		own = append(own, fd)
	}

	out, err := os.Create(filepath.Join(dir, m.Name+".wpkg"))
	if err != nil {
		return err
	}
	defer out.Close()
	man := host.Manifest{
		Name: m.Name, Version: m.Version, Title: m.Title, Icon: icon, Config: m.Config, Modes: modes, Secrets: m.Secrets,
		Network: widget.NetworkRequest{Hosts: m.Network.Hosts, Methods: m.Network.Methods, Why: m.Network.Why},
	}
	man.Uses = m.Uses
	if err := host.WritePackage(out, man, own, wasm, os.DirFS(filepath.Join(dir, "dist"))); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	// Prove the package loads before anyone else finds out it doesn't.
	b, err := os.ReadFile(out.Name())
	if err != nil {
		return err
	}
	if _, err := host.LoadPackage(b); err != nil {
		return fmt.Errorf("built package doesn't load: %w", err)
	}
	return nil
}

// specTmpl is the Go embedding of a built widget. A folder is its own
// package, so it exports Package and Widget; .widget files share the
// package of their directory, so theirs carry the widget's name: ClockPackage
// and ClockWidget for clock.widget.
var specTmpl = template.Must(template.New("spec").Parse(`// Code generated by widgetc from {{.From}}. DO NOT EDIT.

package {{.Package}}

import (
	_ "embed"

	"github.com/aren55555/widgetd/host"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

// {{.ID}}Package is the {{.Name}} widget, packaged: its schema, its sandboxed
// server half, and its view.
//
//go:embed {{.Name}}.wpkg
var {{.ID}}Package []byte

// {{.ID}}Widget loads {{.ID}}Package, ready to hand to a widgetd host.
func {{.ID}}Widget() (widget.Spec, error) { return host.LoadPackage({{.ID}}Package) }
`))

// writeSpec writes a folder's widget_gen.go.
func writeSpec(dir string, m manifest) error {
	return writeSpecFile(filepath.Join(dir, "widget_gen.go"), map[string]any{
		"From": "widget.toml", "Package": strings.ReplaceAll(filepath.Base(dir), "-", ""), "Name": m.Name, "ID": "",
	})
}

// writeSingleSpec writes <name>_widget_gen.go beside a .widget, in the
// package of the Go files already there (or one named for the directory,
// as for a folder).
func writeSingleSpec(file string, m manifest) error {
	dir := filepath.Dir(file)
	pkg, err := dirPackage(dir)
	if err != nil {
		return err
	}
	if pkg == "" {
		pkg = strings.ReplaceAll(filepath.Base(dir), "-", "")
	}
	return writeSpecFile(filepath.Join(dir, m.Name+"_widget_gen.go"), map[string]any{
		"From": filepath.Base(file), "Package": pkg, "Name": m.Name, "ID": goName(m.Name),
	})
}

func writeSpecFile(p string, data map[string]any) error {
	var buf bytes.Buffer
	if err := specTmpl.Execute(&buf, data); err != nil {
		return err
	}
	src, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("format %s: %w\n%s", filepath.Base(p), err, buf.Bytes())
	}
	return os.WriteFile(p, src, 0o644)
}

// dirPackage is the package name of the non-test Go files in dir, or "".
func dirPackage(dir string) (string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return "", err
	}
	fset := token.NewFileSet()
	pkg := ""
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, parser.PackageClauseOnly)
		if err != nil {
			return "", err
		}
		switch name := f.Name.Name; {
		case pkg == "":
			pkg = name
		case name != pkg:
			return "", fmt.Errorf("%s: Go files of more than one package", dir)
		}
	}
	return pkg, nil
}

// goName is a widget name as an exported Go identifier: bitcoin-spiral is
// BitcoinSpiral.
func goName(name string) string {
	var b strings.Builder
	for _, part := range strings.Split(name, "-") {
		if part != "" {
			b.WriteString(strings.ToUpper(part[:1]) + part[1:])
		}
	}
	s := b.String()
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		s = "W" + s
	}
	return s
}

func run(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, args[0], err)
	}
	return nil
}

func readManifest(dir string, m *manifest) error {
	if _, err := toml.DecodeFile(filepath.Join(dir, "widget.toml"), m); err != nil {
		return fmt.Errorf("widget.toml: %w", err)
	}
	return nil
}

// exportsFunc reports whether the Go package in dir declares a top-level
// function name (Choices, so a widget opts in just by writing it).
func exportsFunc(dir, name string) (bool, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return false, err
	}
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			return false, err
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == name {
				return true, nil
			}
		}
	}
	return false, nil
}
