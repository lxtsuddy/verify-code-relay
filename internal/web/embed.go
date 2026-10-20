package web

import "embed"

// templateFS holds the HTML templates (see render.go for how they are combined).
//
//go:embed templates
var templateFS embed.FS

// staticFS holds the CSS, JS and icons served under /static/.
//
//go:embed static
var staticFS embed.FS
