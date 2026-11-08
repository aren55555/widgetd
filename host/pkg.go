package host

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/aren55555/widgetd/sdk/go/widget"
)

// A widget package (.wpkg) is a zip:
//
//	manifest.json   Manifest
//	schema.binpb    FileDescriptorSet: the widget's protos and their imports
//	server.wasm     the server half, a WASI preview 1 command
//	view/view.js    the view, and anything else under view/
//
// widgetc builds them. LoadPackage turns one into a widget.Spec the host
// runs sandboxed; the host never needs the widget's source or generated code.

// Manifest describes a packaged widget.
type Manifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Title   string `json:"title,omitempty"`
	// Config is the fully qualified name of the instance configuration
	// message, if the widget takes one.
	Config  string                `json:"config,omitempty"`
	Network widget.NetworkRequest `json:"network"`
	// Modes are the helper modes server.wasm implements ("choices", "verify").
	Modes []string `json:"modes,omitempty"`
	// Secrets are the Config fields that are write-only.
	Secrets []string `json:"secrets,omitempty"`
	// SignIn, if the widget uses an account.
	SignIn *widget.SignIn `json:"sign_in,omitempty"`
	// Protos are the widget's own files within the descriptor set (the rest
	// are their imports).
	Protos []string `json:"protos"`
}

const (
	maxPackageFile  = 64 << 20
	maxPackageTotal = 128 << 20
)

// LoadPackage reads a .wpkg into a Spec.
func LoadPackage(b []byte) (widget.Spec, error) {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return widget.Spec{}, fmt.Errorf("package: %w", err)
	}
	var total uint64
	for _, f := range zr.File {
		if f.UncompressedSize64 > maxPackageFile {
			return widget.Spec{}, fmt.Errorf("package: %s is over %d bytes", f.Name, maxPackageFile)
		}
		total += f.UncompressedSize64
		if !fs.ValidPath(f.Name) {
			return widget.Spec{}, fmt.Errorf("package: bad path %q", f.Name)
		}
	}
	if total > maxPackageTotal {
		return widget.Spec{}, fmt.Errorf("package: over %d bytes unpacked", maxPackageTotal)
	}

	read := func(name string) ([]byte, error) {
		f, err := zr.Open(name)
		if err != nil {
			return nil, fmt.Errorf("package: %w", err)
		}
		defer f.Close()
		return io.ReadAll(io.LimitReader(f, maxPackageFile+1))
	}

	raw, err := read("manifest.json")
	if err != nil {
		return widget.Spec{}, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return widget.Spec{}, fmt.Errorf("package manifest: %w", err)
	}

	raw, err = read("schema.binpb")
	if err != nil {
		return widget.Spec{}, err
	}
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(raw, &set); err != nil {
		return widget.Spec{}, fmt.Errorf("package schema: %w", err)
	}
	files, err := protodesc.NewFiles(&set)
	if err != nil {
		return widget.Spec{}, fmt.Errorf("package schema: %w", err)
	}
	if len(m.Protos) == 0 {
		return widget.Spec{}, errors.New("package manifest lists no protos")
	}
	var own []protoreflect.FileDescriptor
	for _, p := range m.Protos {
		fd, err := files.FindFileByPath(p)
		if err != nil {
			return widget.Spec{}, fmt.Errorf("package schema: %s: %w", p, err)
		}
		own = append(own, fd)
	}

	var cfg proto.Message
	if m.Config != "" {
		d, err := files.FindDescriptorByName(protoreflect.FullName(m.Config))
		md, ok := d.(protoreflect.MessageDescriptor)
		if err != nil || !ok {
			return widget.Spec{}, fmt.Errorf("package: config %s is not a message in the schema", m.Config)
		}
		cfg = dynamicpb.NewMessage(md)
	}

	wasm, err := read("server.wasm")
	if err != nil {
		return widget.Spec{}, err
	}
	view, err := fs.Sub(zr, "view")
	if err != nil {
		return widget.Spec{}, err
	}
	return widget.Spec{
		Name:    m.Name,
		Version: m.Version,
		Title:   m.Title,
		Modes:   m.Modes,
		Secrets: m.Secrets,
		SignIn:  m.SignIn,
		Files:   own,
		Config:  cfg,
		Wasm:    wasm,
		View:    view,
		Network: m.Network,
	}, nil
}

// WritePackage writes a .wpkg. files are the widget's own protos (their
// imports are included in the set automatically); view is the built view.
func WritePackage(w io.Writer, m Manifest, files []protoreflect.FileDescriptor, wasm []byte, view fs.FS) error {
	zw := zip.NewWriter(w)
	add := func(name string, b []byte) error {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			return err
		}
		_, err = f.Write(b)
		return err
	}

	m.Protos = nil
	for _, fd := range files {
		m.Protos = append(m.Protos, fd.Path())
	}
	mj, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := add("manifest.json", mj); err != nil {
		return err
	}

	sb, err := proto.MarshalOptions{Deterministic: true}.Marshal(descriptorSet(files))
	if err != nil {
		return err
	}
	if err := add("schema.binpb", sb); err != nil {
		return err
	}
	if err := add("server.wasm", wasm); err != nil {
		return err
	}

	var names []string
	err = fs.WalkDir(view, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			names = append(names, p)
		}
		return err
	})
	if err != nil {
		return err
	}
	slices.Sort(names)
	for _, n := range names {
		b, err := fs.ReadFile(view, n)
		if err != nil {
			return err
		}
		if err := add(path.Join("view", n), b); err != nil {
			return err
		}
	}
	return zw.Close()
}
