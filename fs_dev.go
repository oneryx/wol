//go:build dev

package main

import (
	"io/fs"
	"os"
)

func StaticFS() fs.FS { return os.DirFS("static") }
