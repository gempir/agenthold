//go:build bundled

package runtimeassets

import (
	"embed"
	"io/fs"
)

// Staged only by scripts/release/build.sh, for the target architecture.
//
//go:embed staged/*
var embedded embed.FS

func bundledFiles() fs.FS {
	f, err := fs.Sub(embedded, "staged")
	if err != nil {
		panic(err)
	}
	return f
}
