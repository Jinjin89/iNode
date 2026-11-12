package inode

import (
	"bytes"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
)

//go:embed web/dist
var web embed.FS

func frontend() http.Handler {
	files, _ := fs.Sub(web, "web/dist")
	static := http.FileServer(http.FS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			static.ServeHTTP(w, r)
			return
		}
		page, _ := fs.ReadFile(files, "index.html")
		names, _ := json.Marshal(keys(Operations))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(bytes.ReplaceAll(page, []byte("__OPERATIONS__"), names))
	})
}
