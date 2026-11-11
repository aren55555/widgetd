package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// module makes a directory that is its own Go module, example.com/m, so
// import paths can be worked out without the repository.
func module(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const sample = `<manifest>
name = "demo"
version = "0.1.0"
</manifest>

<schema>
syntax = "proto3";

package demo.v1;

import "demo/common/v1/common.proto";

message State {
  demo.common.v1.Count count = 1;
}
</schema>

<server lang="go">
package server

import (
	pb "widget/schema"
	"widget/gen/demo/common/v1"
	"widget/server/util"
)

var _ = pb.State{}
var _ = commonv1.Count{}
var _ = util.X
</server>

<view lang="ts">
import { StateSchema } from "widget/schema";
import { x } from './sub/x.ts';
</view>

<style>
.a { color: red; }
</style>

<file path="proto/demo/common/v1/common.proto">
syntax = "proto3";

package demo.common.v1;

message Count {
  int32 n = 1;
}
</file>

<file path="server/util/util.go">
package util

const X = 1
</file>

<file path="view/sub/x.ts">
import type { State } from 'widget/schema';
export const x = 1;
</file>
`

func TestSplitJoinRoundTrip(t *testing.T) {
	mod := module(t)
	wfile := filepath.Join(mod, "demo.widget")
	if err := os.WriteFile(wfile, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(mod, "out", "demo")
	if err := split(wfile, dir); err != nil {
		t.Fatal(err)
	}
	read := func(p string) string {
		b, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for p, want := range map[string]string{
		"server/server.go": `pb "example.com/m/out/demo/gen/demo/v1"`,
		"view/view.ts":     `from "./gen/demo/v1/demo_pb.ts"`,
		"view/sub/x.ts":    `from '../gen/demo/v1/demo_pb.ts'`,
		"buf.yaml":         "buf.build/bufbuild/protovalidate",
		"view/view.css":    ".a { color: red; }",
	} {
		if got := read(p); !strings.Contains(got, want) {
			t.Errorf("%s: want %q in\n%s", p, want, got)
		}
	}
	if got := read("server/server.go"); !strings.Contains(got, `"example.com/m/out/demo/gen/demo/common/v1"`) || !strings.Contains(got, `"example.com/m/out/demo/server/util"`) {
		t.Errorf("server.go imports not rewritten:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "proto", "demo", "v1", "demo.proto")); err != nil {
		t.Errorf("schema not at its default path: %v", err)
	}
	if err := split(wfile, dir); err == nil {
		t.Error("split into a non-empty folder succeeded")
	}
}

func TestJoinCanonical(t *testing.T) {
	mod := module(t)
	wfile := filepath.Join(mod, "demo.widget")
	if err := os.WriteFile(wfile, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(mod, "demo")
	if err := split(wfile, dir); err != nil {
		t.Fatal(err)
	}
	got, err := joinText(dir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if got != sample {
		t.Errorf("join(split(w)) != w\n--- got\n%s\n--- want\n%s", got, sample)
	}
	// Under another name the schema keeps its path, so it says so.
	other, err := joinText(dir, "other")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(other, `<schema path="proto/demo/v1/demo.proto">`) {
		t.Errorf("want a schema path attribute:\n%s", other)
	}
	if err := join(dir, wfile); err == nil {
		t.Error("join over an existing file succeeded")
	}
}

func TestSplitAnnotates(t *testing.T) {
	mod := module(t)
	blocks, err := parseSingleFile(sample)
	if err != nil {
		t.Fatal(err)
	}
	s, err := widgetSource(blocks, "demo")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(mod, "scratch")
	if err := writeFolder(s, dir, "demo.widget", true); err != nil {
		t.Fatal(err)
	}
	lineOf := func(marker string) int {
		i := strings.Index(sample, marker)
		return strings.Count(sample[:i], "\n") + 2 // the body starts on the next line
	}
	for p, marker := range map[string]string{
		"server/server.go":    "<server",
		"server/util/util.go": `<file path="server/util/util.go">`,
	} {
		b, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil {
			t.Fatal(err)
		}
		want := "//line /widget/demo.widget:" + strconv.Itoa(lineOf(marker)) + "\n"
		if !strings.HasPrefix(string(b), want) {
			t.Errorf("%s: want it to start %q, got %q", p, want, firstLine(string(b)))
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "view", "sub", "x.ts"))
	if !strings.HasPrefix(string(b), "// Generated from demo.widget") {
		t.Errorf("view/sub/x.ts: no header: %q", firstLine(string(b)))
	}
	b, _ = os.ReadFile(filepath.Join(dir, "buf.yaml"))
	if string(b) != bufYAML {
		t.Errorf("buf.yaml annotated or changed: %q", b)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func TestParseErrors(t *testing.T) {
	const m = "<manifest>\nname = \"x\"\n</manifest>\n"
	for name, src := range map[string]string{
		"stray text":         m + "hello\n",
		"stray before":       "# note\n" + m,
		"unclosed":           m + "<view>\nx\n",
		"mismatched":         m + "<view>\nx\n</style>\n",
		"two views":          m + "<view>\n</view>\n<view>\n</view>\n",
		"no manifest":        "<view>\n</view>\n",
		"unknown attr":       m + "<style media=\"x\">\n</style>\n",
		"bad lang":           m + "<server lang=\"rust\">\n</server>\n",
		"file without path":  m + "<file>\n</file>\n",
		"absolute":           m + "<file path=\"/etc/x\">\n</file>\n",
		"dotdot":             m + "<file path=\"../x\">\n</file>\n",
		"unclean":            m + "<file path=\"a//b\">\n</file>\n",
		"generated gen":      m + "<file path=\"gen/x.go\">\n</file>\n",
		"generated view gen": m + "<file path=\"view/gen/x.ts\">\n</file>\n",
		"generated dist":     m + "<file path=\"dist/view.js\">\n</file>\n",
		"node_modules":       m + "<file path=\"view/node_modules/a/b.js\">\n</file>\n",
		"wpkg":               m + "<file path=\"x.wpkg\">\n</file>\n",
		"hidden":             m + "<file path=\".gitignore\">\n</file>\n",
		"named path":         m + "<file path=\"server/server.go\">\n</file>\n",
		"manifest path":      m + "<file path=\"widget.toml\">\n</file>\n",
		"schema path":        m + "<schema>\npackage a.v1;\n</schema>\n<file path=\"proto/a/v1/t.proto\">\n</file>\n",
		"default buf.yaml":   m + "<file path=\"buf.yaml\">\n" + bufYAML + "</file>\n",
		"same file twice":    m + "<file path=\"a.txt\">\n</file>\n<file path=\"a.txt\">\n</file>\n",
		"schema no package":  m + "<schema>\nsyntax = \"proto3\";\n</schema>\n",
	} {
		blocks, err := parseSingleFile(src)
		if err == nil {
			_, err = widgetSource(blocks, "t")
		}
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestJoinRefuses(t *testing.T) {
	base := map[string]string{
		"widget.toml":        "name = \"x\"\n",
		"proto/x/v1/x.proto": "syntax = \"proto3\";\npackage x.v1;\n",
	}
	for name, extra := range map[string]map[string]string{
		"binary":         {"view/logo.png": "\x89PNG\x00\x01\n"},
		"no newline":     {"view/view.ts": "export {}"},
		"closing tag":    {"notes.md": "a\n</file>\nb\n"},
		"widget import":  {"server/server.go": "package server\n\nimport _ \"widget/x\"\n"},
		"schema package": {"server/server.go": "package server\n\nimport _ \"example.com/m/w/schema\"\n"},
		"bad imports":    {"server/server.go": "package server\n\nimport (\n"},
		"alias in ts":    {"view/view.ts": "import \"widget/schema\";\n"},
	} {
		mod := module(t)
		dir := filepath.Join(mod, "w")
		for p, body := range base {
			write(t, filepath.Join(dir, p), body)
		}
		for p, body := range extra {
			write(t, filepath.Join(dir, p), body)
		}
		if _, err := joinText(dir, "x"); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestJoinSkipsGenerated(t *testing.T) {
	mod := module(t)
	dir := filepath.Join(mod, "w")
	for p, body := range map[string]string{
		"widget.toml":              "name = \"x\"\n",
		"proto/x/v1/x.proto":       "syntax = \"proto3\";\npackage x.v1;\n",
		"gen/x/v1/x.pb.go":         "package x\n",
		"view/gen/x/v1/x_pb.ts":    "export {}\n",
		"dist/view.js":             "x\n",
		"wasm/main_gen.go":         "package main\n",
		"widget_gen.go":            "package w\n",
		"x.wpkg":                   "x\n",
		"view/node_modules/a/i.js": "x\n",
		".DS_Store":                "x\n",
		"view/.cache/x":            "x\n",
		"notes/readme.md":          "kept\n",
	} {
		write(t, filepath.Join(dir, p), body)
	}
	got, err := joinText(dir, "x")
	if err != nil {
		t.Fatal(err)
	}
	want := "<manifest>\nname = \"x\"\n</manifest>\n\n<schema>\nsyntax = \"proto3\";\npackage x.v1;\n</schema>\n\n<file path=\"notes/readme.md\">\nkept\n</file>\n"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestClearSources(t *testing.T) {
	mod := module(t)
	dir := filepath.Join(mod, "scratch")
	for _, p := range []string{"server/old.go", "server/server.go", "buf.lock", "view/bun.lock", "gen/x.go", "view/node_modules/a.js"} {
		write(t, filepath.Join(dir, p), "x\n")
	}
	s := source{files: []srcFile{{path: "server/server.go"}}}
	if err := clearSources(dir, s); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]bool{
		"server/old.go": false, "server/server.go": true, "buf.lock": true, "view/bun.lock": true, "gen/x.go": true, "view/node_modules/a.js": true,
	} {
		_, err := os.Stat(filepath.Join(dir, p))
		if got := err == nil; got != want {
			t.Errorf("%s: exists %v, want %v", p, got, want)
		}
	}
}

func TestGoName(t *testing.T) {
	for in, want := range map[string]string{"clock": "Clock", "bitcoin-spiral": "BitcoinSpiral", "9lives": "W9lives", "a--b": "AB"} {
		if got := goName(in); got != want {
			t.Errorf("goName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDirPackage(t *testing.T) {
	dir := t.TempDir()
	if p, err := dirPackage(dir); err != nil || p != "" {
		t.Fatalf("empty dir: %q, %v", p, err)
	}
	write(t, filepath.Join(dir, "a.go"), "package things\n")
	write(t, filepath.Join(dir, "a_test.go"), "package things_test\n")
	if p, err := dirPackage(dir); err != nil || p != "things" {
		t.Fatalf("got %q, %v", p, err)
	}
}

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
