package web

import "embed"

// templateFS holds the HTML templates (see render.go for how they are combined); "all:"
// because shared partials start with "_", which embed would otherwise skip.
//
//go:embed all:templates
var templateFS embed.FS

// staticFS holds the CSS, JS and icons served under /static/.
//
//go:embed static
var staticFS embed.FS
