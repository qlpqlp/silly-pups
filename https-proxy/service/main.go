// HTTPS Proxy — TLS termination + reverse proxy for other Dogebox pups (local DNS / IP).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"crypto/tls"
	"embed"
)

//go:embed admin/*
var adminFS embed.FS

type server struct {
	mu              sync.RWMutex
	store           *configStore
	adminToken      string
	cert            tls.Certificate
	proxyCache      map[string]*httputil.ReverseProxy
	proxyMu         sync.Mutex
	listenerMu      sync.Mutex
	listenerServers []*http.Server
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func publicPortInt() int {
	p, err := strconv.Atoi(strings.TrimSpace(env("PUBLIC_PORT", "18000")))
	if err != nil || p < 1 || p > 65535 {
		return 18000
	}
	return p
}

// Port for HTTPS when using the default layout. Override with HTTPS_PROXY_TLS_PORT.
// If unset, defaults to PUBLIC_PORT+1 (run.sh / manifest usually set 44443 with PUBLIC_PORT 18000).
func primaryTLSPort() int {
	if v := strings.TrimSpace(os.Getenv("HTTPS_PROXY_TLS_PORT")); v != "" {
		p, err := strconv.Atoi(v)
		if err == nil && p >= 1 && p <= 65535 {
			return p
		}
	}
	p := publicPortInt()
	if p >= 65535 {
		return 65535
	}
	return p + 1
}

// For each port-listener row, HTTPS binds at ListenPort + this offset (HTTP stays on ListenPort for Dogebox).
func listenerTLSOffset() int {
	v := strings.TrimSpace(env("HTTPS_PROXY_LISTENER_TLS_OFFSET", "10000"))
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 10000
	}
	return n
}

func validateHTTPSPortPairs(cfg *Config) error {
	if !cfg.HTTPSEnabled || len(cfg.Listeners) == 0 {
		return nil
	}
	off := listenerTLSOffset()
	listenPorts := make(map[int]struct{})
	for _, L := range cfg.Listeners {
		if L.ListenPort >= 1 && L.ListenPort <= 65535 && strings.TrimSpace(L.Upstream) != "" {
			listenPorts[L.ListenPort] = struct{}{}
		}
	}
	seenTLS := make(map[int]struct{})
	for _, L := range cfg.Listeners {
		up := strings.TrimSpace(L.Upstream)
		if L.ListenPort < 1 || L.ListenPort > 65535 || up == "" {
			continue
		}
		tp := L.ListenPort + off
		if tp < 1 || tp > 65535 {
			return fmt.Errorf("listen_port %d + HTTPS_PROXY_LISTENER_TLS_OFFSET (%d) out of range", L.ListenPort, off)
		}
		if _, ok := listenPorts[tp]; ok {
			return fmt.Errorf("TLS port %d (listen %d + offset) collides with another listener; change ports or HTTPS_PROXY_LISTENER_TLS_OFFSET", tp, L.ListenPort)
		}
		if _, dup := seenTLS[tp]; dup {
			return fmt.Errorf("duplicate TLS port %d", tp)
		}
		seenTLS[tp] = struct{}{}
	}
	return nil
}

func hostKey(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if i := strings.Index(host, ":"); i >= 0 {
		host = host[:i]
	}
	return host
}

func (s *server) rebuildCertLocked() error {
	cfg := s.store.snapshot()
	if !cfg.HTTPSEnabled {
		return nil
	}
	cert, err := tlsCertForSANs(cfg.TLSDomains, cfg.TLSIPs)
	if err != nil {
		return err
	}
	s.cert = cert
	return nil
}

func (s *server) invalidateProxies() {
	s.proxyMu.Lock()
	s.proxyCache = make(map[string]*httputil.ReverseProxy)
	s.proxyMu.Unlock()
}

