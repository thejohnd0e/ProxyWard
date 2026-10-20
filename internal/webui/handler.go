// Package webui serves the compiled web interface that is embedded in the binary.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var embedded embed.FS

// Handler serves the embedded build (internal/webui/dist, written by
// scripts/build-web.mjs).
func Handler() http.Handler {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		return NewHandler(nil)
	}
	return NewHandler(sub)
}

// contentSecurityPolicy allows only same-origin resources. Style attributes
// need 'unsafe-inline' for style-src-attr only; scripts and stylesheets do not.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; style-src-attr 'unsafe-inline'; " +
	"img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// SecurityHeaders adds browser hardening headers to every response.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// NewHandler serves files from root. Paths without a file extension that
// match no file get index.html so client-side routes survive a reload; missing
// files and API paths stay 404, never the app shell.
func NewHandler(root fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := path.Clean("/" + r.URL.Path)
		if p == "/api" || strings.HasPrefix(p, "/api/") || p == "/healthz" {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(p, "/")
		if name == "" || name == "index.html" {
			serveIndex(w, r, root)
			return
		}
		if root != nil {
			if info, err := fs.Stat(root, name); err == nil && !info.IsDir() {
				serveFile(w, r, root, name)
				return
			}
		}
		if path.Ext(name) != "" || name == "assets" || strings.HasPrefix(name, "assets/") {
			http.NotFound(w, r)
			return
		}
		serveIndex(w, r, root)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, root fs.FS) {
	if root == nil {
		unbuilt(w)
		return
	}
	data, err := fs.ReadFile(root, "index.html")
	if err != nil {
		unbuilt(w)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

func unbuilt(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte("The web interface has not been built. Run: node scripts/build-web.mjs\n"))
}

func serveFile(w http.ResponseWriter, r *http.Request, root fs.FS, name string) {
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeFileFS(w, r, root, name)
}
