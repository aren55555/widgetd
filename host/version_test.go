package host

import (
	"runtime/debug"
	"testing"
)

func TestVersionOf(t *testing.T) {
	vcs := func(rev, modified string) []debug.BuildSetting {
		return []debug.BuildSetting{{Key: "vcs.revision", Value: rev}, {Key: "vcs.modified", Value: modified}}
	}
	main := debug.Module{Path: modulePath, Version: "(devel)"}
	dep := func(v string) *debug.Module { return &debug.Module{Path: modulePath, Version: v} }
	tests := []struct {
		name string
		info debug.BuildInfo
		want string
	}{
		{"checkout", debug.BuildInfo{Main: main, Settings: vcs("06c1338abcdef0123456789", "false")}, "06c1338"},
		{"dirty checkout", debug.BuildInfo{Main: main, Settings: vcs("06c1338abcdef0123456789", "true")}, "06c1338-dirty"},
		{"go run", debug.BuildInfo{Main: main}, "dev"},
		{"go install @tag", debug.BuildInfo{Main: debug.Module{Path: modulePath, Version: "v0.4.0"}}, "v0.4.0"},
		{
			"dependency at a tag",
			debug.BuildInfo{Main: debug.Module{Path: "example.com/mine"}, Deps: []*debug.Module{dep("v0.4.0")}},
			"v0.4.0",
		},
		{
			"dependency at a commit",
			debug.BuildInfo{
				Main: debug.Module{Path: "example.com/mine"},
				Deps: []*debug.Module{dep("v0.0.0-20261114120000-06c1338abcde")},
			},
			"06c1338",
		},
		{
			"dependency replaced locally",
			debug.BuildInfo{
				Main: debug.Module{Path: "example.com/mine"},
				Deps: []*debug.Module{{Path: modulePath, Version: "v0.4.0", Replace: &debug.Module{Path: "../widgetd"}}},
			},
			"dev",
		},
		{"not built with widgetd", debug.BuildInfo{Main: debug.Module{Path: "example.com/mine"}}, "dev"},
	}
	for _, tc := range tests {
		if got := versionOf(&tc.info); got != tc.want {
			t.Errorf("%s: versionOf = %q, want %q", tc.name, got, tc.want)
		}
	}
}
