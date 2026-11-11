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
	icon, err := bundleView(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if icon != nil {
		t.Fatalf("icon without one asked for: %+v", icon)
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
	if _, err := bundleView(dir, ""); err == nil {
		t.Fatal("built a view that emits a file the card can't load")
	}
}

func TestBundleViewRefusesBadPlugins(t *testing.T) {
	dir := viewDir(t, map[string]string{
		"view.ts":    `console.log(1);`,
		"plugins.ts": `export default { name: "not an array", setup() {} };`,
	})
	if _, err := bundleView(dir, ""); err == nil {
		t.Fatal("accepted plugins.ts not exporting an array")
	}
}

func TestBundleViewIcon(t *testing.T) {
	view := map[string]string{"view.ts": `console.log(1);`}

	for name, wantShapes := range map[string]int{"cloud-sun": 6, "arrow-down-0-1": 5} {
		dir := viewDir(t, view)
		icon, err := bundleView(dir, name)
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
		if _, err := bundleView(viewDir(t, view), bad); err == nil {
			t.Errorf("icon %q accepted", bad)
		}
	}
}
