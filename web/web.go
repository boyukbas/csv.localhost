// Package web holds the browser-side assets compiled into the binary.
package web

import "embed"

// FS contains the page templates and the static assets served under /assets/.
//
//go:embed page.html list.html assets
var FS embed.FS
