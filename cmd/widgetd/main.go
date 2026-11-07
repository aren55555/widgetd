// Command widgetd runs a widget host on its own: on a small VPS, an old PC,
// or a Raspberry Pi.
//
//	widgetd -public-url https://widgets.example.com -acme-domain widgets.example.com
//	widgetd -public-url http://localhost:8080          # development
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

	bitcoinspiral "github.com/aren55555/widgetd/examples/bitcoin-spiral"
	"github.com/aren55555/widgetd/host"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

// builtin are the widgets compiled into this binary. Runtime-installed
// packages arrive in M1b.
func builtin() []widget.Spec {
	return []widget.Spec{bitcoinspiral.Widget()}
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

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log, *addr, *stateDir, *publicURL, *configPath, *acmeDomain); err != nil {
		log.Error("widgetd", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, addr, stateDir, publicURL, configPath, acmeDomain string) error {
	specs := builtin()
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
