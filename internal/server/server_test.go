package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthorized(t *testing.T) {
	const key = "s3cret"
	cases := []struct {
		name string
		req  func() *http.Request
		want bool
	}{
		{"none", func() *http.Request { return httptest.NewRequest("GET", "/", nil) }, false},
		{"wrong bearer", func() *http.Request {
			r := httptest.NewRequest("GET", "/", nil)
			r.Header.Set("Authorization", "Bearer wrong")
			return r
		}, false},
		{"right bearer", func() *http.Request {
			r := httptest.NewRequest("GET", "/", nil)
			r.Header.Set("Authorization", "Bearer s3cret")
			return r
		}, true},
		{"x-api-key", func() *http.Request {
			r := httptest.NewRequest("GET", "/", nil)
			r.Header.Set("X-API-Key", key)
			return r
		}, true},
		{"query key", func() *http.Request { return httptest.NewRequest("GET", "/?key=s3cret", nil) }, true},
		{"empty key", func() *http.Request {
			r := httptest.NewRequest("GET", "/", nil)
			r.Header.Set("X-API-Key", "")
			return r
		}, false},
	}
	for _, c := range cases {
		if got := authorized(c.req(), key); got != c.want {
			t.Errorf("%s: authorized = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAPIKeyAuthMiddleware(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := apiKeyAuth("s3cret")(next)

	t.Run("no key is 401", func(t *testing.T) {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/stats", nil))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rr.Code)
		}
	})

	t.Run("valid bearer passes", func(t *testing.T) {
		rr := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/api/v1/stats", nil)
		r.Header.Set("Authorization", "Bearer s3cret")
		h.ServeHTTP(rr, r)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rr.Code)
		}
	})
}
