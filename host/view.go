package host

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/aren55555/widgetd/client"
)

// frameBoot is the script every card frame runs before the widget's own
// code (client/src/frame.ts): it sends the frame's console, and anything the
// view throws, to the server's log. It is inlined, so the frame needs no
// extra request, and allowed by its hash alone.
var frameBoot = mustFrameBoot()

type bootScript struct {
	js template.JS
	// csp is its CSP source, "'sha256-…'"; version is a short hash for URLs.
	csp, version string
}

func mustFrameBoot() bootScript {
	b, err := fs.ReadFile(client.Dist, "dist/frame.js")
	if err != nil {
		panic("widgetd: client/dist/frame.js is missing; run just client-build: " + err.Error())
	}
	js := strings.TrimSpace(string(b))
	if strings.Contains(strings.ToLower(js), "</script") {
		panic("widgetd: client/dist/frame.js can't be inlined: it contains </script")
	}
	sum := sha256.Sum256([]byte(js))
	return bootScript{
		js:      template.JS(js), //nolint:gosec // G203: our own bundle, embedded at build time
		csp:     "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'",
		version: hex.EncodeToString(sum[:])[:12],
	}
}

// frameURL is where a widget's card document is served. It is keyed by
// digest, so it never changes for a given build and can be cached forever;
// the query names the frame bootstrap, which is part of the document too.
func (s *Server) frameURL(w *loadedWidget) string {
	return s.base + "frame/" + w.spec.Name + "/" + w.digest + "?b=" + frameBoot.version
}

// requestOrigin is the origin the browser used to reach us. A card frame is
// an opaque origin, so it can only name the host's assets absolutely.
func requestOrigin(r *http.Request) string {
	scheme := "http"
	if secureRequest(r.Header, r.TLS != nil) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

var frameTmpl = template.Must(template.New("frame").Parse(`<!doctype html>
<html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>html,body{margin:0;height:100%;background:transparent;overflow:hidden}#root{height:100%}</style>
{{if .CSS}}<link rel="stylesheet" href="{{.Assets}}view.css">{{end}}
</head><body><div id="root"></div>
<script>{{.Boot}}</script>
<script src="{{.Assets}}view.js"></script>
</body></html>`))

// serveFrame serves the document a card's iframe loads. The iframe is
// sandboxed without allow-same-origin, so the document runs in an opaque
// origin: it can't read the dashboard, its cookies or its storage. The CSP
// here closes the rest: it may load only its own bundle, and may not open
// any connection at all. Data reaches it only over postMessage, from the
// board, which got it from the one stream.
func (s *Server) serveFrame(w http.ResponseWriter, r *http.Request, name, digest string) {
	lw, ok := s.widgets[name]
	if !ok || lw.digest != digest {
		http.NotFound(w, r)
		return
	}
	assets := requestOrigin(r) + s.base + "v/" + name + "/" + digest + "/"
	_, cssErr := fs.Stat(lw.spec.View, "view.css")
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", frameCSP(assets, lw.policy.Load().imageSources()))
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("X-Content-Type-Options", "nosniff")
	_ = frameTmpl.Execute(w, struct { // headers are sent: a failure means the client went away
		Assets string
		CSS    bool
		Boot   template.JS
	}{assets, cssErr == nil, frameBoot.js})
}

// assetTypes are the types of view files Go's built-in table doesn't know,
// which would otherwise depend on the machine's mime.types: bundled fonts
// and their licences.
var assetTypes = map[string]string{
	".woff2": "font/woff2",
	".txt":   "text/plain; charset=utf-8",
}

// serveAsset serves a file of a widget's built view.
func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request, name, digest, file string) {
	lw, ok := s.widgets[name]
	if !ok || lw.digest != digest {
		http.NotFound(w, r)
		return
	}
	file = path.Clean(file)
	b, err := fs.ReadFile(lw.spec.View, file)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	if ct := assetTypes[path.Ext(file)]; ct != "" {
		h.Set("Content-Type", ct)
	} else if ct := mime.TypeByExtension(path.Ext(file)); ct != "" {
		h.Set("Content-Type", ct)
	}
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("X-Content-Type-Options", "nosniff")
	// Requested from an opaque origin, so CORS must allow "null" to load
	// fonts; scripts and styles don't need it but it costs nothing.
	h.Set("Access-Control-Allow-Origin", "*")
	_, _ = w.Write(b) //nolint:gosec // G705: a static asset, served with its own type and nosniff
}

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="referrer" content="no-referrer">
<title>widgetd</title>
<link rel="stylesheet" href="{{.Base}}admin.css">
</head><body><div id="app"></div>
<script type="module" src="{{.Base}}admin.js"></script>
</body></html>`))

// servePage serves the admin app shell for the pages a phone opens from a QR
// code (enroll, pair) and the device list. The page reads its route from
// location itself.
func (s *Server) servePage(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'")
	h.Set("Referrer-Policy", "no-referrer")
	_ = pageTmpl.Execute(w, struct{ Base string }{s.base})
}

// routes serves everything under the base path.
func (s *Server) routes(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, s.base)
	parts := strings.SplitN(rest, "/", 4)
	switch {
	case len(parts) == 3 && parts[0] == "frame":
		s.serveFrame(w, r, parts[1], parts[2])
	case len(parts) == 4 && parts[0] == "v":
		s.serveAsset(w, r, parts[1], parts[2], parts[3])
	case parts[0] == "oauth":
		s.serveOAuth(w, r, strings.TrimPrefix(rest, "oauth/"))
	case phonePages[parts[0]]:
		s.servePage(w)
	default:
		s.serveClient(w, r, rest)
	}
}

// frameCSP is a card frame's whole policy: its own bundle and the frame
// bootstrap, images from the hosts its widget was granted, and no
// connections of any kind.
func frameCSP(assets string, imageHosts []string) string {
	img := strings.Join(append([]string{assets, "data:"}, imageHosts...), " ")
	return fmt.Sprintf("default-src 'none'; script-src %[1]s %[3]s; style-src %[1]s 'unsafe-inline'; img-src %[2]s; font-src %[1]s; connect-src 'none'; form-action 'none'; base-uri 'none'", assets, img, frameBoot.csp)
}

// phonePages are the routes of the admin app (client/src/admin.ts) that a
// phone opens, usually from a QR code. Each serves the same shell; the app
// reads its route from the URL. A route the app knows but this map doesn't
// is a 404 before the app can load, so keep the two in step.
var phonePages = map[string]bool{"enroll": true, "pair": true, "configure": true, "edit": true, "admin": true}
