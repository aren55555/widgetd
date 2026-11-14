package host

import (
	"regexp"
	"runtime/debug"
	"sync"
)

// modulePath is widgetd's own module, found in the build info whether it is
// the main module (cmd/widgetd) or a dependency of someone's server.
const modulePath = "github.com/aren55555/widgetd"

// serverVersion is what this binary's widgetd was built from, read once.
var serverVersion = sync.OnceValue(func() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	return versionOf(info)
})

// pseudoVersion matches a module pseudo-version's trailing timestamp and
// 12-character commit (v0.0.0-20261114120000-06c1338abcde).
var pseudoVersion = regexp.MustCompile(`\d{14}-([0-9a-f]{12})(\+incompatible)?$`)

// versionOf names widgetd's build in info: its short commit when it is the
// main module built in a checkout ("-dirty" with uncommitted changes), its
// module version when it is a dependency (a pseudo-version as its short
// commit), and "dev" when neither is recorded, as under go run.
func versionOf(info *debug.BuildInfo) string {
	if info.Main.Path == modulePath {
		var rev, modified string
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				modified = s.Value
			}
		}
		if rev != "" {
			if len(rev) > 7 {
				rev = rev[:7]
			}
			if modified == "true" {
				rev += "-dirty"
			}
			return rev
		}
		return moduleVersion(info.Main)
	}
	for _, m := range info.Deps {
		if m.Path == modulePath {
			if m.Replace != nil {
				return moduleVersion(*m.Replace)
			}
			return moduleVersion(*m)
		}
	}
	return "dev"
}

// moduleVersion is a module's version as shown: a tag as is, a
// pseudo-version as its short commit, and "dev" for none (a local replace,
// or "(devel)").
func moduleVersion(m debug.Module) string {
	if m.Version == "" || m.Version == "(devel)" {
		return "dev"
	}
	if p := pseudoVersion.FindStringSubmatch(m.Version); p != nil {
		return p[1][:7]
	}
	return m.Version
}
