//go:build !clionly

package web

import (
	"embed"
	"io/fs"
)

//go:embed all:out
var embedded embed.FS

func FS() (fs.FS, error) {
	return fs.Sub(embedded, "out")
}
