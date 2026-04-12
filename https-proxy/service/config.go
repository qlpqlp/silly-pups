package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ProxyRoute maps incoming HTTP Host (without port normalization) to an upstream origin.
type ProxyRoute struct {
	Host     string `json:"host"`     // e.g. dogebox, pq.local (matched case-insensitively; port in Host header ignored)
	Upstream string `json:"upstream"` // e.g. http://127.0.0.1:33880 or http://pq-wallet:33880
}

// PortListener binds one local HTTPS port to one HTTP upstream (same TLS cert for all listeners).
// Browsers validate the hostname (e.g. dogebox) only; the port is not part of the certificate.
type PortListener struct {
	ListenPort int    `json:"listen_port"` // HTTP listen port for this row (TLS uses listen_port + HTTPS_PROXY_LISTENER_TLS_OFFSET)
	Upstream   string `json:"upstream"`    // e.g. http://127.0.0.1:8080 — plain HTTP backend for that pup
}

// Config is persisted JSON for the proxy + TLS SANs.
type Config struct {
	// HTTPSEnabled true = TLS + self-signed cert by default. Set false in admin for plain HTTP only.
	HTTPSEnabled    bool           `json:"https_enabled"`
	TLSDomains      []string       `json:"tls_domains"`
	TLSIPs          []string       `json:"tls_ips"`
	Listeners       []PortListener `json:"listeners,omitempty"` // local port -> upstream (scheme matches HTTPSEnabled)
	Routes          []ProxyRoute   `json:"routes"`
	DefaultUpstream string         `json:"default_upstream,omitempty"`
	AdminPathPrefix string         `json:"admin_path_prefix"` // default /__proxy
}

func defaultConfig() Config {
	return Config{
		HTTPSEnabled:    true,
		TLSDomains:      []string{"dogebox"},
		TLSIPs:          []string{},
		Listeners:       nil,
		Routes:          nil,
		DefaultUpstream: "",
		AdminPathPrefix: "/__proxy",
	}
}

func validateConfig(cfg *Config) error {
	seen := make(map[int]struct{})
	for _, L := range cfg.Listeners {
		p := L.ListenPort
		if p < 1 || p > 65535 {
			return fmt.Errorf("invalid listen_port %d", p)
		}
		if _, ok := seen[p]; ok {
			return fmt.Errorf("duplicate listen_port %d", p)
		}
		seen[p] = struct{}{}
	}
	if err := validateHTTPSPortPairs(cfg); err != nil {
		return err
	}
	if cfg.HTTPSEnabled && len(cfg.Listeners) == 0 {
		if primaryTLSPort() == publicPortInt() {
			return fmt.Errorf("HTTPS_PROXY_TLS_PORT must differ from PUBLIC_PORT — HTTP serves Dogebox on PUBLIC_PORT; TLS uses the companion port")
		}
	}
	return nil
}

type configStore struct {
	mu   sync.RWMutex
	path string
	cfg  Config
}

func loadConfig(path string) (*configStore, error) {
	cs := &configStore{path: path, cfg: defaultConfig()}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cs, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(b, &cs.cfg); err != nil {
		return nil, err
	}
	cs.cfg.AdminPathPrefix = strings.TrimSpace(cs.cfg.AdminPathPrefix)
	if cs.cfg.AdminPathPrefix == "" {
		cs.cfg.AdminPathPrefix = "/__proxy"
	}
	if !strings.HasPrefix(cs.cfg.AdminPathPrefix, "/") {
		cs.cfg.AdminPathPrefix = "/" + cs.cfg.AdminPathPrefix
	}
	cs.cfg.AdminPathPrefix = strings.TrimRight(cs.cfg.AdminPathPrefix, "/")
	return cs, nil
}

func (c *configStore) snapshot() Config {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg
}

func (c *configStore) update(cfg Config) error {
	cfg.AdminPathPrefix = strings.TrimSpace(cfg.AdminPathPrefix)
	if cfg.AdminPathPrefix == "" {
		cfg.AdminPathPrefix = "/__proxy"
	}
	if !strings.HasPrefix(cfg.AdminPathPrefix, "/") {
		cfg.AdminPathPrefix = "/" + cfg.AdminPathPrefix
	}
	cfg.AdminPathPrefix = strings.TrimRight(cfg.AdminPathPrefix, "/")
	c.mu.Lock()
	c.cfg = cfg
	c.mu.Unlock()
	return c.persist()
}

func (c *configStore) persist() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c.cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}
