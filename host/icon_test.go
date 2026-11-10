package host

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"google.golang.org/protobuf/reflect/protoreflect"

	widgetdv1 "github.com/aren55555/widgetd/gen/widgetd/v1"
	testwidgetv1 "github.com/aren55555/widgetd/internal/testwidget/gen/testwidget/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

// cloudSun is Lucide's cloud-sun, plus a filled circle as some icons have.
func cloudSun() *widget.Icon {
	return &widget.Icon{Name: "cloud-sun", Shapes: []widget.IconShape{
		{Tag: "path", Attrs: map[string]string{"d": "M12 2v2"}},
		{Tag: "path", Attrs: map[string]string{"d": "M13 22H7a5 5 0 1 1 4.9-6H13a3 3 0 0 1 0 6Z"}},
		{Tag: "circle", Attrs: map[string]string{"cx": "12", "cy": "12", "r": ".5", "fill": "currentColor"}},
		{Tag: "rect", Attrs: map[string]string{"x": "3", "y": "3", "width": "18", "height": "18", "rx": "2"}},
		{Tag: "polyline", Attrs: map[string]string{"points": "22 12 18 12 15 21 9 3 6 12 2 12"}},
		{Tag: "line", Attrs: map[string]string{"x1": "1e-3", "y1": "-2", "x2": "+3", "y2": "4"}},
	}}
}

func TestCheckIcon(t *testing.T) {
	if err := checkIcon(nil); err != nil {
		t.Fatalf("no icon: %v", err)
	}
	if err := checkIcon(cloudSun()); err != nil {
		t.Fatalf("Lucide icon refused: %v", err)
	}
	bad := map[string]func(*widget.Icon){
		"script tag":    func(ic *widget.Icon) { ic.Shapes[0].Tag = "script" },
		"foreignObject": func(ic *widget.Icon) { ic.Shapes[0].Tag = "foreignObject" },
		"use (href)": func(ic *widget.Icon) {
			ic.Shapes[0] = widget.IconShape{Tag: "use", Attrs: map[string]string{"href": "#x"}}
		},
		"event handler":    func(ic *widget.Icon) { ic.Shapes[0].Attrs["onload"] = "1" },
		"style":            func(ic *widget.Icon) { ic.Shapes[0].Attrs["style"] = "x" },
		"href on a path":   func(ic *widget.Icon) { ic.Shapes[0].Attrs["href"] = "javascript:1" },
		"markup in d":      func(ic *widget.Icon) { ic.Shapes[0].Attrs["d"] = `M0 0"/><script>alert(1)</script>` },
		"url() in d":       func(ic *widget.Icon) { ic.Shapes[0].Attrs["d"] = "url(x)" },
		"fill colour":      func(ic *widget.Icon) { ic.Shapes[2].Attrs["fill"] = "url(#x)" },
		"wrong shape attr": func(ic *widget.Icon) { ic.Shapes[0].Attrs["cx"] = "1" },
		"huge value":       func(ic *widget.Icon) { ic.Shapes[0].Attrs["d"] = strings.Repeat("1", maxIconValue+1) },
		"no shapes":        func(ic *widget.Icon) { ic.Shapes = nil },
		"too many shapes": func(ic *widget.Icon) {
			for len(ic.Shapes) <= maxIconShapes {
				ic.Shapes = append(ic.Shapes, ic.Shapes[0])
			}
		},
		"bad name":   func(ic *widget.Icon) { ic.Name = "Cloud Sun" },
		"empty name": func(ic *widget.Icon) { ic.Name = "" },
	}
	for name, spoil := range bad {
		t.Run(name, func(t *testing.T) {
			ic := cloudSun()
			spoil(ic)
			if err := checkIcon(ic); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// The icon reaches every page that draws a widget's title: the board's card
// headers and instance statuses, and the admin widget list.
func TestIconReachesPages(t *testing.T) {
	h := newHarness(t, func(s *widget.Spec) { s.Icon = cloudSun() })
	mustMount(t, h.host(), "main", widget.MountOptions{Title: "Test"})

	f := h.open(h.deviceCookie("d1"))
	if ic := f.nextInstance("one").GetIcon(); ic.GetName() != "cloud-sun" || len(ic.GetShapes()) != 6 {
		t.Fatalf("instance status icon = %v", ic)
	}
	m := f.next().GetMounted()
	if ic := m.GetIcon(); ic.GetName() != "cloud-sun" || ic.GetShapes()[0].GetAttrs()["d"] != "M12 2v2" {
		t.Fatalf("mounted icon = %v", ic)
	}

	res, err := h.adminClient().ListWidgets(context.Background(), withCookie(&widgetdv1.ListWidgetsRequest{}, h.adminCookie()))
	if err != nil {
		t.Fatal(err)
	}
	if ic := res.Msg.GetWidgets()[0].GetIcon(); ic.GetName() != "cloud-sun" {
		t.Fatalf("admin widget icon = %v", ic)
	}

	// A widget without one sends none.
	plain := newHarness(t)
	mustMount(t, plain.host(), "main", widget.MountOptions{})
	if ic := plain.open(plain.deviceCookie("d1")).next().GetMounted().GetIcon(); ic != nil {
		t.Fatalf("icon without one declared: %v", ic)
	}
}

// A native widget with a bad icon is refused like a bad package.
func TestSpecIconChecked(t *testing.T) {
	ic := cloudSun()
	ic.Shapes[0].Tag = "script"
	spec := widget.Spec{Name: "w", Version: "1", Icon: ic, View: fstest.MapFS{"view.js": {}},
		Files: []protoreflect.FileDescriptor{testwidgetv1.File_testwidget_v1_testwidget_proto},
		Run:   func(ctx context.Context, _ widget.Host) error { <-ctx.Done(); return nil }}
	if _, err := New(Options{StateDir: t.TempDir(), PublicURL: "http://localhost", Widgets: []widget.Spec{spec}}); err == nil {
		t.Fatal("accepted a widget whose icon has a <script>")
	}
}

func TestPackageIcon(t *testing.T) {
	view := fstest.MapFS{"view.js": {Data: []byte("/* v */")}}
	files := []protoreflect.FileDescriptor{testwidgetv1.File_testwidget_v1_testwidget_proto}
	pack := func(ic *widget.Icon) []byte {
		var buf bytes.Buffer
		m := Manifest{Name: "pkgd", Version: "1", Icon: ic}
		if err := WritePackage(&buf, m, files, testWasm(t), view); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}

	spec, err := LoadPackage(pack(cloudSun()))
	if err != nil {
		t.Fatal(err)
	}
	if spec.Icon == nil || spec.Icon.Name != "cloud-sun" || len(spec.Icon.Shapes) != 6 || spec.Icon.Shapes[0].Attrs["d"] != "M12 2v2" {
		t.Fatalf("icon = %+v", spec.Icon)
	}
	if p := iconProto(spec.Icon); p.GetName() != "cloud-sun" || p.GetShapes()[2].GetAttrs()["fill"] != "currentColor" {
		t.Fatalf("proto = %v", p)
	}

	evil := cloudSun()
	evil.Shapes[0].Attrs["onload"] = "alert(1)"
	if _, err := LoadPackage(pack(evil)); err == nil || !strings.Contains(err.Error(), "onload") {
		t.Fatalf("package with a script in its icon loaded: %v", err)
	}
}
