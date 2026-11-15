// Package web embeds the teacher console static assets.
package web

import "embed"

//go:embed index.html app.js style.css
var FS embed.FS
