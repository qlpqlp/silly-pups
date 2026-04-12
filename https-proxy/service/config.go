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
	ListenPort int    `json:"listen_port"` // local TLS port, e.g. 8080, 10000, 4444
	Upstream   string `json:"upstream"`    // e.g. http://127.0.0.1:8080 — plain HTTP backend for that pup
}

// Config is persisted JSON for the proxy + TLS SANs.
type Config struct {
	TLSDomains      []string       `json:"tls_domains"`
	TLSIPs          []string       `json:"tls_ips"`
	Listeners       []PortListener `json:"listeners,omitempty"` // if non-empty, each row is https://dogebox:<port> -> upstream
	Routes          []ProxyRoute   `json:"routes"`
	DefaultUpstream string         `json:"default_upstream,omitempty"`
	AdminPathPrefix string         `json:"admin_path_prefix"` // default /__proxy
}

func defaultConfig() Config {
	return Config{
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