func (s *server) proxyForUpstream(upstream, forwardedProto string) (*httputil.ReverseProxy, error) {
	upstream = strings.TrimSpace(upstream)
	if upstream == "" {
		return nil, errors.New("empty upstream")
	}
	fp := strings.ToLower(strings.TrimSpace(forwardedProto))
	if fp != "https" {
		fp = "http"
	}
	target, err := url.Parse(upstream)
	if err != nil {
		return nil, err
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return nil, fmt.Errorf("upstream must be http or https: %q", upstream)
	}
	key := target.String() + "\x00" + fp
	s.proxyMu.Lock()
	defer s.proxyMu.Unlock()
	if s.proxyCache == nil {
		s.proxyCache = make(map[string]*httputil.ReverseProxy)
	}
	if p, ok := s.proxyCache[key]; ok {
		return p, nil
	}
	p := httputil.NewSingleHostReverseProxy(target)
	t := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	p.Transport = t
	od := p.Director
	p.Director = func(req *http.Request) {
		od(req)
		req.Header.Set("X-Forwarded-Proto", fp)
		if req.Header.Get("X-Forwarded-Host") == "" {
			req.Header.Set("X-Forwarded-Host", req.Host)
		}
		ip, _, err := net.SplitHostPort(req.RemoteAddr)
		if err == nil {
			req.Header.Set("X-Real-IP", ip)
			if req.Header.Get("X-Forwarded-For") == "" {
				req.Header.Set("X-Forwarded-For", ip)
			}
		}
	}
	p.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("[https-proxy] upstream %s: %v", key, err)
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}
	s.proxyCache[key] = p
	return p, nil
}

func (s *server) routeProxy(host, forwardedProto string) (*httputil.ReverseProxy, error) {
	h := hostKey(host)
	cfg := s.store.snapshot()
	for _, rt := range cfg.Routes {
		if hostKey(rt.Host) == h {
			return s.proxyForUpstream(rt.Upstream, forwardedProto)
		}
	}
	if strings.TrimSpace(cfg.DefaultUpstream) != "" {
		return s.proxyForUpstream(cfg.DefaultUpstream, forwardedProto)
	}
	return nil, nil
}

func (s *server) adminPrefix() string {
	p := s.store.snapshot().AdminPathPrefix
	if p == "" {
		return "/__proxy"
	}
	return p
}

func (s *server) authOK(r *http.Request) bool {
	tok := strings.TrimSpace(s.adminToken)
	if tok == "" {
		return true
	}
	a := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(a), "bearer ") {
		if strings.TrimSpace(a[7:]) == tok {
			return true
		}
	}
	if r.URL.Query().Get("token") == tok {
		return true
	}
	return false
}

