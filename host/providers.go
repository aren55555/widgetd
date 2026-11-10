package host

import (
	"context"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Providers: whom the display can have an account with. A provider is data,
// a manifest, never code: its endpoints, its sign-in flow, and what its
// scopes unlock. Where providers differ (how they revoke, how an API says a
// scope was missing) the manifest picks one of a fixed set of strategies the
// host implements, defaulting to the OAuth standards. So installing a
// provider can't run anything; it can only describe one.
//
// Widgets name a provider and scopes; they never name endpoints.

//go:embed providers/*.toml
var builtinProviderFS embed.FS

// ProviderManifest is a provider package: provider.toml.
type ProviderManifest struct {
	Name    string `toml:"name"`
	Title   string `toml:"title"`
	Version string `toml:"version"`

	SignIn struct {
		// Flow is "browser" (authorization code + PKCE, RFC 6749 and 7636:
		// a QR code to the consent page on a phone) or "device" (RFC 8628:
		// a code to enter on the provider's site).
		Flow      string            `toml:"flow"`
		AuthURL   string            `toml:"auth_url"`
		DeviceURL string            `toml:"device_url"`
		TokenURL  string            `toml:"token_url"`
		AuthParams map[string]string `toml:"auth_params"`
		// Revoke is "rfc7009" (POST the token to RevokeURL), "github-grant"
		// (DELETE RevokeURL, with {client_id} filled in, and the token in a
		// JSON body), or "none".
		Revoke    string `toml:"revoke"`
		RevokeURL string `toml:"revoke_url"`
		// ClientHelp tells whoever runs the host how to register a client.
		ClientHelp string `toml:"client_help"`
	} `toml:"sign_in"`

	// Identity says who an account is, so a sign-in to an existing account
	// can be checked to be the same person, and the admin page can show it.
	// Optional. Scopes are added to every sign-in to make URL readable.
	Identity struct {
		URL    string   `toml:"url"`
		Field  string   `toml:"field"` // in its JSON reply: "email", "login"
		Scopes []string `toml:"scopes"`
	} `toml:"identity"`

	Scopes struct {
		// Covers maps each scope to what it unlocks: URL prefixes, each
		// optionally led by a method ("GET https://api.example.com/x/").
		// Strict: a scope not here doesn't exist as far as the host is
		// concerned, and a request no declared scope covers isn't signed.
		Covers map[string][]string `toml:"covers"`
		// AnyToken is what any signed-in token may call, whatever its
		// scopes ("GET https://api.github.com/user").
		AnyToken []string `toml:"any_token"`
		// Missing is how an API says a scope was missing: "rfc6750" (the
		// WWW-Authenticate insufficient_scope error and its scope list; the
		// default) or "headers" (MissingHave lists the token's scopes,
		// MissingWant the ones the endpoint would accept).
		Missing     string `toml:"missing"`
		MissingHave string `toml:"missing_have"`
		MissingWant string `toml:"missing_want"`
		// RejectedPattern pulls the scopes a refused sign-in objected to out
		// of the provider's error description (group 1: a space- or
		// comma-separated list). Without it, a refusal names no scope.
		RejectedPattern string `toml:"rejected_pattern"`
	} `toml:"scopes"`
}

type flow int

const (
	flowBrowser flow = iota + 1
	flowDevice
)

// rule is one entry of what a scope unlocks.
type rule struct {
	scope  string // "" for any token
	method string // "" for any method
	prefix *url.URL
}

// provider is a manifest, checked and compiled.
type provider struct {
	m          ProviderManifest
	Name       string
	Title      string
	Flow       flow
	AuthParams url.Values
	rules      []rule
	rejected   *regexp.Regexp
}

var providerNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// parseProvider reads and checks a provider manifest.
func parseProvider(b []byte) (*provider, error) {
	var m ProviderManifest
	md, err := toml.Decode(string(b), &m)
	if err != nil {
		return nil, err
	}
	if und := md.Undecoded(); len(und) > 0 {
		return nil, fmt.Errorf("unknown field %s", und[0])
	}
	if !providerNameRE.MatchString(m.Name) {
		return nil, fmt.Errorf("name %q: want lowercase letters, digits and dashes", m.Name)
	}
	if m.Title == "" {
		return nil, errors.New("title is required")
	}
	p := &provider{m: m, Name: m.Name, Title: m.Title, AuthParams: url.Values{}}
	for k, v := range m.SignIn.AuthParams {
		p.AuthParams.Set(k, v)
	}
	https := func(field, u string) error {
		pu, err := url.Parse(u)
		if err != nil || pu.Scheme != "https" || pu.Host == "" {
			return fmt.Errorf("%s %q: want an https URL", field, u)
		}
		return nil
	}
	switch m.SignIn.Flow {
	case "browser":
		p.Flow = flowBrowser
		if err := https("auth_url", m.SignIn.AuthURL); err != nil {
			return nil, err
		}
	case "device":
		p.Flow = flowDevice
		if err := https("device_url", m.SignIn.DeviceURL); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("sign_in.flow %q: want browser or device", m.SignIn.Flow)
	}
	if err := https("token_url", m.SignIn.TokenURL); err != nil {
		return nil, err
	}
	switch m.SignIn.Revoke {
	case "rfc7009":
		if err := https("revoke_url", m.SignIn.RevokeURL); err != nil {
			return nil, err
		}
	case "github-grant":
		if err := https("revoke_url", m.SignIn.RevokeURL); err != nil || !strings.Contains(m.SignIn.RevokeURL, "{client_id}") {
			return nil, fmt.Errorf("sign_in.revoke_url %q: want an https URL with {client_id}", m.SignIn.RevokeURL)
		}
	case "none":
	default:
		return nil, fmt.Errorf("sign_in.revoke %q: want rfc7009, github-grant or none", m.SignIn.Revoke)
	}
	switch m.Scopes.Missing {
	case "", "rfc6750":
	case "headers":
		if m.Scopes.MissingHave == "" || m.Scopes.MissingWant == "" {
			return nil, errors.New("scopes.missing = headers needs missing_have and missing_want")
		}
	default:
		return nil, fmt.Errorf("scopes.missing %q: want rfc6750 or headers", m.Scopes.Missing)
	}
	if m.Scopes.RejectedPattern != "" {
		re, err := regexp.Compile(m.Scopes.RejectedPattern)
		if err != nil || re.NumSubexp() < 1 {
			return nil, fmt.Errorf("scopes.rejected_pattern: want a regexp with a group: %v", err)
		}
		p.rejected = re
	}
	if m.Identity.URL != "" {
		if err := https("identity.url", m.Identity.URL); err != nil {
			return nil, err
		}
		if m.Identity.Field == "" {
			return nil, errors.New("identity.field is required with identity.url")
		}
	}
	if len(m.Scopes.Covers) == 0 {
		return nil, errors.New("scopes.covers is empty: a provider no scope unlocks anything with is no use")
	}
	add := func(scope, entry string) error {
		method, raw, ok := strings.Cut(entry, " ")
		if !ok {
			method, raw = "", entry
		}
		if method != "" && strings.ToUpper(method) != method {
			return fmt.Errorf("%q: the method must be upper case", entry)
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" {
			return fmt.Errorf("%q: want an https URL prefix, without a query", entry)
		}
		p.rules = append(p.rules, rule{scope: scope, method: method, prefix: u})
		return nil
	}
	for scope, entries := range m.Scopes.Covers {
		if strings.TrimSpace(scope) == "" || strings.ContainsAny(scope, " ,") {
			return nil, fmt.Errorf("scope %q: no spaces or commas", scope)
		}
		if len(entries) == 0 {
			return nil, fmt.Errorf("scope %q covers nothing", scope)
		}
		for _, e := range entries {
			if err := add(scope, e); err != nil {
				return nil, err
			}
		}
	}
	for _, e := range m.Scopes.AnyToken {
		if err := add("", e); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// loadBuiltinProviders reads the providers shipped inside widgetd.
func loadBuiltinProviders() (map[string]*provider, error) {
	out := map[string]*provider{}
	err := fs.WalkDir(builtinProviderFS, "providers", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(builtinProviderFS, path)
		if err != nil {
			return err
		}
		p, err := parseProvider(b)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		out[p.Name] = p
		return nil
	})
	return out, err
}

// knows reports whether the provider has a scope (A: a widget declaring a
// scope the provider doesn't list is refused at load).
func (p *provider) knows(scope string) bool {
	_, ok := p.m.Scopes.Covers[scope]
	return ok
}

// hosts reports whether a URL is on one of the provider's API hosts at all.
func (p *provider) hosts(u *url.URL) bool {
	for _, r := range p.rules {
		if strings.EqualFold(r.prefix.Host, u.Host) {
			return true
		}
	}
	return false
}

func (r rule) matches(method string, u *url.URL) bool {
	if r.method != "" && r.method != method {
		return false
	}
	if u.Scheme != "https" || !strings.EqualFold(u.Host, r.prefix.Host) {
		return false
	}
	path := u.EscapedPath()
	pre := r.prefix.EscapedPath()
	if path == pre {
		return true
	}
	if !strings.HasPrefix(path, pre) {
		return false
	}
	// "/x/" covers "/x/y"; "/x" covers "/x/y" and "/x?…" but not "/xy".
	return strings.HasSuffix(pre, "/") || path[len(pre)] == '/'
}

// covers reports whether a widget declaring scopes may have a request
// signed (D: strict least privilege on a shared token), and, if not, which
// scopes would have covered it.
func (p *provider) covers(scopes []string, method string, u *url.URL) (bool, []string) {
	var would []string
	for _, r := range p.rules {
		if !r.matches(method, u) {
			continue
		}
		if r.scope == "" || slices.Contains(scopes, r.scope) {
			return true, nil
		}
		if !slices.Contains(would, r.scope) {
			would = append(would, r.scope)
		}
	}
	sort.Strings(would)
	return false, would
}

// missingScopes reads, from a signed request's response, the scopes the API
// says it needed and the token lacked (C).
func (p *provider) missingScopes(resp *http.Response) []string {
	switch p.m.Scopes.Missing {
	case "headers":
		want := splitScopes(resp.Header.Get(p.m.Scopes.MissingWant))
		have := splitScopes(resp.Header.Get(p.m.Scopes.MissingHave))
		if len(want) == 0 || resp.StatusCode < 400 {
			return nil
		}
		for _, w := range want {
			if slices.Contains(have, w) {
				return nil // the token had an acceptable scope: not a scope problem
			}
		}
		return want
	default: // RFC 6750 §3.1
		for _, h := range resp.Header.Values("WWW-Authenticate") {
			params := bearerParams(h)
			if params["error"] == "insufficient_scope" {
				if s := splitScopes(params["scope"]); len(s) > 0 {
					return s
				}
				return []string{"(unnamed)"}
			}
		}
		return nil
	}
}

// rejectedScopes reads which scopes a refused sign-in objected to (B), or
// nil if the provider didn't say.
func (p *provider) rejectedScopes(description string) []string {
	if p.rejected == nil {
		return nil
	}
	m := p.rejected.FindStringSubmatch(description)
	if m == nil {
		return nil
	}
	return splitScopes(m[1])
}

func splitScopes(s string) []string {
	out := strings.FieldsFunc(s, func(c rune) bool { return c == ' ' || c == ',' })
	sort.Strings(out)
	return out
}

// bearerParams parses a WWW-Authenticate Bearer challenge's parameters.
func bearerParams(h string) map[string]string {
	out := map[string]string{}
	scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return out
	}
	re := regexp.MustCompile(`([a-zA-Z_]+)\s*=\s*(?:"((?:[^"\\]|\\.)*)"|([^\s,]+))`)
	for _, m := range re.FindAllStringSubmatch(rest, -1) {
		v := m[2]
		if v == "" {
			v = m[3]
		}
		out[strings.ToLower(m[1])] = strings.ReplaceAll(v, `\"`, `"`)
	}
	return out
}

// revoke ends an account's grant with the provider, by the manifest's
// strategy.
func (p *provider) revoke(ctx context.Context, s *Server, c signInClient, t *token) error {
	switch p.m.SignIn.Revoke {
	case "rfc7009":
		tok := t.Refresh
		if tok == "" {
			tok = t.Access
		}
		form := url.Values{"token": {tok}}
		var hdr http.Header
		if c.secret != "" {
			hdr = http.Header{"Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte(url.QueryEscape(c.id)+":"+url.QueryEscape(c.secret)))}}
		} else {
			form.Set("client_id", c.id)
		}
		return s.providerForm(ctx, http.MethodPost, p.m.SignIn.RevokeURL, form, hdr, nil)
	case "github-grant":
		if c.secret == "" {
			return errors.New("revoking needs the client secret")
		}
		endpoint := strings.ReplaceAll(p.m.SignIn.RevokeURL, "{client_id}", url.PathEscape(c.id))
		hdr := http.Header{"Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte(c.id+":"+c.secret))}}
		return s.providerJSON(ctx, http.MethodDelete, endpoint, fmt.Sprintf(`{"access_token":%q}`, t.Access), hdr)
	}
	return nil
}
