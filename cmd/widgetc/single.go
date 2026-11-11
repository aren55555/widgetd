package main

import (
	"bytes"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// A .widget file is a widget folder written as one file: every source file
// of the folder is a block, and nothing else is in the file.
//
//	<manifest>                widget.toml
//	<schema>                  proto/<package dirs>/<file name>.proto, e.g.
//	                          proto/clock/v1/clock.proto for clock.widget;
//	                          <schema path="proto/…"> names it outright
//	<server lang="go">        server/server.go
//	<view lang="ts">          view/view.ts
//	<style>                   view/view.css
//	<file path="…">           any other source file: more .proto files, more
//	                          Go files in server/, more view modules, tests,
//	                          view/package.json, buf.lock, …
//
// Only <manifest> is required; what a widget needs beyond it is checked by
// the build, the same as for a folder. buf.yaml may be left out, and is then
// the default (proto/ plus protovalidate). Generated paths (gen/, view/gen/,
// dist/, wasm/, widget_gen.go, *.wpkg), node_modules and hidden files are
// never sources, in either form.
//
// A folder's Go imports name its own packages by import path, which depends
// on where the folder sits. A .widget names them from "widget" instead:
// "widget/gen/clock/v1", "widget/server/…", and "widget/schema" for the Go
// package generated from the <schema> block. Script files may import
// "widget/schema" too, for that block's generated TypeScript. Converting
// rewrites these and nothing else.
//
// `widgetc split` and `widgetc join` convert between the two forms without
// loss: join(split(w)) == w for any .widget join could have written (blocks
// in the order above, then <file> blocks by path), and split(join(dir))
// reproduces dir's sources byte for byte.
//
// `widgetc build x.widget` splits into a scratch folder, .widgetc/<x>/ beside
// the file, and builds that like any folder. It writes <name>.wpkg and
// <name>_widget_gen.go (the Go embedding, as widget_gen.go is for a folder)
// beside the .widget. The scratch copy carries //line directives, so Go
// errors point into the .widget; that is the only difference from a split.

const bufYAML = `version: v2
modules:
  - path: proto
deps:
  - buf.build/bufbuild/protovalidate
`

var (
	openRE  = regexp.MustCompile(`(?m)^<(manifest|schema|server|view|style|file)((?:[ \t]+[a-z]+="[^"]*")*)[ \t]*>[ \t]*\n`)
	closeRE = regexp.MustCompile(`(?m)^</(manifest|schema|server|view|style|file)>[ \t]*(?:\n|\z)`)
	attrRE  = regexp.MustCompile(`([a-z]+)="([^"]*)"`)

	protoPackageRE = regexp.MustCompile(`(?m)^package\s+([a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)*)\s*;`)
)

// fixedPaths are the files a named block stands for. <schema> is the one
// named block whose path varies.
var fixedPaths = map[string]string{
	"manifest": "widget.toml",
	"server":   "server/server.go",
	"view":     "view/view.ts",
	"style":    "view/view.css",
}

type block struct {
	kind  string
	attrs map[string]string
	body  string
	line  int // line in the .widget where the body starts
}

// srcFile is one source file of a widget.
type srcFile struct {
	path string
	body string
	line int // line in the .widget where it starts; 0 if it isn't from one
}

// source is a widget's source files, whichever form they came from.
type source struct {
	files  []srcFile // sorted by path
	schema string    // path of the <schema> block's file, or ""
}

func (s source) get(p string) (srcFile, bool) {
	i := sort.Search(len(s.files), func(i int) bool { return s.files[i].path >= p })
	if i < len(s.files) && s.files[i].path == p {
		return s.files[i], true
	}
	return srcFile{}, false
}

func parseSingleFile(src string) ([]block, error) {
	var blocks []block
	pos := 0
	stray := func(from, to int) error {
		if gap := src[from:to]; strings.TrimSpace(gap) != "" {
			off := from + strings.Index(gap, strings.TrimSpace(gap))
			return fmt.Errorf("line %d: text outside a block", strings.Count(src[:off], "\n")+1)
		}
		return nil
	}
	for {
		o := openRE.FindStringSubmatchIndex(src[pos:])
		if o == nil {
			break
		}
		for i := range o {
			o[i] += pos
		}
		if err := stray(pos, o[0]); err != nil {
			return nil, err
		}
		kind := src[o[2]:o[3]]
		line := strings.Count(src[:o[1]], "\n") + 1
		c := closeRE.FindStringSubmatchIndex(src[o[1]:])
		if c == nil {
			return nil, fmt.Errorf("line %d: <%s> is never closed", line-1, kind)
		}
		for i := range c {
			c[i] += o[1]
		}
		if closing := src[c[2]:c[3]]; closing != kind {
			return nil, fmt.Errorf("line %d: <%s> closed by </%s>", line-1, kind, closing)
		}
		attrs := map[string]string{}
		for _, a := range attrRE.FindAllStringSubmatch(src[o[4]:o[5]], -1) {
			if _, dup := attrs[a[1]]; dup {
				return nil, fmt.Errorf("line %d: <%s> has %s twice", line-1, kind, a[1])
			}
			attrs[a[1]] = a[2]
		}
		blocks = append(blocks, block{kind: kind, attrs: attrs, body: src[o[1]:c[0]], line: line})
		pos = c[1]
	}
	if err := stray(pos, len(src)); err != nil {
		return nil, err
	}
	return blocks, nil
}

// widgetSource turns a .widget's blocks into the folder they stand for.
// base is the .widget's file name without the extension.
func widgetSource(blocks []block, base string) (source, error) {
	var s source
	seen := map[string]bool{}
	byPath := map[string]srcFile{}
	add := func(b block, p string) error {
		if _, dup := byPath[p]; dup {
			return fmt.Errorf("line %d: %s is given twice", b.line-1, p)
		}
		byPath[p] = srcFile{path: p, body: b.body, line: b.line}
		return nil
	}
	allow := func(b block, names ...string) error {
		for k, v := range b.attrs {
			if !contains(names, k) {
				return fmt.Errorf("line %d: <%s> has no %s attribute", b.line-1, b.kind, k)
			}
			if k == "lang" && v != map[string]string{"server": "go", "view": "ts"}[b.kind] {
				return fmt.Errorf(`line %d: <%s lang=%q>: widgetc builds only lang="%s" so far`, b.line-1, b.kind, v, map[string]string{"server": "go", "view": "ts"}[b.kind])
			}
		}
		return nil
	}
	var files []block
	for _, b := range blocks {
		if b.kind != "file" {
			if seen[b.kind] {
				return s, fmt.Errorf("line %d: two <%s> blocks", b.line-1, b.kind)
			}
			seen[b.kind] = true
		}
		switch b.kind {
		case "manifest", "style":
			if err := allow(b); err != nil {
				return s, err
			}
			if err := add(b, fixedPaths[b.kind]); err != nil {
				return s, err
			}
		case "server", "view":
			if err := allow(b, "lang"); err != nil {
				return s, err
			}
			if err := add(b, fixedPaths[b.kind]); err != nil {
				return s, err
			}
		case "schema":
			if err := allow(b, "path"); err != nil {
				return s, err
			}
			p, ok := b.attrs["path"]
			if ok {
				if err := checkSourcePath(p); err != nil {
					return s, fmt.Errorf("line %d: <schema path=%q>: %w", b.line-1, p, err)
				}
				if !strings.HasPrefix(p, "proto/") || !strings.HasSuffix(p, ".proto") {
					return s, fmt.Errorf("line %d: <schema path=%q>: want proto/….proto", b.line-1, p)
				}
			} else {
				var err error
				if p, err = defaultSchemaPath(b.body, base); err != nil {
					return s, fmt.Errorf("line %d: <schema>: %w", b.line-1, err)
				}
			}
			s.schema = p
			if err := add(b, p); err != nil {
				return s, err
			}
		case "file":
			files = append(files, b) // after the named blocks, whose paths they mustn't take
		}
	}
	if !seen["manifest"] {
		return s, errors.New("no <manifest> block")
	}
	for _, b := range files {
		if err := allow(b, "path"); err != nil {
			return s, err
		}
		p, ok := b.attrs["path"]
		if !ok {
			return s, fmt.Errorf("line %d: <file> needs a path attribute", b.line-1)
		}
		if err := checkSourcePath(p); err != nil {
			return s, fmt.Errorf("line %d: <file path=%q>: %w", b.line-1, p, err)
		}
		for kind, fp := range fixedPaths {
			if p == fp {
				return s, fmt.Errorf("line %d: <file path=%q>: that is the <%s> block", b.line-1, p, kind)
			}
		}
		if p == s.schema {
			return s, fmt.Errorf("line %d: <file path=%q>: that is the <schema> block", b.line-1, p)
		}
		if p == "buf.yaml" && b.body == bufYAML {
			return s, fmt.Errorf("line %d: <file path=\"buf.yaml\"> is the default; leave it out", b.line-1)
		}
		if err := add(b, p); err != nil {
			return s, err
		}
	}
	if _, ok := byPath["buf.yaml"]; !ok {
		byPath["buf.yaml"] = srcFile{path: "buf.yaml", body: bufYAML}
	}
	for _, f := range byPath {
		s.files = append(s.files, f)
	}
	sort.Slice(s.files, func(i, j int) bool { return s.files[i].path < s.files[j].path })
	return s, nil
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// defaultSchemaPath is where a <schema> block without a path goes: its
// package as directories, and the .widget's name as the file name.
func defaultSchemaPath(body, base string) (string, error) {
	pkg := protoPackageRE.FindStringSubmatch(body)
	if pkg == nil {
		return "", errors.New("needs a package line, e.g. `package clock.v1;`, or a path attribute")
	}
	return "proto/" + strings.ReplaceAll(pkg[1], ".", "/") + "/" + strings.ReplaceAll(base, "-", "_") + ".proto", nil
}

// checkSourcePath accepts the relative paths a widget's source files may have.
func checkSourcePath(p string) error {
	if p == "" || p == "." || strings.ContainsAny(p, "\\\"\n") || path.IsAbs(p) || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") {
		return errors.New("want a clean relative path inside the widget")
	}
	if generatedPath(p) {
		return errors.New("widgetc generates that")
	}
	if hiddenPath(p) {
		return errors.New("hidden files aren't part of a widget")
	}
	return nil
}

// generatedPath reports whether p is something widgetc or a package manager
// writes, so never a source.
func generatedPath(p string) bool {
	segs := strings.Split(p, "/")
	switch {
	case segs[0] == "gen", segs[0] == "dist", segs[0] == "wasm":
		return true
	case len(segs) > 1 && segs[0] == "view" && segs[1] == "gen":
		return true
	case p == "widget_gen.go", len(segs) == 1 && strings.HasSuffix(p, ".wpkg"):
		return true
	}
	return contains(segs, "node_modules")
}

func hiddenPath(p string) bool {
	for _, s := range strings.Split(p, "/") {
		if strings.HasPrefix(s, ".") {
			return true
		}
	}
	return false
}

// readSources reads a widget folder's source files: everything but what
// generatedPath and hiddenPath exclude.
func readSources(dir string) (map[string][]byte, error) {
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(fp string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, fp)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if generatedPath(rel) || hiddenPath(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s: not a regular file", rel)
		}
		b, err := os.ReadFile(fp)
		if err != nil {
			return err
		}
		out[rel] = b
		return nil
	})
	return out, err
}

