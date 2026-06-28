package httpapi

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"
)

//go:embed templates/*.html
var templateFiles embed.FS

//go:embed templates/static
var staticFiles embed.FS

var tmpl *template.Template

func init() {
	tmpl = template.Must(
		template.New("").ParseFS(templateFiles, "templates/*.html"),
	)
}

// staticHandler serves embedded static files at /static/.
// Cache-Control: no-cache forces caches (incl. a Cloudflare proxy) to
// revalidate via ETag, so a redeployed CSS/JS is never served stale.
func staticHandler() http.Handler {
	sub, err := fs.Sub(staticFiles, "templates")
	if err != nil {
		panic("httpapi: failed to sub staticFiles: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		fileServer.ServeHTTP(w, r)
	})
}
