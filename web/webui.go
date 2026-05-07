package webui

import "embed"

//go:embed index.html *.js style.css
var FS embed.FS
