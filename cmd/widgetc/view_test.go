package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// viewDir writes a widget's view/ files into a fresh folder under testdata,
// inside the repo, so the view resolves @widgetd/sdk from the workspace as a
// real widget would.
func viewDir(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not on PATH")
	}
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("testdata", "view-") //nolint:usetesting // must be inside the repo, see above
	if err != nil {
		t.Fatal(err)
	}
	dir, _ = filepath.Abs(dir)
	t.Cleanup(func() { os.RemoveAll(dir) })
	for name, body := range files {
		p := filepath.Join(dir, "view", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func readDist(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "dist", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBundleViewPluginsAndProduction(t *testing.T) {
	dir := viewDir(t, map[string]string{
		"view.ts": `import msg from "virtual:hello";
console.log(msg, process.env.NODE_ENV);`,
		"plugins.ts": `import type { BunPlugin } from "bun";
const hello: BunPlugin = {
  name: "hello",
  setup(build) {
    build.onResolve({ filter: /^virtual:hello$/ }, () => ({ path: "hello", namespace: "v" }));
    build.onLoad({ filter: /.*/, namespace: "v" }, () => ({ contents: 'export default "hi from a plugin";', loader: "js" }));
  },
};
export default [hello];`,
		"view.css": "p{color:red}",
	})
	x, err := bundleView(dir, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if x.Icon != nil || x.Fonts != nil {
		t.Fatalf("extras without any asked for: %+v", x)
	}
	js := readDist(t, dir, "view.js")
	if !strings.Contains(js, "hi from a plugin") {
		t.Errorf("plugin not applied: %s", js)
	}
	if !strings.Contains(js, `"production"`) || strings.Contains(js, "process.env") {
		t.Errorf("NODE_ENV not production: %s", js)
	}
	if strings.Contains(js, "import ") || strings.Contains(js, "export ") {
		t.Errorf("not a classic script: %s", js)
	}
	if css := readDist(t, dir, "view.css"); css != "p{color:red}" {
		t.Errorf("view.css = %q", css)
	}
}

func TestBundleViewRefusesExtraOutputs(t *testing.T) {
	dir := viewDir(t, map[string]string{
		"view.ts":  `import logo from "./logo.png"; console.log(logo);`,
		"logo.png": "not really a png",
	})
	if _, err := bundleView(dir, "", nil); err == nil {
		t.Fatal("built a view that emits a file the card can't load")
	}
}

func TestBundleViewRefusesBadPlugins(t *testing.T) {
	dir := viewDir(t, map[string]string{
		"view.ts":    `console.log(1);`,
		"plugins.ts": `export default { name: "not an array", setup() {} };`,
	})
	if _, err := bundleView(dir, "", nil); err == nil {
		t.Fatal("accepted plugins.ts not exporting an array")
	}
}

func TestBundleViewIcon(t *testing.T) {
	view := map[string]string{"view.ts": `console.log(1);`}

	for name, wantShapes := range map[string]int{"cloud-sun": 6, "arrow-down-0-1": 5} {
		dir := viewDir(t, view)
		x, err := bundleView(dir, name, nil)
		icon := x.Icon
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if icon == nil || icon.Name != name || len(icon.Shapes) != wantShapes {
			t.Fatalf("%s: icon = %+v", name, icon)
		}
		for _, s := range icon.Shapes {
			if s.Tag == "" || len(s.Attrs) == 0 {
				t.Fatalf("%s: shape = %+v", name, s)
			}
		}
	}

	for _, bad := range []string{"not-a-lucide-icon", "Cloud Sun", "cloud_sun"} {
		if _, err := bundleView(viewDir(t, view), bad, nil); err == nil {
			t.Errorf("icon %q accepted", bad)
		}
	}
}

// fakeFontsource is a Fontsource package as the test view's own dependency:
// the per-face CSS naming its files, the files, and the licence.
func fakeFontsource() map[string]string {
	p := "node_modules/@fontsource/test-sans/"
	face := func(subset, weight, style string) string {
		return `/* test-sans-` + subset + `-` + weight + `-` + style + ` */
@font-face {
  font-family: 'Test Sans';
  src: url(./files/test-sans-` + subset + `-` + weight + `-` + style + `.woff2) format('woff2'), url(./files/test-sans-` + subset + `-` + weight + `-` + style + `.woff) format('woff');
}`
	}
	return map[string]string{
		"view.ts":                  `console.log(1);`,
		"view.css":                 `p{font-family:"Test Sans"}`,
		p + "package.json":         `{"name":"@fontsource/test-sans","version":"5.0.1","exports":{"./LICENSE":"./LICENSE","./*":"./*.css"}}`,
		p + "LICENSE":              "SIL Open Font License",
		p + "latin-400.css":        face("latin", "400", "normal"),
		p + "latin-700.css":        face("latin", "700", "normal"),
		p + "latin-700-italic.css": face("latin", "700", "italic"),
		p + "files/test-sans-latin-400-normal.woff2": "w400",
		p + "files/test-sans-latin-700-normal.woff2": "w700",
		p + "files/test-sans-latin-700-italic.woff2": "w700i",
		p + "files/test-sans-latin-700-normal.woff":  "not wanted",
	}
}

func TestBundleViewFonts(t *testing.T) {
	dir := viewDir(t, fakeFontsource())
	x, err := bundleView(dir, "", []fontRequest{{Family: "Test Sans", Weights: []int{700}, Styles: []string{"normal", "italic"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(x.Fonts) != 1 {
		t.Fatalf("fonts = %+v", x.Fonts)
	}
	f := x.Fonts[0]
	want := []string{"fonts/test-sans-latin-700-normal.woff2", "fonts/test-sans-latin-700-italic.woff2"}
	if f.Family != "Test Sans" || f.Source != "@fontsource/test-sans@5.0.1" || f.License != "fonts/test-sans-LICENSE.txt" ||
		strings.Join(f.Files, ",") != strings.Join(want, ",") {
		t.Fatalf("font = %+v", f)
	}
	// Exactly the faces asked for, as .woff2, with the licence.
	for _, p := range append(want, f.License) {
		readDist(t, dir, p)
	}
	for _, p := range []string{"fonts/test-sans-latin-400-normal.woff2", "fonts/test-sans-latin-700-normal.woff"} {
		if _, err := os.Stat(filepath.Join(dir, "dist", p)); err == nil {
			t.Errorf("bundled %s, which wasn't asked for", p)
		}
	}
	// The faces come ahead of the view's own CSS, pointing beside view.css.
	css := readDist(t, dir, "view.css")
	if !strings.HasSuffix(css, `p{font-family:"Test Sans"}`) ||
		!strings.Contains(css, `font-family:"Test Sans";font-style:italic;font-weight:700`) ||
		!strings.Contains(css, `src:url(fonts/test-sans-latin-700-italic.woff2) format("woff2")`) {
		t.Errorf("view.css = %s", css)
	}
}

func TestBundleViewFontsDefaultsAndErrors(t *testing.T) {
	// Defaults: weight 400, normal, latin; and a view without view.css still
	// gets one, for the faces.
	files := fakeFontsource()
	delete(files, "view.css")
	dir := viewDir(t, files)
	x, err := bundleView(dir, "", []fontRequest{{Family: "Test Sans"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := x.Fonts[0].Files; len(got) != 1 || got[0] != "fonts/test-sans-latin-400-normal.woff2" {
		t.Fatalf("files = %v", got)
	}
	if css := readDist(t, dir, "view.css"); !strings.Contains(css, "font-weight:400") {
		t.Errorf("view.css = %s", css)
	}

	for name, fonts := range map[string][]fontRequest{
		"missing weight":  {{Family: "Test Sans", Weights: []int{300}}},
		"missing package": {{Family: "No Such Font"}},
		"bad weight":      {{Family: "Test Sans", Weights: []int{450}}},
		"bad style":       {{Family: "Test Sans", Styles: []string{"oblique"}}},
		"bad family":      {{Family: "test_sans"}},
		"listed twice":    {{Family: "Test Sans"}, {Family: "Test Sans"}},
		"bad subset":      {{Family: "Test Sans", Subsets: []string{"Latin Ext"}}},
	} {
		if _, err := bundleView(viewDir(t, fakeFontsource()), "", fonts); err == nil {
			t.Errorf("%s: accepted %+v", name, fonts)
		}
	}
}
