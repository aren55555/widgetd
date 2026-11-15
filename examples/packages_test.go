package examples_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aren55555/widgetd/examples"
	"github.com/aren55555/widgetd/host"
)

// A new example, folder or .widget file, must be added to Packages, or
// embedders that offer every official widget would silently miss it.
func TestPackagesListsEveryExample(t *testing.T) {
	var want []string
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		switch {
		case e.IsDir():
			if _, err := os.Stat(filepath.Join(e.Name(), "widget.toml")); err == nil {
				want = append(want, e.Name())
			}
		case strings.HasSuffix(e.Name(), ".widget"):
			want = append(want, strings.TrimSuffix(e.Name(), ".widget"))
		}
	}
	specs, err := host.LoadWidgets(examples.Packages(), "")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range specs {
		got = append(got, s.Name)
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("Packages() has %v, examples are %v", got, want)
	}
}