// fixedUpstream empty => use host-based routes + default_upstream.
// forwardedProto is the client→proxy scheme ("http" or "https") for X-Forwarded-Proto.
func (s *server) makeHandler(fixedUpstream, forwardedProto string) http.HandlerFunc {
	fixedUpstream = strings.TrimSpace(fixedUpstream)
	fp := strings.ToLower(strings.TrimSpace(forwardedProto))
	if fp != "https" {
		fp = "http"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		prefix := s.adminPrefix()
		if strings.HasPrefix(r.URL.Path, prefix+"/") || r.URL.Path == prefix || r.URL.Path == prefix+"/" {
			s.handleAdmin(w, r)
			return
		}

		if fixedUpstream != "" {
			p, err := s.proxyForUpstream(fixedUpstream, fp)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if p != nil {
				p.ServeHTTP(w, r)
				return
			}
		}

		p, err := s.routeProxy(r.Host, fp)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if p != nil {
			p.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>HTTPS Proxy</title>
<p>No route for <strong>%s</strong>. Open <a href="%s/">the admin UI</a> to add listeners or host → upstream mappings.</p>`,
			htmlEsc(r.Host), htmlEsc(prefix))
	}
}

func htmlEsc(s string) string {
	s = strings.ReplaceAll(s, `&`, `&amp;`)
	s = strings.ReplaceAll(s, `<`, `&lt;`)
	s = strings.ReplaceAll(s, `>`, `&gt;`)
	s = strings.ReplaceAll(s, `"`, `&quot;`)
	return s
}

func (s *server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	prefix := s.adminPrefix()
	p := strings.TrimPrefix(r.URL.Path, prefix)
	if p == "" {
		p = "/"
	}
	switch {
	case r.Method == http.MethodGet && (p == "/" || p == ""):
		b, err := adminFS.ReadFile("admin/index.html")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
	case r.Method == http.MethodGet && p == "/api/health":
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case r.Method == http.MethodGet && p == "/api/config":
		if !s.authOK(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, s.store.snapshot())
	case r.Method == http.MethodPost && p == "/api/config":
		if !s.authOK(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var cfg Config
		if err := json.Unmarshal(b, &cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := validateConfig(&cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		if err := s.store.update(cfg); err != nil {
			s.mu.Unlock()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := s.rebuildCertLocked(); err != nil {
			s.mu.Unlock()
			http.Error(w, "cert: "+err.Error(), http.StatusInternalServerError)
			return
		}
		s.invalidateProxies()
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		go func() {
			time.Sleep(300 * time.Millisecond)
			s.reloadListeners()
		}()
	case r.Method == http.MethodGet && p == "/api/cert.pem":
		if !s.authOK(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !s.store.snapshot().HTTPSEnabled {
			http.Error(w, "HTTPS not enabled; turn on TLS in config first", http.StatusNotFound)
			return
		}
		s.mu.RLock()
		pem := certToPEM(s.cert)
		s.mu.RUnlock()
		w.Header().Set("Content-Type", "application/x-pem-file")
		_, _ = w.Write([]byte(pem))
	case r.Method == http.MethodPost && p == "/api/probe-tls":
		s.handleProbeTLS(w, r)
	case r.Method == http.MethodPost && p == "/api/probe-upstream":
		s.handleProbeUpstream(w, r)
	case r.Method == http.MethodPost && p == "/api/probe-tcp":
		s.handleProbeTCP(w, r)
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *server) tlsConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(chi *tls.ClientHelloInfo) (*tls.Certificate, error) {
			s.mu.RLock()
			defer s.mu.RUnlock()
			c := s.cert
			return &c, nil
		},
	}
}

func (s *server) shutdownListeners() {
	s.listenerMu.Lock()
	servers := s.listenerServers
	s.listenerServers = nil
	s.listenerMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	for _, hs := range servers {
		_ = hs.Shutdown(ctx)
	}
}

func (s *server) reloadListeners() {
	s.shutdownListeners()
	if err := s.startListeners(); err != nil {
		log.Printf("[https-proxy] listener restart: %v", err)
	}
}

func (s *server) startListeners() error {
	cfg := s.store.snapshot()
	if !cfg.HTTPSEnabled {
		return s.startHTTPListeners()
	}
	if err := validateHTTPSPortPairs(&cfg); err != nil {
		return err
	}
	return s.startHTTPSListeners()
}

func (s *server) bindHTTPListener(addr string, h http.Handler, port int, kind, up string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("http listen %s: %w", addr, err)
	}
	hs := &http.Server{
		Handler:      h,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}
	s.listenerMu.Lock()
	s.listenerServers = append(s.listenerServers, hs)
	s.listenerMu.Unlock()
	go func() {
		if up != "" {
			log.Printf("[https-proxy] %s :%d -> %s", kind, port, up)
		} else {
			log.Printf("[https-proxy] %s :%d host-based routes; admin %s/", kind, port, s.adminPrefix())
		}
		if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[https-proxy] http listener %s stopped: %v", addr, err)
		}
	}()
	return nil
}

func (s *server) bindHTTPSListener(addr string, tlsCfg *tls.Config, h http.Handler, port int, kind, up string) error {
	ln, err := tls.Listen("tcp", addr, tlsCfg)
	if err != nil {
		return fmt.Errorf("https listen %s: %w", addr, err)
	}
	hs := &http.Server{
		Handler:      h,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}
	s.listenerMu.Lock()
	s.listenerServers = append(s.listenerServers, hs)
	s.listenerMu.Unlock()
	go func() {
		if up != "" {
			log.Printf("[https-proxy] %s :%d -> %s (same cert for all ports; SAN matches hostname e.g. dogebox)", kind, port, up)
		} else {
			log.Printf("[https-proxy] %s :%d host-based routes; admin %s/", kind, port, s.adminPrefix())
		}
		if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[https-proxy] tls listener %s stopped: %v", addr, err)
		}
	}()
	return nil
}

// With HTTPS enabled: HTTP on each exposed port (Dogebox reverse-proxies with http://) and HTTPS on a companion port.
func (s *server) startHTTPSListeners() error {
	cfg := s.store.snapshot()
	tlsCfg := s.tlsConfig()
	off := listenerTLSOffset()

	if len(cfg.Listeners) > 0 {
		n := 0
		for _, L := range cfg.Listeners {
			up := strings.TrimSpace(L.Upstream)
			if L.ListenPort < 1 || L.ListenPort > 65535 || up == "" {
				continue
			}
			httpAddr := fmt.Sprintf(":%d", L.ListenPort)
			if err := s.bindHTTPListener(httpAddr, s.makeHandler(up, "http"), L.ListenPort, "HTTP (Dogebox)", up); err != nil {
				s.shutdownListeners()
				return err
			}
			tlsPort := L.ListenPort + off
			tlsAddr := fmt.Sprintf(":%d", tlsPort)
			if err := s.bindHTTPSListener(tlsAddr, tlsCfg, s.makeHandler(up, "https"), tlsPort, "HTTPS (direct)", up); err != nil {
				s.shutdownListeners()
				return err
			}
			n++
		}
		if n == 0 {
			return errors.New("no valid port listeners (need listen_port + upstream)")
		}
		return nil
	}

	pub := publicPortInt()
	tlsP := primaryTLSPort()
	if tlsP == pub {
		return fmt.Errorf("HTTPS_PROXY_TLS_PORT must differ from PUBLIC_PORT (%d) — HTTP uses PUBLIC_PORT for Dogebox; TLS uses the companion port", pub)
	}
	httpAddr := fmt.Sprintf(":%d", pub)
	if err := s.bindHTTPListener(httpAddr, s.makeHandler("", "http"), pub, "HTTP (Dogebox / panel)", ""); err != nil {
		return err
	}
	tlsAddr := fmt.Sprintf(":%d", tlsP)
	if err := s.bindHTTPSListener(tlsAddr, tlsCfg, s.makeHandler("", "https"), tlsP, "HTTPS (browser)", ""); err != nil {
		s.shutdownListeners()
		return err
	}
	return nil
}

func (s *server) startHTTPListeners() error {
	cfg := s.store.snapshot()

	if len(cfg.Listeners) > 0 {
		n := 0
		for _, L := range cfg.Listeners {
			up := strings.TrimSpace(L.Upstream)
			if L.ListenPort < 1 || L.ListenPort > 65535 || up == "" {
				continue
			}
			addr := fmt.Sprintf(":%d", L.ListenPort)
			if err := s.bindHTTPListener(addr, s.makeHandler(up, "http"), L.ListenPort, "HTTP", up); err != nil {
				s.shutdownListeners()
				return err
			}
			n++
		}
		if n == 0 {
			return errors.New("no valid port listeners (need listen_port + upstream)")
		}
		return nil
	}

	pub := publicPortInt()
	addr := fmt.Sprintf(":%d", pub)
	if err := s.bindHTTPListener(addr, s.makeHandler("", "http"), pub, "HTTP", ""); err != nil {
		return err
	}
	return nil
}

func main() {
	log.SetOutput(os.Stderr)
	storage := env("HTTPS_PROXY_STORAGE", "/storage/https-proxy")
	if err := os.MkdirAll(storage, 0700); err != nil {
		log.Fatal(err)
	}
	cfgPath := filepath.Join(storage, "config.json")
	store, err := loadConfig(cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		if err := store.persist(); err != nil {
			log.Fatal(err)
		}
	}

	const defaultAdminToken = "DOGECOIN"
	token := strings.TrimSpace(env("HTTPS_PROXY_ADMIN_TOKEN", ""))
	if token == "" {
		tokenPath := filepath.Join(storage, "admin.token")
		b, err := os.ReadFile(tokenPath)
		if err == nil && strings.TrimSpace(string(b)) != "" {
			token = strings.TrimSpace(string(b))
		} else {
			token = defaultAdminToken
			if err := os.WriteFile(tokenPath, []byte(token), 0600); err != nil {
				log.Fatal(err)
			}
			log.Printf("[https-proxy] wrote default admin token to %s (override with HTTPS_PROXY_ADMIN_TOKEN)", tokenPath)
		}
	}

	srv := &server{
		store:      store,
		adminToken: token,
		proxyCache: make(map[string]*httputil.ReverseProxy),
	}
	if err := srv.rebuildCertLocked(); err != nil {
		log.Fatal(err)
	}

	if err := srv.startListeners(); err != nil {
		log.Fatal(err)
	}
	if srv.store.snapshot().HTTPSEnabled {
		if len(srv.store.snapshot().Listeners) == 0 {
			log.Printf("[https-proxy] TLS: use http://<host>:%d for Dogebox panel links; use https://<host>:%d for HTTPS (same hostname e.g. dogebox on every port).",
				publicPortInt(), primaryTLSPort())
		} else {
			log.Printf("[https-proxy] TLS: each row — HTTP on listen_port (Dogebox), HTTPS on listen_port + %d (HTTPS_PROXY_LISTENER_TLS_OFFSET).",
				listenerTLSOffset())
		}
	} else {
		log.Printf("[https-proxy] Plain HTTP only. Open http://<host>:%d%s/ — enable HTTPS in admin for TLS on the companion port.", publicPortInt(), srv.adminPrefix())
	}

	select {}
}
