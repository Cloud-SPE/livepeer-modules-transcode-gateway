package server

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// webrootFS holds the three SPAs embedded into the gateway binary.
//
// The contents are populated by `make embed-webroot` (which copies
// web/site, web/portal, web/admin into ./webroot/) before `go build`.
// On a fresh clone the directory contains only .gitkeep — the embed
// directive still works, but spaHandler returns 404 until the SPAs
// are copied in. The Dockerfile + the make target wire this up so
// every shipped build has the SPAs baked in.
//
//go:embed all:webroot
var webrootFS embed.FS

// mountSPAs wires the three embedded SPAs into the chi router.
//
//	/portal/* → portal SPA (with index.html fallback for client routing)
//	/admin/*  → admin SPA  (with index.html fallback for client routing)
//	/*        → site SPA   (catchall for any non-API path)
//
// In dev, devs still run `make web` and hit :3000/:3001/:3002 with
// hot-reload-ish node dev-servers that proxy /api/* back to :4000.
// The embed is only used by the docker image build path — production
// serves everything on one port from this one binary.
func mountSPAs(r chi.Router) {
	// Trailing-slash redirects so bare /portal and /admin land cleanly
	// on the SPA root instead of 404-ing.
	r.Get("/portal", http.RedirectHandler("/portal/", http.StatusMovedPermanently).ServeHTTP)
	r.Get("/admin", http.RedirectHandler("/admin/", http.StatusMovedPermanently).ServeHTTP)

	r.Handle("/portal/*", http.StripPrefix("/portal", spaHandler("portal")))
	r.Handle("/admin/*", http.StripPrefix("/admin", spaHandler("admin")))

	// Site SPA: catchall for any path chi didn't otherwise match.
	// Excludes /api/* — those should 404 normally so callers see a
	// proper "endpoint not found" instead of an HTML page.
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasPrefix(req.URL.Path, "/api/") {
			http.NotFound(w, req)
			return
		}
		spaHandler("site").ServeHTTP(w, req)
	})
}

// spaHandler returns a handler that serves files from webroot/<subdir>/,
// falling back to <subdir>/index.html for any path that doesn't match a
// real file (so the SPA's client-side router can take over). Returns a
// 404 handler when the embedded SPA isn't present — happens on local
// dev builds where webroot/ wasn't populated.
func spaHandler(subdir string) http.Handler {
	sub, err := fs.Sub(webrootFS, "webroot/"+subdir)
	if err != nil {
		return http.NotFoundHandler()
	}
	// Confirm the subdir actually has content; an empty subdir means
	// the embed step didn't run for that SPA.
	if _, err := fs.ReadFile(sub, "index.html"); err != nil {
		return http.NotFoundHandler()
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			serveSPAIndex(w, r, sub)
			return
		}
		f, err := sub.Open(p)
		if err != nil {
			// File not found → SPA client-side route. Serve index.html
			// so the SPA can render the route itself.
			serveSPAIndex(w, r, sub)
			return
		}
		stat, _ := f.Stat()
		f.Close()
		if stat != nil && stat.IsDir() {
			// Directory request without trailing slash or index — fall
			// back to SPA root rather than listing the directory.
			serveSPAIndex(w, r, sub)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

func serveSPAIndex(w http.ResponseWriter, r *http.Request, sub fs.FS) {
	b, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}
