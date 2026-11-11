package transport

import (
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// ProxySpec describes a SOCKS5 proxy endpoint.
type ProxySpec struct {
	Host     string
	Port     int
	Username string
	Password string
}

// Cache holds one http.Transport per distinct proxy spec so connections are
// pooled and reused across requests.
type Cache struct {
	mu sync.Mutex
	m  map[string]*http.Transport
}

func NewCache() *Cache {
	return &Cache{m: make(map[string]*http.Transport)}
}

func (c *Cache) key(p ProxySpec) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%d|%s|%s", p.Host, p.Port, p.Username, p.Password)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// Get returns a cached transport for the spec, building it on first use.
func (c *Cache) Get(p ProxySpec) (*http.Transport, error) {
	k := c.key(p)
	c.mu.Lock()
	defer c.mu.Unlock()
	if t, ok := c.m[k]; ok {
		return t, nil
	}
	t, err := build(p)
	if err != nil {
		return nil, err
	}
	c.m[k] = t
	return t, nil
}

// Evict drops the cached transport for a spec so a changed service rebuilds it.
func (c *Cache) Evict(p ProxySpec) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, c.key(p))
}

// New builds a single SOCKS5-backed http.Transport for the given proxy spec.
func New(p ProxySpec) (*http.Transport, error) {
	return build(p)
}

func build(p ProxySpec) (*http.Transport, error) {
	var auth *proxy.Auth
	if p.Username != "" || p.Password != "" {
		auth = &proxy.Auth{User: p.Username, Password: p.Password}
	}

	dialer, err := proxy.SOCKS5(
		"tcp",
		net.JoinHostPort(p.Host, strconv.Itoa(p.Port)),
		auth,
		&net.Dialer{Timeout: 15 * time.Second},
	)
	if err != nil {
		return nil, fmt.Errorf("create socks5 dialer: %w", err)
	}

	ctxDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return nil, fmt.Errorf("socks5 dialer does not implement ContextDialer")
	}

	return &http.Transport{
		DialContext:           ctxDialer.DialContext,
		ForceAttemptHTTP2:     false, // HTTP/1.1 is more reliable through arbitrary SOCKS5 proxies
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}, nil
}
