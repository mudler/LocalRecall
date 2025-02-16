package main

import (
	"embed"
	"io/fs"
	"net/http"
	"os"

	"github.com/labstack/echo/v4"
)

//go:embed static
var staticFiles embed.FS

func getFileSystem(useOS bool) http.FileSystem {
	if useOS {
		return http.FS(os.DirFS("static"))
	}

	fsys, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}

	return http.FS(fsys)
}

func registerStaticHandler(e *echo.Echo) {
	assetHandler := http.FileServer(getFileSystem(false))
	e.GET("/", echo.WrapHandler(assetHandler))
}
