//go:build !bundled

package runtimeassets

import "io/fs"

func bundledFiles() fs.FS { return nil }
