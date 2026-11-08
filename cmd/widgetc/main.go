// Command widgetc builds a widget from its folder:
//
//	<widget>/
//	  widget.toml          manifest: name, version, config message, network request
//	  buf.yaml, buf.lock   the schema's buf module (proto/ plus its deps)
//	  proto/...            the schema: State, Delta, Config
//	  server/              the server half: a Go package exporting Run
//	  view/view.ts         the view half: calls defineWidget
//	  view/view.css        optional
//
// `widgetc build <dir>` generates the schema's Go code into <dir>/gen and its
// TypeScript into <dir>/view/gen (from the same .proto, so the two halves
// can't disagree), bundles the view into <dir>/dist, and writes
// <dir>/widget_gen.go, which exposes the whole widget as a widget.Spec.
// It also compiles the server half for the sandbox (GOOS=wasip1) and writes
// <dir>/<name>.wpkg: schema, server.wasm and view in one file, which is
// what a host loads; widget_gen.go just embeds it.
package main

import (
	"bytes"
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
	Config  string `toml:"config"`
	Network struct {
		Hosts   []string `toml:"hosts"`
		Methods []string `toml:"methods"`
		Why     string   `toml:"why"`
	} `toml:"network"`
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func main() {
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: widgetc build <widget-dir | file.widget>...")
	}
	flag.Parse()
	if flag.NArg() < 2 || flag.Arg(0) != "build" {
		flag.Usage()
		os.Exit(2)
	}
	for _, dir := range flag.Args()[1:] {
		var err error
		if strings.HasSuffix(dir, ".widget") {
			err = buildSingleFile(dir)
		} else {
			err = build(dir, true)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "widgetc: %s: %v\n", dir, err)
			os.Exit(1)
		}
	}
}

// build builds a widget folder. withSpec also writes widget_gen.go, the Go
// package that embeds the result; a single-file widget's build folder is
// scratch, so it gets just the .wpkg.
func build(dir string, withSpec bool) error {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	var m manifest
	if err := readManifest(dir, &m); err != nil {
		return err
	}
	if !nameRE.MatchString(m.Name) {
		return fmt.Errorf("widget.toml: name %q: want lowercase letters, digits and dashes", m.Name)
	}
	if m.Version == "" {
		return errors.New("widget.toml: version is required")
	}

	importPath, err := goImportPath(dir)
	if err != nil {
		return err
	}
	protos, err := protoFiles(filepath.Join(dir, "proto"))
	if err != nil {
		return err
	}
	if len(protos) == 0 {
		return errors.New("no .proto files under proto/")
	}

	if err := generate(dir, importPath); err != nil {
		return err
	}
	if err := bundleView(dir); err != nil {
		return err
	}
	if err := pack(dir, importPath, m, protos); err != nil {
		return err
	}
	if !withSpec {
		return nil
	}
	return writeSpec(dir, m)
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

// bundleView bundles view/view.ts (and copies view/view.css) into dist/. The
// bundle is a classic script: the card frame runs in an opaque origin, where
// a module script would need CORS.
func bundleView(dir string) error {
	dist := filepath.Join(dir, "dist")
	if err := os.RemoveAll(dist); err != nil {
		return err
	}
	if err := run(dir, "bun", "build", "view/view.ts", "--outfile", "dist/view.js", "--format", "iife", "--target", "browser", "--minify"); err != nil {
		return err
	}
	css, err := os.ReadFile(filepath.Join(dir, "view", "view.css"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dist, "view.css"), css, 0o644)
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

func main() { widget.Main(server.Run{{if .Choices}}, widget.WithChoices(server.Choices){{end}}) }
`))

// pack builds server.wasm and the schema, and writes <name>.wpkg: the one
// file a host needs to run the widget.
func pack(dir, importPath string, m manifest, protos []string) error {
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
	if err := mainTmpl.Execute(&src, map[string]any{"ImportPath": importPath, "Choices": choices}); err != nil {
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
		Name: m.Name, Version: m.Version, Title: m.Title, Config: m.Config,
		Network: widget.NetworkRequest{Hosts: m.Network.Hosts, Methods: m.Network.Methods, Why: m.Network.Why},
	}
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

var specTmpl = template.Must(template.New("spec").Parse(`// Code generated by widgetc from widget.toml. DO NOT EDIT.

package {{.Package}}

import (
	_ "embed"

	"github.com/aren55555/widgetd/host"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

// Package is the {{.Name}} widget, packaged: its schema, its sandboxed
// server half, and its view.
//
//go:embed {{.Name}}.wpkg
var Package []byte

// Widget loads Package, ready to hand to a widgetd host.
func Widget() (widget.Spec, error) { return host.LoadPackage(Package) }
`))

func writeSpec(dir string, m manifest) error {
	var buf bytes.Buffer
	err := specTmpl.Execute(&buf, map[string]any{
		"Package": strings.ReplaceAll(filepath.Base(dir), "-", ""),
		"Name":    m.Name,
	})
	if err != nil {
		return err
	}
	src, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("format widget_gen.go: %w\n%s", err, buf.Bytes())
	}
	return os.WriteFile(filepath.Join(dir, "widget_gen.go"), src, 0o644)
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
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, parser.SkipObjectResolution)
	if err != nil {
		return false, err
	}
	for _, p := range pkgs {
		for _, f := range p.Files {
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == name {
					return true, nil
				}
			}
		}
	}
	return false, nil
}
