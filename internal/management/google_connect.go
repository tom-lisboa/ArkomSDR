package management

import (
	"embed"
	"net/http"
)

//go:embed google_connect/*
var googleConnectFiles embed.FS

// The seller entry point contains no credentials or account data. Staff sessions
// stay in the page's memory; the existing authenticated APIs own every action.
func googleConnectFile(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		raw, err := googleConnectFiles.ReadFile("google_connect/" + name)
		if err != nil {
			http.Error(w, "Page unavailable", 503)
			return
		}
		_, _ = w.Write(raw)
	}
}
