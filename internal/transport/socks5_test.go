package transport

import (
	"testing"
)

func TestCacheBuildsTransportWithAuth(t *testing.T) {
	c := NewCache()
	spec := ProxySpec{Host: "127.0.0.1", Port: 1080, Username: "user", Password: "pass"}
	tr, err := c.Get(spec)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tr == nil {
		t.Fatal("expected non-nil transport")
	}
	if tr.DialContext == nil {
		t.Fatal("expected DialContext to be set")
	}
}

func TestCacheBuildsDirectTransportWithoutProxy(t *testing.T) {
	c := NewCache()
	tr, err := c.Get(ProxySpec{})
	if err != nil {
		t.Fatalf("Get(direct): %v", err)
	}
	if tr == nil {
		t.Fatal("expected non-nil transport for direct connections")
	}
	if tr.DialContext != nil {
		t.Fatal("direct transport must not carry a SOCKS5 DialContext")
	}
}

func TestCacheBuildsTransportWithoutAuth(t *testing.T) {
	c := NewCache()
	spec := ProxySpec{Host: "127.0.0.1", Port: 1080}
	if _, err := c.Get(spec); err != nil {
		t.Fatalf("Get without auth: %v", err)
	}
}

func TestCacheReusesTransport(t *testing.T) {
	c := NewCache()
	spec := ProxySpec{Host: "10.0.0.1", Port: 9050}
	tr1, err := c.Get(spec)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	tr2, err := c.Get(spec)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tr1 != tr2 {
		t.Fatal("expected same transport instance for identical spec")
	}
}

func TestEvict(t *testing.T) {
	c := NewCache()
	spec := ProxySpec{Host: "10.0.0.1", Port: 9050}
	tr1, _ := c.Get(spec)
	c.Evict(spec)
	tr2, _ := c.Get(spec)
	if tr1 == tr2 {
		t.Fatal("expected a rebuilt transport after eviction")
	}
}
