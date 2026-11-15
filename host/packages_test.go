package host

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"google.golang.org/protobuf/reflect/protoreflect"

	testwidgetv1 "github.com/aren55555/widgetd/internal/testwidget/gen/testwidget/v1"
)

func testPackage(t *testing.T, name, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	m := Manifest{Name: name, Version: version, Config: "testwidget.v1.Config"}
	view := fstest.MapFS{"view.js": {Data: []byte("/* v */")}}
	if err := WritePackage(&buf, m, []protoreflect.FileDescriptor{testwidgetv1.File_testwidget_v1_testwidget_proto}, testWasm(t), view); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Installed packages join the linked-in ones, and win a name clash in the
// linked-in one's place.
func TestLoadWidgets(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(t.TempDir(), "b.wpkg")
	if err := os.WriteFile(src, testPackage(t, "b", "2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallPackage(dir, src); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "c.wpkg"), testPackage(t, "c", "1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a package"), 0o644); err != nil {
		t.Fatal(err)
	}
	specs, err := LoadWidgets([][]byte{testPackage(t, "a", "1"), testPackage(t, "b", "1")}, dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range specs {
		got = append(got, s.Name+"@"+s.Version)
	}
	if want := "a@1 b@2 c@1"; strings.Join(got, " ") != want {
		t.Fatalf("got %s, want %s", strings.Join(got, " "), want)
	}
}

func TestLoadWidgetsNoDir(t *testing.T) {
	for _, dir := range []string{"", filepath.Join(t.TempDir(), "missing")} {
		specs, err := LoadWidgets([][]byte{testPackage(t, "a", "1")}, dir)
		if err != nil || len(specs) != 1 {
			t.Fatalf("dir %q: %d specs, %v", dir, len(specs), err)
		}
	}
}

func TestLoadWidgetsBadPackage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.wpkg"), []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWidgets(nil, dir); err == nil {
		t.Fatal("a broken installed package loaded")
	}
	if _, err := InstallPackage(dir, filepath.Join(dir, "bad.wpkg")); err == nil {
		t.Fatal("a broken package installed")
	}
}
