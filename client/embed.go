// Package client embeds the built browser code: the <widget-board> element
// screens load, and the admin pages phones open from QR codes. Build it with
// `just client-build`; the output is committed so the Go module is usable
// without a JS toolchain.
package client

import "embed"

// Dist holds dist/, the bundles and styles the host serves.
//
//go:embed all:dist
var Dist embed.FS
