//go:build !dev

package main

import (
	"embed"
	"io/fs"
)

//go:embed static
var staticEmbed embed.FS

func StaticFS() fs.FS {
	sub, _ := fs.Sub(staticEmbed, "static")
	return sub
}
