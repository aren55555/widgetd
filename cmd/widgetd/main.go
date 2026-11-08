// Command widgetd runs a widget host on its own: on a small VPS, an old PC,
// or a Raspberry Pi.
//
//	widgetd -public-url https://widgets.example.com -acme-domain widgets.example.com
//	widgetd -public-url http://localhost:8080          # development
//
// Widgets are the packages built in, plus any installed with
//
//	widgetd -state ./data install some-widget.wpkg
//
// Instances come from a TOML file (-config):
//
//	[[instance]]
//	id     = "btc"
//	widget = "bitcoin-spiral"
//	config = '{"currency": "CURRENCY_EUR"}'   # the widget's Config, as protojson
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"golang.org/x/crypto/acme/autocert"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/aren55555/widgetd/examples"
	bitcoinspiral "github.com/aren55555/widgetd/examples/bitcoin-spiral"
	sportsgame "github.com/aren55555/widgetd/examples/sports-game"
	"github.com/aren55555/widgetd/host"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

// builtin are the packages shipped inside this binary. Packages installed
// with `widgetd install` sit beside them, and win on a name clash.
var builtin = [][]byte{bitcoinspiral.Package, sportsgame.Package, examples.Clock}

// loadWidgets reads the built-in packages, then every .wpkg in dir.
func loadWidgets(dir string) ([]widget.Spec, error) {
	byName := map[string]widget.Spec{}
	var order []string
	add := func(b []byte, from string) error {
		spec, err := host.LoadPackage(b)
		if err != nil {
			return fmt.Errorf("%s: %w", from, err)
		}
		if _, ok := byName[spec.Name]; !ok {
			order = append(order, spec.Name)
		}
		byName[spec.Name] = spec
		return nil
	}
	for _, b := range builtin {
		if err := add(b, "built-in package"); err != nil {
			return nil, err
		}
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.wpkg"))
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		if err := add(b, p); err != nil {
			return nil, err
		}
	}
	out := make([]widget.Spec, 0, len(order))
	for _, n := range order {
		out = append(out, byName[n])
	}
	return out, nil
}

// install checks a package loads, then copies it into dir as <name>.wpkg.
// Running instances pick it up on the next start.
func install(dir, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	spec, err := host.LoadPackage(b)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".install-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	dst := filepath.Join(dir, spec.Name+".wpkg")
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return err
	}
	fmt.Printf("installed %s %s (asks for: %v) to %s; restart widgetd to load it\n", spec.Name, spec.Version, spec.Network.Hosts, dst)
	return nil
}

type config struct {
	Instance []struct {
		ID     string `toml:"id"`
		Widget string `toml:"widget"`
		Config string `toml:"config"`
	} `toml:"instance"`
}

func main() {
	addr := flag.String("addr", ":8080", "listen address (plain HTTP; ignored with -acme-domain)")
	stateDir := flag.String("state", "./data", "state directory")
	publicURL := flag.String("public-url", "http://localhost:8080", "the HTTPS origin admin pages and QR codes use")
	configPath := flag.String("config", "widgetd.toml", "instances file")
	acmeDomain := flag.String("acme-domain", "", "serve HTTPS on :443 with a Let's Encrypt certificate for this domain")
	flag.Parse()

	if flag.Arg(0) == "install" {
		if flag.NArg() != 2 {
			fmt.Fprintln(os.Stderr, "usage: widgetd [-state dir] install <file.wpkg>")
			os.Exit(2)
		}
		if err := install(filepath.Join(*stateDir, "packages"), flag.Arg(1)); err != nil {
			fmt.Fprintln(os.Stderr, "widgetd install:", err)
			os.Exit(1)
		}
		return
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log, *addr, *stateDir, *publicURL, *configPath, *acmeDomain); err != nil {
		log.Error("widgetd", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, addr, stateDir, publicURL, configPath, acmeDomain string) error {
	specs, err := loadWidgets(filepath.Join(stateDir, "packages"))
	if err != nil {
		return err
	}
	instances, err := loadInstances(configPath, specs)
	if err != nil {
		return err
	}
	srv, err := host.New(host.Options{
		StateDir:  stateDir,
		PublicURL: publicURL,
		Widgets:   specs,
		Instances: instances,
		Logger:    log,
	})
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	srv.Register(mux)
	mux.HandleFunc("GET /{$}", index)
	srv.Start()

	hs := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	var redirect *http.Server
	if acmeDomain != "" {
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(acmeDomain),
			Cache:      autocert.DirCache(filepath.Join(stateDir, "acme")),
		}
		hs.Addr = ":443"
		hs.TLSConfig = &tls.Config{GetCertificate: m.GetCertificate, MinVersion: tls.VersionTLS12}
		// :80 answers ACME challenges and sends everything else to HTTPS.
		redirect = &http.Server{Addr: ":80", Handler: m.HTTPHandler(nil), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			if err := redirect.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("acme listener", "err", err)
			}
		}()
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("widgetd listening", "addr", hs.Addr, "public_url", publicURL)
		if hs.TLSConfig != nil {
			errc <- hs.ListenAndServeTLS("", "")
		} else {
			errc <- hs.ListenAndServe()
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-quit:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hs.Shutdown(ctx)
	if redirect != nil {
		redirect.Shutdown(ctx)
	}
	return srv.Close(ctx)
}

func loadInstances(path string, specs []widget.Spec) ([]host.Instance, error) {
	var c config
	if _, err := toml.DecodeFile(path, &c); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	byName := map[string]widget.Spec{}
	for _, s := range specs {
		byName[s.Name] = s
	}
	var out []host.Instance
	for _, in := range c.Instance {
		spec, ok := byName[in.Widget]
		if !ok {
			return nil, fmt.Errorf("%s: instance %s: no widget %q", path, in.ID, in.Widget)
		}
		var cfg proto.Message
		if in.Config != "" {
			if spec.Config == nil {
				return nil, fmt.Errorf("%s: instance %s: widget %s takes no config", path, in.ID, in.Widget)
			}
			cfg = spec.Config.ProtoReflect().New().Interface()
			if err := protojson.Unmarshal([]byte(in.Config), cfg); err != nil {
				return nil, fmt.Errorf("%s: instance %s config: %w", path, in.ID, err)
			}
		}
		out = append(out, host.Instance{ID: in.ID, Widget: in.Widget, Config: cfg})
	}
	return out, nil
}

func index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>widgets</title>
<style>
  :root { color-scheme: dark; }
  body { margin: 0; padding: 16px; background: #09090b; }
  widget-board { --wd-surface: #18181b; --wd-edge: #27272a; --wd-ink: #e4e4e7; }
</style>
<script type="module" src="/widgetd/board.js"></script>
</head><body><widget-board></widget-board></body></html>`)
}
