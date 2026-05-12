package main

import (
	"log"
	"net/http"
	"strings"
	"time"
)

// trustworthyOriginForCOOP avoids sending COOP/CORP on plain HTTP LAN hosts (browser ignores them and logs noise).
func trustworthyOriginForCOOP(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}
	if strings.EqualFold(r.URL.Scheme, "https") {
		return true
	}
	h := strings.ToLower(strings.TrimSpace(r.Host))
	if h == "" {
		return false
	}
	if strings.HasPrefix(h, "localhost:") || h == "localhost" {
		return true
	}
	if strings.HasPrefix(h, "127.0.0.1:") || h == "127.0.0.1" {
		return true
	}
	if strings.HasPrefix(h, "[::1]:") || h == "[::1]" {
		return true
	}
	return false
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		if trustworthyOriginForCOOP(r) {
			w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
			w.Header().Set("Cross-Origin-Resource-Policy", "same-site")
		}
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data: https:; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; script-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'")
		next.ServeHTTP(w, r)
	})
}

// normalizeTrailingSlash strips a single trailing '/' from the request path so
// clients that use trailingSlash URLs (e.g. Next.js static export) still match
// HandleFunc patterns registered without a trailing slash.
func normalizeTrailingSlash(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if len(p) > 1 && p[len(p)-1] == '/' {
			r2 := r.Clone(r.Context())
			u := *r.URL
			u.Path = strings.TrimSuffix(p, "/")
			r2.URL = &u
			next.ServeHTTP(w, r2)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func withRequestLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		log.Printf("[http] %s %s status=%d bytes=%d ms=%d ip=%s",
			r.Method, r.URL.Path, status, sw.bytes, time.Since(start).Milliseconds(), requestClientIP(r))
	})
}
