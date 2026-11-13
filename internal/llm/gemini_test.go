package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testClient returns a Gemini client wired to an httptest server via its
// transport, so requests bypass the SOCKS5 proxy entirely.
func testClient(t *testing.T, srv *httptest.Server, primary string, fallbacks []string) *Gemini {
	t.Helper()
	g, err := New(Config{
		BaseURL:        srv.URL,
		APIKey:         "test-key",
		Model:          primary,
		FallbackModels: fallbacks,
		Transport:      srv.Client().Transport,
		MaxRetries:     2,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return g
}

const okBody = `{"candidates":[{"content":{"parts":[{"text":"hello"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}`

func TestGenerateDowngradesOn429(t *testing.T) {
	var primaryHits, liteHits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "gemini-3.5-flash:"):
			primaryHits.Add(1)
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":429,"message":"quota exhausted","status":"RESOURCE_EXHAUSTED"}}`))
		case strings.Contains(r.URL.Path, "gemini-3.5-flash-lite:"):
			liteHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(okBody))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	g := testClient(t, srv, "gemini-3.5-flash", []string{"gemini-3.5-flash-lite"})

	res, err := g.Generate(context.Background(), "sys", []Turn{{Role: "user", Text: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if res.Text != "hello" {
		t.Fatalf("text = %q, want hello", res.Text)
	}
	if primaryHits.Load() != 1 {
		t.Fatalf("primary hits = %d, want 1 (429 should not retry the same model)", primaryHits.Load())
	}
	if liteHits.Load() != 1 {
		t.Fatalf("lite hits = %d, want 1", liteHits.Load())
	}
	if got := g.Model(); got != "gemini-3.5-flash-lite" {
		t.Fatalf("sticky model = %q, want gemini-3.5-flash-lite", got)
	}
}

func TestGenerateDoesNotDowngradeOn400(t *testing.T) {
	var primaryHits, liteHits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "gemini-3.5-flash:") {
			primaryHits.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"message":"bad request","status":"INVALID_ARGUMENT"}}`))
			return
		}
		liteHits.Add(1)
	}))
	defer srv.Close()

	g := testClient(t, srv, "gemini-3.5-flash", []string{"gemini-3.5-flash-lite"})

	if _, err := g.Generate(context.Background(), "sys", []Turn{{Role: "user", Text: "hi"}}, nil); err == nil {
		t.Fatal("expected error for 400, got nil")
	}
	if liteHits.Load() != 0 {
		t.Fatalf("lite hits = %d, want 0 (400 must not downgrade)", liteHits.Load())
	}
}

func TestGenerateSticksToFallbackOnSubsequentCalls(t *testing.T) {
	var liteHits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "gemini-3.5-flash-lite:") {
			liteHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(okBody))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	g := testClient(t, srv, "gemini-3.5-flash", []string{"gemini-3.5-flash-lite"})
	// Force the client onto the fallback model.
	g.stickModel("gemini-3.5-flash-lite")

	if _, err := g.Generate(context.Background(), "sys", []Turn{{Role: "user", Text: "hi"}}, nil); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if liteHits.Load() != 1 {
		t.Fatalf("lite hits = %d, want 1 (sticky model should be used first)", liteHits.Load())
	}
}

func TestNewRequiresModel(t *testing.T) {
	if _, err := New(Config{BaseURL: "https://example.com", APIKey: "k"}); err == nil {
		t.Fatal("expected error when model is empty")
	}
}

func TestGenerateDowngradesOnTimeout(t *testing.T) {
	var liteHits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "gemini-3.5-flash:") {
			// Simulate a dead model: sleep past the client's timeout so the
			// request fails with a deadline error instead of an HTTP response.
			time.Sleep(500 * time.Millisecond)
			return
		}
		liteHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okBody))
	}))
	defer srv.Close()

	g, err := New(Config{
		BaseURL:        srv.URL,
		APIKey:         "test-key",
		Model:          "gemini-3.5-flash",
		FallbackModels: []string{"gemini-3.5-flash-lite"},
		Transport:      srv.Client().Transport,
		Timeout:        200 * time.Millisecond,
		MaxRetries:     1,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := g.Generate(context.Background(), "sys", []Turn{{Role: "user", Text: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if res.Text != "hello" {
		t.Fatalf("text = %q, want hello", res.Text)
	}
	if liteHits.Load() != 1 {
		t.Fatalf("lite hits = %d, want 1 (timeout must downgrade to the next model)", liteHits.Load())
	}
}

func TestIsDowngradeTrigger(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&APIError{StatusCode: 429, Code: 429, Status: "RESOURCE_EXHAUSTED"}, true},
		{&APIError{StatusCode: 500, Code: 500}, true},
		{&APIError{StatusCode: 503, Code: 503}, true},
		{&APIError{StatusCode: 400, Code: 400}, false},
		{&APIError{StatusCode: 401, Code: 401}, false},
		{fmt.Errorf("connection refused"), true},
		{context.DeadlineExceeded, true},
	}
	for _, c := range cases {
		if got := isDowngradeTrigger(c.err); got != c.want {
			t.Errorf("isDowngradeTrigger(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
