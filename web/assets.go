package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist all:static
var embedded embed.FS

func Static() fs.FS {
	if index, err := embedded.Open("dist/index.html"); err == nil {
		_ = index.Close()
		dist, err := fs.Sub(embedded, "dist")
		if err != nil {
			panic(err)
		}
		return dist
	}

	static, err := fs.Sub(embedded, "static")
	if err != nil {
		panic(err)
	}
	return static
}