// layout is what the import rewrites need to know about a folder.
type layout struct {
	importPath string // the folder's Go import path
	schema     string // the <schema> block's file, or ""
}

func (l layout) schemaGo() string {
	d := path.Dir(strings.TrimPrefix(l.schema, "proto/"))
	if d == "." {
		return l.importPath + "/gen"
	}
	return l.importPath + "/gen/" + d
}

// schemaTS is the generated TypeScript for the schema, relative to the folder.
func (l layout) schemaTS() string {
	return "view/gen/" + strings.TrimSuffix(strings.TrimPrefix(l.schema, "proto/"), ".proto") + "_pb.ts"
}

// schemaTSFrom is how a script at p imports the schema's TypeScript.
func (l layout) schemaTSFrom(p string) string {
	r, err := filepath.Rel(filepath.FromSlash(path.Dir(p)), filepath.FromSlash(l.schemaTS()))
	if err != nil {
		panic(err) // both relative
	}
	r = filepath.ToSlash(r)
	if !strings.HasPrefix(r, "../") {
		r = "./" + r
	}
	return r
}

func isScript(p string) bool {
	switch path.Ext(p) {
	case ".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs":
		return true
	}
	return false
}

// rewriteGoImports rewrites the import paths of a Go file. ok is false if
// its imports don't parse.
func rewriteGoImports(src string, f func(string) (string, error)) (out string, ok bool, err error) {
	fset := token.NewFileSet()
	file, perr := parser.ParseFile(fset, "", src, parser.ImportsOnly)
	if perr != nil {
		return src, false, nil //nolint:nilerr // unparseable imports are reported by ok, not err
	}
	b := []byte(src)
	for i := len(file.Imports) - 1; i >= 0; i-- {
		lit := file.Imports[i].Path
		q := lit.Value[0]
		old := lit.Value[1 : len(lit.Value)-1]
		np, err := f(old)
		if err != nil {
			return src, true, err
		}
		if np == old {
			continue
		}
		start, end := fset.Position(lit.Pos()).Offset, fset.Position(lit.End()).Offset
		b = append(b[:start:start], append([]byte(string(q)+np+string(q)), b[end:]...)...)
	}
	return string(b), true, nil
}

