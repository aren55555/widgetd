package main

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/aren55555/widgetd/host"
)

// These tests build real widgets (buf, bun, a wasip1 compile), so they run
// only with WIDGETC_PARITY=1, as `just test` sets. They rebuild the examples
// in place and must not run alongside other packages' builds, which embed
// the examples' packages; `just test` runs them on their own.

func needParity(t *testing.T) {
	t.Helper()
	if os.Getenv("WIDGETC_PARITY") == "" {
		t.Skip("set WIDGETC_PARITY=1 to build widgets (needs buf, bun and network-free module caches)")
	}
}

func examplesDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.Abs("../../examples")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestParityFolders converts each example folder to a .widget and back, in
// place: the sources must come back byte for byte, and building them must
// give a byte-identical package. (In place, because a folder's Go import
// path, and so its compiled server, depends on where it is.)
func TestParityFolders(t *testing.T) {
	needParity(t)
	tomls, _ := filepath.Glob(filepath.Join(examplesDir(t), "*", "widget.toml"))
	if len(tomls) == 0 {
		t.Fatal("no example folders")
	}
	for _, manifest := range tomls {
		dir := filepath.Dir(manifest)
		base := filepath.Base(dir)
		t.Run(base, func(t *testing.T) {
			m, err := build(dir)
			if err != nil {
				t.Fatal(err)
			}
			wpkg := filepath.Join(dir, m.Name+".wpkg")
			before, err := os.ReadFile(wpkg)
			if err != nil {
				t.Fatal(err)
			}
			text, err := joinText(dir, base)
			if err != nil {
				t.Fatal(err)
			}
			orig, err := readSources(dir)
			if err != nil {
				t.Fatal(err)
			}
			// Put the sources back whatever happens (they should already be).
			t.Cleanup(func() {
				for p, b := range orig {
					if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(p)), b, 0o644); err != nil {
						t.Errorf("restore %s: %v", p, err)
					}
				}
			})
			for p := range orig {
				if err := os.Remove(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
					t.Fatal(err)
				}
			}
			blocks, err := parseSingleFile(text)
			if err != nil {
				t.Fatal(err)
			}
			s, err := widgetSource(blocks, base)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeFolder(s, dir, base+".widget", false); err != nil {
				t.Fatal(err)
			}
			back, err := readSources(dir)
			if err != nil {
				t.Fatal(err)
			}
			sameSources(t, orig, back)

			if _, err := build(dir); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(wpkg)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Errorf("%s.wpkg differs after join and split; entries that differ: %v", m.Name, diffEntries(t, before, after))
			}
		})
	}
}

// TestParitySingleFiles checks each example .widget survives split and join
// unchanged, and that building one differs from building its split folder
// only in server.wasm, by the //line positions that point into the .widget.
func TestParitySingleFiles(t *testing.T) {
	needParity(t)
	files, _ := filepath.Glob(filepath.Join(examplesDir(t), "*.widget"))
	if len(files) == 0 {
		t.Fatal("no example .widget files")
	}
	for _, file := range files {
		base := strings.TrimSuffix(filepath.Base(file), ".widget")
		t.Run(base, func(t *testing.T) {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(filepath.Dir(file), ".widgetc", "parity-"+base)
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(dir) })
			if err := split(file, dir); err != nil {
				t.Fatal(err)
			}
			text, err := joinText(dir, base)
			if err != nil {
				t.Fatal(err)
			}
			if text != string(raw) {
				t.Errorf("join(split(%s)) differs; is it in the form join writes?\n%s", filepath.Base(file), text)
			}

			s, err := readWidgetFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var name string
			if f, ok := s.get("widget.toml"); ok {
				var m manifest
				if _, err := toml.Decode(f.body, &m); err != nil {
					t.Fatal(err)
				}
				name = m.Name
			}
			wpkg := filepath.Join(filepath.Dir(file), name+".wpkg")
			committed, err := os.ReadFile(wpkg)
			if err != nil {
				t.Fatal(err)
			}
			if err := buildSingleFile(file, false); err != nil {
				t.Fatal(err)
			}
			plain, _ := os.ReadFile(wpkg)
			// Last, so the package left behind is the real one.
			if err := buildSingleFile(file, true); err != nil {
				t.Fatal(err)
			}
			annotated, _ := os.ReadFile(wpkg)
			if d := diffEntries(t, plain, annotated); len(d) > 1 || (len(d) == 1 && d[0] != "server.wasm") {
				t.Errorf("annotating changed more than server.wasm: %v", d)
			}
			if !bytes.Equal(committed, annotated) {
				t.Errorf("rebuilding %s changed it: %v", filepath.Base(wpkg), diffEntries(t, committed, annotated))
			}
		})
	}
}

