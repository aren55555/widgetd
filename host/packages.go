package host

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/aren55555/widgetd/sdk/go/widget"
)

// LoadWidgets loads the widgets a host offers: each of pkgs in order, then
// every .wpkg file in dir (by name), as InstallPackage puts them there. A
// later package replaces an earlier one with the same widget name, in the
// earlier one's place, so an installed package overrides a built-in one.
//
// pkgs is typically the widgets an embedder links in, such as
// examples.Packages() plus any third-party widget's generated Package. A
// missing dir, or an empty one, adds nothing; so does dir == "".
func LoadWidgets(pkgs [][]byte, dir string) ([]widget.Spec, error) {
	byName := map[string]int{}
	var out []widget.Spec
	add := func(b []byte, from string) error {
		spec, err := LoadPackage(b)
		if err != nil {
			return fmt.Errorf("%s: %w", from, err)
		}
		if i, ok := byName[spec.Name]; ok {
			out[i] = spec
			return nil
		}
		byName[spec.Name] = len(out)
		out = append(out, spec)
		return nil
	}
	for i, b := range pkgs {
		if err := add(b, fmt.Sprintf("package %d", i)); err != nil {
			return nil, err
		}
	}
	if dir == "" {
		return out, nil
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("installed packages: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".wpkg") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p) //nolint:gosec // p is a file in the operator's own packages dir
		if err != nil {
			return nil, fmt.Errorf("installed package: %w", err)
		}
		if err := add(b, p); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// InstallPackage checks that the package at path loads, then copies it into
// dir as <name>.wpkg, where LoadWidgets finds it. It replaces any package
// already installed under that name. A running host picks it up when it next
// starts. It returns the installed widget.
func InstallPackage(dir, path string) (widget.Spec, error) {
	b, err := os.ReadFile(path) //nolint:gosec // the operator names the package to install
	if err != nil {
		return widget.Spec{}, fmt.Errorf("install: %w", err)
	}
	spec, err := LoadPackage(b)
	if err != nil {
		return widget.Spec{}, fmt.Errorf("install %s: %w", path, err)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return widget.Spec{}, fmt.Errorf("install: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".install-*")
	if err != nil {
		return widget.Spec{}, fmt.Errorf("install: %w", err)
	}
	// After the rename there is nothing left to remove, so its error says nothing.
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close() // the write error is the one worth reporting
		return widget.Spec{}, fmt.Errorf("install: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return widget.Spec{}, fmt.Errorf("install: %w", err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, spec.Name+".wpkg")); err != nil {
		return widget.Spec{}, fmt.Errorf("install: %w", err)
	}
	return spec, nil
}
