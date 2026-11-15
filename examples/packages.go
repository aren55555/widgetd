package examples

import (
	barttracker "github.com/aren55555/widgetd/examples/bart-tracker"
	bitcoinspiral "github.com/aren55555/widgetd/examples/bitcoin-spiral"
	"github.com/aren55555/widgetd/examples/calendar"
	githubnotifications "github.com/aren55555/widgetd/examples/github-notifications"
	portfolioheatmap "github.com/aren55555/widgetd/examples/portfolio-heatmap"
	sportsgame "github.com/aren55555/widgetd/examples/sports-game"
	"github.com/aren55555/widgetd/examples/weather"
)

// Packages returns every widget widgetd ships, packaged, for
// host.LoadWidgets. An embedder that offers them all picks up new ones by
// updating widgetd. The slice is new on each call, so a caller may append
// its own widgets (a third-party widget's generated Package) to it.
//
// Every example belongs here; TestPackagesListsEveryExample checks that.
func Packages() [][]byte {
	return [][]byte{
		barttracker.Package,
		bitcoinspiral.Package,
		sportsgame.Package,
		githubnotifications.Package,
		calendar.Package,
		portfolioheatmap.Package,
		weather.Package,
		ClockPackage,
	}
}
