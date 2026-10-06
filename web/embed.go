// Package web contains the application page and its static assets.
package web

import "embed"

// FS holds templates/index.html and static/app.css, static/app.js.
//
//go:embed templates/*.html static/*
var FS embed.FS
