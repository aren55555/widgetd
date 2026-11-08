package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// A .widget file is a whole widget in one file, in blocks:
//
//	<manifest>        widget.toml
//	<schema>          the .proto (one file; State, Delta, Config)
//	<server lang="go">  the server half: package server, exporting Run
//	<view lang="ts">  the view half: calls defineWidget
//	<style>           optional: the view's CSS
//
// Both halves import their generated types from the module path
// "widget/schema"; widgetc rewrites that to wherever it generated them.
// widgetc splits the file into an ordinary widget folder under .widgetc/
// beside it, builds that, and writes <name>.wpkg next to the .widget. The
// folder is scratch: edit the .widget. Go errors point back into it.

var blockRE = regexp.MustCompile(`(?ms)^<(manifest|schema|server|view|style)((?:\s+[a-z]+="[^"]*")*)\s*>[ \t]*\n(.*?)^</(manifest|schema|server|view|style)>[ \t]*$`)

type block struct {
	body string
	line int // line in the .widget where the body starts
	attr string
}

func parseSingleFile(src string) (map[string]block, error) {
	blocks := map[string]block{}
	for _, m := range blockRE.FindAllStringSubmatchIndex(src, -1) {
		name, closing := src[m[2]:m[3]], src[m[8]:m[9]]
		if name != closing {
			return nil, fmt.Errorf("<%s> closed by </%s>", name, closing)
		}
		if _, dup := blocks[name]; dup {
			return nil, fmt.Errorf("two <%s> blocks", name)
		}
		blocks[name] = block{
			body: src[m[6]:m[7]],
			line: strings.Count(src[:m[6]], "\n") + 1,
			attr: strings.TrimSpace(src[m[4]:m[5]]),
		}
	}
	for _, need := range []string{"manifest", "schema", "server", "view"} {
		if _, ok := blocks[need]; !ok {
			return nil, fmt.Errorf("no <%s> block", need)
		}
	}
	if a := blocks["server"].attr; a != "" && a != `lang="go"` {
		return nil, fmt.Errorf(`<server %s>: only lang="go" is built by widgetc so far`, a)
	}
	return blocks, nil
}

var protoPackageRE = regexp.MustCompile(`(?m)^package\s+([a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)*)\s*;`)

const bufYAML = `version: v2
modules:
  - path: proto
deps:
  - buf.build/bufbuild/protovalidate
`

func buildSingleFile(path string) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	blocks, err := parseSingleFile(string(raw))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	pkg := protoPackageRE.FindStringSubmatch(blocks["schema"].body)
	if pkg == nil {
		return fmt.Errorf("%s: <schema> needs a package line, e.g. `package clock.v1;`", path)
	}
	protoDir := strings.ReplaceAll(pkg[1], ".", "/")
	base := strings.TrimSuffix(filepath.Base(path), ".widget")
	protoRel := protoDir + "/" + strings.ReplaceAll(base, "-", "_") + ".proto"

	dir := filepath.Join(filepath.Dir(path), ".widgetc", base)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	importPath, err := goImportPath(dir)
	if err != nil {
		return err
	}
	genGo := importPath + "/gen/" + protoDir
	genTS := "./gen/" + strings.TrimSuffix(protoRel, ".proto") + "_pb.ts"

	files := map[string]string{
		"widget.toml":       blocks["manifest"].body,
		"buf.yaml":          bufYAML,
		"proto/" + protoRel: blocks["schema"].body,
		"server/server.go": fmt.Sprintf("//line %s:%d\n%s", path, blocks["server"].line,
			strings.ReplaceAll(blocks["server"].body, `"widget/schema"`, `"`+genGo+`"`)),
		"view/view.ts": fmt.Sprintf("// Generated from %s (view starts at line %d). Edit that file.\n%s", filepath.Base(path), blocks["view"].line,
			strings.ReplaceAll(strings.ReplaceAll(blocks["view"].body, `"widget/schema"`, `"`+genTS+`"`), `'widget/schema'`, `'`+genTS+`'`)),
	}
	if s, ok := blocks["style"]; ok {
		files["view/view.css"] = s.body
	}
	if err := os.RemoveAll(filepath.Join(dir, "proto")); err != nil {
		return err
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return err
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "buf.lock")); errors.Is(err, os.ErrNotExist) {
		if err := run(dir, "buf", "dep", "update"); err != nil {
			return err
		}
	}

	if err := build(dir, false); err != nil {
		return err
	}
	var m manifest
	if err := readManifest(dir, &m); err != nil {
		return err
	}
	return os.Rename(filepath.Join(dir, m.Name+".wpkg"), filepath.Join(filepath.Dir(path), m.Name+".wpkg"))
}