var schemaAlias = []string{`"widget/schema"`, `'widget/schema'`}

// toFolder turns a .widget source file's text into the folder's.
func toFolder(l layout, f srcFile) (string, error) {
	switch {
	case strings.HasSuffix(f.path, ".go"):
		out, _, err := rewriteGoImports(f.body, func(p string) (string, error) {
			switch {
			case p == "widget/schema":
				if l.schema == "" {
					return "", fmt.Errorf("%s imports widget/schema, but there is no <schema> block", f.path)
				}
				return l.schemaGo(), nil
			case p == "widget":
				return l.importPath, nil
			case strings.HasPrefix(p, "widget/"):
				return l.importPath + strings.TrimPrefix(p, "widget"), nil
			}
			return p, nil
		})
		return out, err
	case isScript(f.path):
		body := f.body
		for _, a := range schemaAlias {
			if !strings.Contains(body, a) {
				continue
			}
			if l.schema == "" {
				return "", fmt.Errorf("%s imports widget/schema, but there is no <schema> block", f.path)
			}
			body = strings.ReplaceAll(body, a, a[:1]+l.schemaTSFrom(f.path)+a[:1])
		}
		return body, nil
	}
	return f.body, nil
}

// toWidget is the inverse of toFolder.
func toWidget(l layout, p, text string) (string, error) {
	switch {
	case strings.HasSuffix(p, ".go"):
		out, ok, err := rewriteGoImports(text, func(ip string) (string, error) {
			switch {
			case ip == "widget" || strings.HasPrefix(ip, "widget/"):
				return "", fmt.Errorf("%s imports %q, a path a .widget reserves for its own packages", p, ip)
			case l.schema != "" && ip == l.schemaGo():
				return "widget/schema", nil
			case ip == l.importPath+"/schema":
				return "", fmt.Errorf("%s imports %q, which a .widget can't name: widget/schema is the generated schema", p, ip)
			case ip == l.importPath:
				return "widget", nil
			case strings.HasPrefix(ip, l.importPath+"/"):
				return "widget" + strings.TrimPrefix(ip, l.importPath), nil
			}
			return ip, nil
		})
		if err == nil && !ok {
			err = fmt.Errorf("%s: its imports don't parse", p)
		}
		return out, err
	case isScript(p):
		for _, a := range schemaAlias {
			if strings.Contains(text, a) {
				return "", fmt.Errorf("%s contains %s, which a .widget reserves", p, a)
			}
		}
		if l.schema != "" {
			rel := l.schemaTSFrom(p)
			for _, a := range schemaAlias {
				text = strings.ReplaceAll(text, a[:1]+rel+a[:1], a)
			}
		}
		return text, nil
	}
	return text, nil
}

