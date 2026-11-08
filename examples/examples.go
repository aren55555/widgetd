// Package examples holds example widgets. Each widget folder is its own Go
// package (see bitcoin-spiral); single-file widgets are built next to this
// file, and embedded here.
package examples

import _ "embed"

// Clock is examples/clock.widget, built by `widgetc build examples/clock.widget`.
//
//go:embed clock.wpkg
var Clock []byte
