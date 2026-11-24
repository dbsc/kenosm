package handler

import (
	"testing"

	"kenosm/internal/model"
)

func svcWithUpstream(url string) *model.Service {
	return &model.Service{
		Slug:        "test",
		UpstreamURL: url,
		ProxyPort:   1080,
	}
}

func TestValidateServiceProxyOptional(t *testing.T) {
	a := &Admin{}
	s := svcWithUpstream("https://api.deepseek.com")
	s.ProxyHost = ""
	s.ProxyPort = 0
	if err := a.validateService(s); err != nil {
		t.Fatalf("direct (proxy-less) service must be valid: %v", err)
	}

	s.ProxyHost = "proxy.example.com"
	s.ProxyPort = 1080
	if err := a.validateService(s); err != nil {
		t.Fatalf("proxied service must be valid: %v", err)
	}

	s.ProxyPort = 0
	if err := a.validateService(s); err == nil {
		t.Fatal("proxy host set with port 0 must be rejected")
	}
}

func TestValidateServiceBlocksPrivateUpstream(t *testing.T) {
	a := &Admin{}
	for _, url := range []string{
		"http://127.0.0.1:8080/v1",
		"http://localhost:8080/v1",
		"http://169.254.169.254/latest/meta-data",
		"http://metadata.internal/v1",
	} {
		if err := a.validateService(svcWithUpstream(url)); err == nil {
			t.Fatalf("upstream %s must be rejected", url)
		}
	}

	if err := a.validateService(svcWithUpstream("http://10.0.0.5:8080/v1")); err == nil {
		t.Fatal("private-range upstream must be rejected without ALLOW_PRIVATE_UPSTREAM")
	}

	allowed := &Admin{allowPrivate: true}
	if err := allowed.validateService(svcWithUpstream("http://10.0.0.5:8080/v1")); err != nil {
		t.Fatalf("private upstream must be allowed with the opt-in: %v", err)
	}
	if err := allowed.validateService(svcWithUpstream("http://127.0.0.1:8080/v1")); err == nil {
		t.Fatal("loopback must stay blocked even with the opt-in")
	}

	if err := a.validateService(svcWithUpstream("https://api.deepseek.com")); err != nil {
		t.Fatalf("public upstream must be valid: %v", err)
	}
}
