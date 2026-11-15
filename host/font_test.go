package host

import (
	"bytes"
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"google.golang.org/protobuf/reflect/protoreflect"

	testwidgetv1 "github.com/aren55555/widgetd/internal/testwidget/gen/testwidget/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

func testFont() Font {
	return Font{
		Family: "Test Sans", Source: "@fontsource/test-sans@5.0.1", License: "fonts/test-sans-LICENSE.txt",
		Files: []string{"fonts/test-sans-latin-700-normal.woff2"},
	}
}

func fontView() fstest.MapFS {
	return fstest.MapFS{
		"view.js":                                {Data: []byte("/* v */")},
		"view.css":                               {Data: []byte(`@font-face{font-family:"Test Sans"}`)},
		"fonts/test-sans-LICENSE.txt":            {Data: []byte("OFL")},
		"fonts/test-sans-latin-700-normal.woff2": {Data: []byte("wOF2")},
	}
}

func TestPackageFonts(t *testing.T) {
	files := []protoreflect.FileDescriptor{testwidgetv1.File_testwidget_v1_testwidget_proto}
	pack := func(f Font, view fs.FS) []byte {
		var buf bytes.Buffer
		if err := WritePackage(&buf, Manifest{Name: "pkgd", Version: "1", Fonts: []Font{f}}, files, testWasm(t), view); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}

	spec, err := LoadPackage(pack(testFont(), fontView()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat(spec.View, "fonts/test-sans-latin-700-normal.woff2"); err != nil {
		t.Fatal(err)
	}

	missing := fontView()
	delete(missing, "fonts/test-sans-latin-700-normal.woff2")
	outside, notFont, noLicence := testFont(), testFont(), testFont()
	outside.Files = []string{"view.js"}
	notFont.Files = []string{"fonts/test-sans-LICENSE.txt"}
	noLicence.License = "fonts/missing.txt"
	for name, c := range map[string]struct {
		f    Font
		view fs.FS
	}{
		"missing file":    {testFont(), missing},
		"outside fonts/":  {outside, fontView()},
		"not a .woff2":    {notFont, fontView()},
		"missing licence": {noLicence, fontView()},
	} {
		if _, err := LoadPackage(pack(c.f, c.view)); err == nil || !strings.Contains(err.Error(), "font") {
			t.Errorf("%s: loaded: %v", name, err)
		}
	}
}

// A bundled font is served beside the view with its type, and loadable from
// the card's opaque origin.
func TestServeFont(t *testing.T) {
	spec := widget.Spec{Name: "fonty", Version: "1", View: fontView(),
		Files: []protoreflect.FileDescriptor{testwidgetv1.File_testwidget_v1_testwidget_proto},
		Run:   func(ctx context.Context, _ widget.Host) error { <-ctx.Done(); return nil }}
	srv, err := New(Options{StateDir: t.TempDir(), PublicURL: "http://localhost", Widgets: []widget.Spec{spec}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close(context.Background()) })
	digest := srv.widgets["fonty"].digest
	for file, want := range map[string]string{
		"fonts/test-sans-latin-700-normal.woff2": "font/woff2",
		"fonts/test-sans-LICENSE.txt":            "text/plain; charset=utf-8",
	} {
		rec := httptest.NewRecorder()
		srv.serveAsset(rec, httptest.NewRequest(http.MethodGet, "/", http.NoBody), "fonty", digest, file)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != want || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("%s: %d %v", file, rec.Code, rec.Header())
		}
	}
}
