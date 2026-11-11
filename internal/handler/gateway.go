package handler

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"kenosm/internal/logging"
	"kenosm/internal/model"
	"kenosm/internal/repository"
	"kenosm/internal/transport"
)

type ctxKey int

const (
	serviceCtxKey   ctxKey = iota
	remainingPathKey
	startTimeKey
)

// Gateway proxies incoming requests to a service's upstream AI API, routing
// the outbound connection through that service's SOCKS5 proxy.
type Gateway struct {
	repo       *repository.ServiceRepo
	transports *transport.Cache
	logger     *logging.Logger
	proxy      *httputil.ReverseProxy
}

func NewGateway(repo *repository.ServiceRepo, cache *transport.Cache, logger *logging.Logger) *Gateway {
	g := &Gateway{repo: repo, transports: cache, logger: logger}
	g.proxy = &httputil.ReverseProxy{
		Director:      g.director,
		Transport:     g, // route via the service-specific SOCKS5 transport
		ErrorHandler:  g.handleProxyError,
		FlushInterval: 100 * time.Millisecond, // keep SSE/streaming responses flowing
	}
	return g
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	remaining := chi.URLParam(r, "*")

	svc, err := g.repo.GetBySlug(r.Context(), slug)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "endpoint not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !svc.Enabled {
		writeError(w, http.StatusForbidden, "endpoint disabled")
		return
	}

	ctx := context.WithValue(r.Context(), serviceCtxKey, &svc)
	ctx = context.WithValue(ctx, remainingPathKey, remaining)
	ctx = context.WithValue(ctx, startTimeKey, time.Now())
	r = r.WithContext(ctx)

	rec := &statusRecorder{ResponseWriter: w}
	g.proxy.ServeHTTP(rec, r)

	status := rec.status
	if status == 0 {
		status = http.StatusOK
	}
	g.logRequest(r, status, rec.err)
}

// director rewrites the outgoing request to target the upstream URL and
// injects the service's auth header.
func (g *Gateway) director(req *http.Request) {
	svc, ok := req.Context().Value(serviceCtxKey).(*model.Service)
	if !ok {
		return
	}
	target, err := url.Parse(svc.UpstreamURL)
	if err != nil {
		return
	}
	remaining, _ := req.Context().Value(remainingPathKey).(string)

	req.URL.Scheme = target.Scheme
	req.URL.Host = target.Host
	req.URL.Path = joinURLPath(target.Path, remaining)
	req.Host = target.Host

	if svc.UpstreamAPIKey != "" {
		header := svc.AuthHeader
		if header == "" {
			header = "Authorization"
		}
		value := svc.UpstreamAPIKey
		if svc.AuthScheme != "" {
			value = svc.AuthScheme + " " + value
		}
		req.Header.Set(header, value)
	}
}

// RoundTrip selects the cached SOCKS5 transport for the request's service and
// delegates to it. Implements http.RoundTripper for httputil.ReverseProxy.
func (g *Gateway) RoundTrip(req *http.Request) (*http.Response, error) {
	svc, ok := req.Context().Value(serviceCtxKey).(*model.Service)
	if !ok {
		return nil, fmt.Errorf("gateway: missing service in request context")
	}
	t, err := g.transports.Get(transport.ProxySpec{
		Host:     svc.ProxyHost,
		Port:     svc.ProxyPort,
		Username: svc.ProxyUsername,
		Password: svc.ProxyPassword,
	})
	if err != nil {
		return nil, fmt.Errorf("gateway: build socks5 transport: %w", err)
	}
	return t.RoundTrip(req)
}

func (g *Gateway) handleProxyError(w http.ResponseWriter, r *http.Request, err error) {
	if rec, ok := w.(*statusRecorder); ok {
		rec.err = err
	}
	writeError(w, http.StatusBadGateway, "upstream error: "+err.Error())
}

func (g *Gateway) logRequest(r *http.Request, status int, err error) {
	svc, _ := r.Context().Value(serviceCtxKey).(*model.Service)
	if svc == nil {
		return
	}
	start, _ := r.Context().Value(startTimeKey).(time.Time)

	entry := model.RequestLog{
		ServiceID: svc.ID,
		RequestID: middleware.GetReqID(r.Context()),
		Method:    r.Method,
		Path:      r.URL.Path,
		Status:    status,
	}
	if !start.IsZero() {
		entry.LatencyMS = int(time.Since(start).Milliseconds())
	}
	if err != nil {
		entry.Error = err.Error()
	}
	g.logger.Log(entry)
}

func joinURLPath(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

// statusRecorder captures the response status and any proxy error while
// transparently passing through streaming, hijacking and efficient copies.
type statusRecorder struct {
	http.ResponseWriter
	status int
	err    error
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := r.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("hijack not supported")
}

func (r *statusRecorder) ReadFrom(src io.Reader) (int64, error) {
	if rf, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(r.ResponseWriter, src)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