// TestFixture builds testdata/multi.widget, which uses what only <file>
// blocks can carry, and runs its tests.
func TestFixture(t *testing.T) {
	needParity(t)
	src, err := os.ReadFile("testdata/multi.widget")
	if err != nil {
		t.Fatal(err)
	}
	// Inside the module (so it has an import path), and ignored by git.
	work, err := filepath.Abs("testdata/.widgetc/e2e")
	if err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(work)
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(work)) })
	file := filepath.Join(work, "multi.widget")
	write(t, file, string(src))

	t.Run("round trip", func(t *testing.T) {
		dir := filepath.Join(work, "split")
		if err := split(file, dir); err != nil {
			t.Fatal(err)
		}
		text, err := joinText(dir, "multi")
		if err != nil {
			t.Fatal(err)
		}
		if text != string(src) {
			t.Errorf("join(split(multi.widget)) differs:\n%s", text)
		}
		os.RemoveAll(dir)
	})

	t.Run("build", func(t *testing.T) {
		if err := buildSingleFile(file, true); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(work, "multi.wpkg"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := host.LoadPackage(b); err != nil {
			t.Fatal(err)
		}
		entries := zipEntries(t, b)
		man := string(entries["manifest.json"])
		for _, p := range []string{"multi/v1/multi.proto", "multi/common/v1/common.proto"} {
			if !strings.Contains(man, p) {
				t.Errorf("manifest lacks %s:\n%s", p, man)
			}
		}
		if !bytes.Contains(entries["view/view.js"], []byte("multi-localdep")) {
			t.Error("view.js lacks the local dependency")
		}
		// view/plugins.ts, carried in a <file> block, ran in the build.
		if !bytes.Contains(entries["view/view.js"], []byte("multi-plugin-ok")) {
			t.Error("view.js lacks what view/plugins.ts supplies")
		}
		// The manifest's icon, resolved from the view's @widgetd/sdk/icons.
		if !strings.Contains(man, `"name": "layers"`) || !strings.Contains(man, `"shapes"`) {
			t.Errorf("manifest lacks the icon:\n%s", man)
		}
		if _, ok := entries["view/view.css"]; !ok {
			t.Error("no view.css")
		}
		// The embedding beside it compiles.
		if _, err := os.Stat(filepath.Join(work, "multi_widget_gen.go")); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("go", "build", "./testdata/.widgetc/e2e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go build: %v\n%s", err, out)
		}
	})

	t.Run("test", func(t *testing.T) {
		if err := testAny(file); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("errors point into the .widget", func(t *testing.T) {
		broken := filepath.Join(work, "broken.widget")
		write(t, broken, strings.Replace(string(src), "return n + 1", "return n + undefinedName", 1))
		dir, err := prepareSingleFile(broken, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := prepare(dir); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("go", "build", "./server")
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatal("broken server built")
		}
		i := bytes.Index(src, []byte("return n + 1"))
		want := "/widget/broken.widget:" + strconv.Itoa(strings.Count(string(src)[:i], "\n")+1)
		if !strings.Contains(string(out), want) {
			t.Errorf("want %q in:\n%s", want, out)
		}
	})
}

func sameSources(t *testing.T, want, got map[string][]byte) {
	t.Helper()
	var keys []string
	for p := range want {
		keys = append(keys, p)
	}
	for p := range got {
		if _, ok := want[p]; !ok {
			keys = append(keys, p)
		}
	}
	slices.Sort(keys)
	for _, p := range keys {
		w, inW := want[p]
		g, inG := got[p]
		switch {
		case !inG:
			t.Errorf("%s: lost", p)
		case !inW:
			t.Errorf("%s: appeared", p)
		case !bytes.Equal(w, g):
			t.Errorf("%s: changed\n--- want\n%s\n--- got\n%s", p, w, g)
		}
	}
}

func zipEntries(t *testing.T, b []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name], err = io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// diffEntries names the entries that differ between two packages.
func diffEntries(t *testing.T, a, b []byte) []string {
	t.Helper()
	ea, eb := zipEntries(t, a), zipEntries(t, b)
	var d []string
	for n, x := range ea {
		if y, ok := eb[n]; !ok || !bytes.Equal(x, y) {
			d = append(d, n)
		}
	}
	for n := range eb {
		if _, ok := ea[n]; !ok {
			d = append(d, n)
		}
	}
	slices.Sort(d)
	return d
}
