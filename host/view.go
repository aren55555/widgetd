package host

import (
	"fmt"
	"html/template"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

// frameURL is where a widget's card document is served. It is keyed by
// digest, so it never changes for a given build and can be cached forever.
func (s *Server) frameURL(w *loadedWidget) string {
	return s.base + "frame/" + w.spec.Name + "/" + w.digest
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
	h.Set("Content-Security-Policy", fmt.Sprintf(
		"default-src 'none'; script-src %[1]s; style-src %[1]s 'unsafe-inline'; img-src %[1]s data:; font-src %[1]s; connect-src 'none'; form-action 'none'; base-uri 'none'",
		assets))
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("X-Content-Type-Options", "nosniff")
	frameTmpl.Execute(w, struct {
		Assets string
		CSS    bool
	}{assets, cssErr == nil})
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
	if ct := mime.TypeByExtension(path.Ext(file)); ct != "" {
		h.Set("Content-Type", ct)
	}
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("X-Content-Type-Options", "nosniff")
	// Requested from an opaque origin, so CORS must allow "null" to load
	// fonts; scripts and styles don't need it but it costs nothing.
	h.Set("Access-Control-Allow-Origin", "*")
	w.Write(b)
}

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="referrer" content="no-referrer">
<title>widgetd</title>
<link rel="stylesheet" href="{{.Base}}admin.css">
</head><body><main id="app"></main>
<script type="module" src="{{.Base}}admin.js"></script>
</body></html>`))

// servePage serves the admin app shell for the pages a phone opens from a QR
// code (enroll, pair) and the device list. The page reads its route from
// location itself.
func (s *Server) servePage(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'")
	h.Set("Referrer-Policy", "no-referrer")
	pageTmpl.Execute(w, struct{ Base string }{s.base})
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
	case parts[0] == "enroll" || parts[0] == "pair" || parts[0] == "admin":
		s.servePage(w, r)
	default:
		s.serveClient(w, r, rest)
	}
}
