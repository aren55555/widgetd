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
//
// Single-file .widget sources and WASM packages arrive in M1b.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/BurntSushi/toml"
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
		fmt.Fprintln(os.Stderr, "usage: widgetc build <widget-dir>...")
	}
	flag.Parse()
	if flag.NArg() < 2 || flag.Arg(0) != "build" {
		flag.Usage()
		os.Exit(2)
	}
	for _, dir := range flag.Args()[1:] {
		if err := build(dir); err != nil {
			fmt.Fprintf(os.Stderr, "widgetc: %s: %v\n", dir, err)
			os.Exit(1)
		}
	}
}

func build(dir string) error {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	var m manifest
	if _, err := toml.DecodeFile(filepath.Join(dir, "widget.toml"), &m); err != nil {
		return fmt.Errorf("widget.toml: %w", err)
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
	return writeSpec(dir, importPath, m, protos)
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
	es, err := findUp(filepath.Join(dir, "view"), "node_modules/.bin/protoc-gen-es")
	if err != nil {
		return fmt.Errorf("protoc-gen-es not found; run `bun install` (view/package.json needs @bufbuild/protoc-gen-es): %w", err)
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
			{"local": []string{"bun", es}, "out": "view/gen", "include_imports": true, "opt": []string{"target=ts"}},
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

var specTmpl = template.Must(template.New("spec").Parse(`// Code generated by widgetc from widget.toml. DO NOT EDIT.

package {{.Package}}

import (
	"embed"
	"io/fs"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/aren55555/widgetd/sdk/go/widget"
{{range .GenImports}}	_ "{{.}}"
{{end}}	"{{.ImportPath}}/server"
)

//go:embed all:dist
var dist embed.FS

// Widget is the {{.Name}} widget, ready to hand to a widgetd host.
func Widget() widget.Spec {
	view, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return widget.Spec{
		Name:    {{printf "%q" .Name}},
		Version: {{printf "%q" .Version}},
		Files:   files({{range .Protos}}{{printf "%q" .}}, {{end}}),
		Config:  {{if .Config}}message({{printf "%q" .Config}}){{else}}nil{{end}},
		Run:     server.Run,
		View:    view,
	}
}

func files(paths ...string) []protoreflect.FileDescriptor {
	out := make([]protoreflect.FileDescriptor, len(paths))
	for i, p := range paths {
		fd, err := protoregistry.GlobalFiles.FindFileByPath(p)
		if err != nil {
			panic(err)
		}
		out[i] = fd
	}
	return out
}

func message(name protoreflect.FullName) proto.Message {
	mt, err := protoregistry.GlobalTypes.FindMessageByName(name)
	if err != nil {
		panic(err)
	}
	return mt.New().Interface()
}
`))

func writeSpec(dir, importPath string, m manifest, protos []string) error {
	pkg := strings.ReplaceAll(filepath.Base(dir), "-", "")
	genDirs := map[string]bool{}
	for _, p := range protos {
		genDirs[importPath+"/gen/"+filepath.ToSlash(filepath.Dir(p))] = true
	}
	var imports []string
	for d := range genDirs {
		imports = append(imports, d)
	}
	sort.Strings(imports)

	var buf bytes.Buffer
	err := specTmpl.Execute(&buf, map[string]any{
		"Package": pkg, "ImportPath": importPath, "GenImports": imports,
		"Name": m.Name, "Version": m.Version, "Config": m.Config, "Protos": protos,
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