// writeFolder writes s's files into dir as a folder. annotate marks files
// that came from the .widget wname so errors point back into it.
func writeFolder(s source, dir, wname string, annotate bool) error {
	ip, err := goImportPath(dir)
	if err != nil {
		return err
	}
	l := layout{importPath: ip, schema: s.schema}
	for _, f := range s.files {
		text, err := toFolder(l, f)
		if err != nil {
			return err
		}
		if annotate && f.line > 0 {
			switch {
			case strings.HasSuffix(f.path, ".go"):
				// A fixed, machine-independent name: Go records //line paths
				// in the binary as they resolve (relative ones against the
				// file's directory) and -trimpath leaves them alone, so the
				// real path would make the package differ on every machine.
				// Errors read "/widget/clock.widget:42: …".
				text = fmt.Sprintf("//line /widget/%s:%d\n%s", wname, f.line, text)
			case isScript(f.path):
				text = fmt.Sprintf("// Generated from %s (this file starts at line %d). Edit that file.\n%s", wname, f.line, text)
			}
		}
		p := filepath.Join(dir, filepath.FromSlash(f.path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// scratchCaches are files in a scratch folder that outlive a rebuild when
// the .widget doesn't supply them: what `buf dep update` and `bun install`
// wrote last time.
var scratchCaches = []string{"buf.lock", "view/bun.lock"}

// clearSources removes dir's source files that s doesn't have, so a block
// taken out of a .widget doesn't linger in its scratch folder.
func clearSources(dir string, s source) error {
	old, err := readSources(dir)
	if err != nil {
		return err
	}
	for p := range old {
		if _, keep := s.get(p); keep || contains(scratchCaches, p) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
			return err
		}
	}
	return nil
}

func readWidgetFile(file string) (source, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return source{}, err
	}
	blocks, err := parseSingleFile(string(raw))
	if err != nil {
		return source{}, fmt.Errorf("%s: %w", file, err)
	}
	s, err := widgetSource(blocks, strings.TrimSuffix(filepath.Base(file), ".widget"))
	if err != nil {
		return source{}, fmt.Errorf("%s: %w", file, err)
	}
	return s, nil
}

// scratchDir is where `widgetc build` and `widgetc test` put a .widget's folder.
func scratchDir(file string) string {
	return filepath.Join(filepath.Dir(file), ".widgetc", strings.TrimSuffix(filepath.Base(file), ".widget"))
}

// prepareSingleFile writes a .widget's scratch folder and returns its path.
func prepareSingleFile(file string, annotate bool) (string, error) {
	file, err := filepath.Abs(file)
	if err != nil {
		return "", err
	}
	s, err := readWidgetFile(file)
	if err != nil {
		return "", err
	}
	dir := scratchDir(file)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := clearSources(dir, s); err != nil {
		return "", err
	}
	if err := writeFolder(s, dir, filepath.Base(file), annotate); err != nil {
		return "", fmt.Errorf("%s: %w", file, err)
	}
	return dir, nil
}

// buildSingleFile builds a .widget through its scratch folder, and writes
// <name>.wpkg and <name>_widget_gen.go beside it.
func buildSingleFile(file string, annotate bool) error {
	file, err := filepath.Abs(file)
	if err != nil {
		return err
	}
	dir, err := prepareSingleFile(file, annotate)
	if err != nil {
		return err
	}
	m, err := build(dir)
	if err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(dir, m.Name+".wpkg"), filepath.Join(filepath.Dir(file), m.Name+".wpkg")); err != nil {
		return err
	}
	return writeSingleSpec(file, m)
}

// split writes a .widget out as a widget folder in dir, which must not
// exist yet or be empty.
func split(file, dir string) error {
	s, err := readWidgetFile(file)
	if err != nil {
		return err
	}
	if ents, err := os.ReadDir(dir); err == nil && len(ents) > 0 {
		return fmt.Errorf("%s is not empty", dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeFolder(s, dir, filepath.Base(file), false)
}

// join writes the widget folder dir as one .widget file, which must not exist.
func join(dir, file string) error {
	if _, err := os.Stat(file); err == nil {
		return fmt.Errorf("%s exists", file)
	}
	text, err := joinText(dir, strings.TrimSuffix(filepath.Base(file), ".widget"))
	if err != nil {
		return err
	}
	return os.WriteFile(file, []byte(text), 0o644)
}

// joinText is the .widget for the folder dir, named base.widget.
func joinText(dir, base string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	srcs, err := readSources(dir)
	if err != nil {
		return "", err
	}
	if _, ok := srcs["widget.toml"]; !ok {
		return "", fmt.Errorf("%s: no widget.toml", dir)
	}
	ip, err := goImportPath(dir)
	if err != nil {
		return "", err
	}
	paths := make([]string, 0, len(srcs))
	for p, b := range srcs {
		if err := representable(p, b); err != nil {
			return "", err
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)

	schema := pickSchema(paths, srcs, base)

	l := layout{importPath: ip, schema: schema}
	var out strings.Builder
	emit := func(open, kind, p string) error {
		text, err := toWidget(l, p, string(srcs[p]))
		if err != nil {
			return err
		}
		if out.Len() > 0 {
			out.WriteString("\n")
		}
		fmt.Fprintf(&out, "<%s>\n%s</%s>\n", open, text, kind)
		return nil
	}
	done := map[string]bool{}
	named := []struct{ kind, open, path string }{
		{"manifest", "manifest", "widget.toml"},
		{"schema", "schema", schema},
		{"server", `server lang="go"`, "server/server.go"},
		{"view", `view lang="ts"`, "view/view.ts"},
		{"style", "style", "view/view.css"},
	}
	for _, n := range named {
		if _, ok := srcs[n.path]; !ok || n.path == "" {
			continue
		}
		open := n.open
		if n.kind == "schema" {
			if d, err := defaultSchemaPath(string(srcs[n.path]), base); err != nil || d != n.path {
				open = fmt.Sprintf("schema path=%q", n.path)
			}
		}
		if err := emit(open, n.kind, n.path); err != nil {
			return "", err
		}
		done[n.path] = true
	}
	for _, p := range paths {
		if done[p] || (p == "buf.yaml" && string(srcs[p]) == bufYAML) {
			continue
		}
		if err := emit(fmt.Sprintf("file path=%q", p), "file", p); err != nil {
			return "", err
		}
	}
	return out.String(), nil
}

var stateRE = regexp.MustCompile(`(?m)^message\s+State\s*\{`)

// pickSchema chooses which .proto a join writes as the <schema> block: the
// only one; else the one at its default path for base.widget; else the
// only one declaring message State. "" if none fits: every .proto is then
// a <file> block, which loses nothing.
func pickSchema(paths []string, srcs map[string][]byte, base string) string {
	var protos, states []string
	for _, p := range paths {
		if strings.HasPrefix(p, "proto/") && strings.HasSuffix(p, ".proto") {
			protos = append(protos, p)
			if stateRE.Match(srcs[p]) {
				states = append(states, p)
			}
		}
	}
	if len(protos) == 1 {
		return protos[0]
	}
	for _, p := range protos {
		if d, err := defaultSchemaPath(string(srcs[p]), base); err == nil && d == p {
			return p
		}
	}
	if len(states) == 1 {
		return states[0]
	}
	return ""
}

// representable reports whether a file can be a block and come back out
// unchanged.
func representable(p string, b []byte) error {
	switch {
	case bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b):
		return fmt.Errorf("%s: binary files can't go in a .widget", p)
	case len(b) > 0 && b[len(b)-1] != '\n':
		return fmt.Errorf("%s: doesn't end in a newline, which a .widget block always does", p)
	case closeRE.Match(b):
		return fmt.Errorf("%s: has a line that would end its block (%s)", p, closeRE.Find(b))
	case strings.ContainsAny(p, "\"\n"):
		return fmt.Errorf("%q: path can't go in a .widget", p)
	}
	return nil
}
