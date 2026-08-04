package main

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static/*
var staticRoot embed.FS

func staticHandler() http.Handler {
	sub, err := fs.Sub(staticRoot, "static")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(sub))
}
