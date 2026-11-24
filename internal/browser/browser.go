package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"

	"kenosm/internal/transport"
)

// Config configures a headless browser used to drive and screenshot pages.
type Config struct {
	Bin     string
	Timeout time.Duration
}

// Browser launches headless Chromium sessions for interactive automation.
type Browser struct {
	bin     string
	timeout time.Duration
}

func New(cfg Config) (*Browser, error) {
	bin := cfg.Bin
	if bin == "" {
		bin = Detect()
	}
	if bin == "" {
		return nil, errors.New("no headless browser found: install chromium (pacman -S chromium)")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &Browser{bin: bin, timeout: timeout}, nil
}

// Detect returns the path to a known headless browser, or "" if none is found.
func Detect() string {
	for _, name := range []string{"chromium", "google-chrome", "google-chrome-stable", "chromium-browser", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// Session is a live headless-browser session whose state (URL, scroll position,
// focused element, DOM) persists across the individual actions, so an agent can
// navigate somewhere, click around and screenshot each step in one session.
type Session struct {
	ctx     context.Context
	cancel  context.CancelFunc
	timeout time.Duration

	mu        sync.Mutex // serializes CDP calls (agent actions + live-view captures)
	closed    bool
	lastURL   string
	lastTitle string
	lastShot  []byte
}

// NewSession launches a headless browser bound to parent and routed through the
// given SOCKS5 proxy (empty proxy = direct connection). The browser process is
// torn down when parent is canceled (or Close is called).
func (b *Browser) NewSession(parent context.Context, proxy transport.ProxySpec) (*Session, error) {
	// A private, per-session profile: without --user-data-dir Chromium would
	// use the real user profile, colliding with a running browser (its
	// singleton lock refuses a second instance) and exposing the user's
	// cookies/sessions to the agent's headless instance.
	profileDir, err := os.MkdirTemp("", "kenosm-chrome-*")
	if err != nil {
		return nil, fmt.Errorf("create browser profile dir: %w", err)
	}

	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts,
		chromedp.ExecPath(b.bin),
		chromedp.UserDataDir(profileDir),
		chromedp.NoSandbox,
		chromedp.DisableGPU,
		chromedp.Flag("hide-scrollbars", true),
		chromedp.WindowSize(1280, 800),
		chromedp.UserAgent("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"),
	)

	// Route the browser through the agent's own SOCKS5 proxy. Chromium cannot
	// supply SOCKS5 credentials via --proxy-server, so when the proxy needs auth
	// a tiny local relay injects them.
	var relay *socksRelay
	if proxy.Host != "" {
		server := "socks5://" + net.JoinHostPort(proxy.Host, strconv.Itoa(proxy.Port))
		if proxy.Username != "" || proxy.Password != "" {
			r, err := startSocksRelay(proxy)
			if err != nil {
				return nil, fmt.Errorf("start socks relay: %w", err)
			}
			relay = r
			server = "socks5://" + r.addr()
		}
		opts = append(opts, chromedp.Flag("proxy-server", server))
	}

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(parent, opts...)
	ctx, cancelCtx := chromedp.NewContext(allocCtx, chromedp.WithLogf(func(string, ...any) {}))

	closeAll := func() {
		cancelCtx()
		cancelAlloc()
		if relay != nil {
			relay.close()
		}
		_ = os.RemoveAll(profileDir)
	}

	s := &Session{
		ctx:     ctx,
		timeout: b.timeout,
		cancel:  closeAll,
	}

	// Start the browser now, bound to the long-lived ctx, so that per-action
	// timeouts below never tear the whole browser down (see NewContext docs).
	if err := chromedp.Do(ctx); err != nil {
		closeAll()
		return nil, err
	}
	return s, nil
}

// Close shuts the browser down. Safe to call more than once.
func (s *Session) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}

// run executes a batch of void actions under the session timeout. Calls are
// serialized so the live-view capture never interleaves with an agent action
// on the same CDP pipe.
func (s *Session) run(actions ...chromedp.Action[chromedp.Void]) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(s.ctx, s.timeout)
	defer cancel()
	return chromedp.Do(ctx, actions...)
}

// runValue executes a single value-returning action under the session timeout.
func (s *Session) runValue[T any](a chromedp.Action[T]) (T, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(s.ctx, s.timeout)
	defer cancel()
	return chromedp.Run(ctx, a)
}

// Latest returns a fresh snapshot of the session's state for the live browser
// view: page URL, title and a PNG of the viewport. When the capture fails
// (page mid-navigation) the last good snapshot is returned; a closed session
// returns nothing, so the live view stops showing dead browsers.
func (s *Session) Latest() (url, title string, png []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", "", nil
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	url, errU := chromedp.Run(ctx, chromedp.Location())
	title, errT := chromedp.Run(ctx, chromedp.Title())
	png, errP := chromedp.Run(ctx, chromedp.CaptureScreenshot())
	if errU != nil || errT != nil || errP != nil {
		return s.lastURL, s.lastTitle, s.lastShot
	}
	s.lastURL, s.lastTitle, s.lastShot = url, title, png
	return url, title, png
}

// Navigate loads a URL and waits for the page body to settle. A deadline
// timeout is reported as success: heavy sites often exceed the per-action
// budget while still loading, and the caller's next screenshot shows the real
// page state (an error page or blank viewport will be visible there, so
// nothing is silently faked).
func (s *Session) Navigate(url string) error {
	err := s.run(
		chromedp.Navigate(url),
		chromedp.WaitReady(chromedp.CSS("body")),
		chromedp.Sleep(700*time.Millisecond),
	)
	if err != nil && errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	return err
}

// Click clicks the element matched by selector (a CSS selector, or the
// "[data-kenosm=N]" handle produced by Snapshot).
func (s *Session) Click(selector string) error {
	return s.run(
		chromedp.WaitReady(chromedp.CSS(selector)),
		chromedp.Click(chromedp.CSS(selector)),
	)
}

// Type focuses the element and types text into it (for form fields, search
// boxes and simple text challenges).
func (s *Session) Type(selector, text string) error {
	return s.run(
		chromedp.WaitReady(chromedp.CSS(selector)),
		chromedp.Click(chromedp.CSS(selector)),
		chromedp.SendKeys(chromedp.CSS(selector), text),
	)
}

// Scroll scrolls the viewport by pixels (negative scrolls up).
func (s *Session) Scroll(pixels int) error {
	return s.run(chromedp.Evaluate[chromedp.Void](fmt.Sprintf("window.scrollBy(0, %d)", pixels)))
}

// Screenshot captures the current viewport as a PNG.
func (s *Session) Screenshot() ([]byte, error) {
	return s.runValue(chromedp.CaptureScreenshot())
}

// Text returns the visible text of the page body.
func (s *Session) Text() (string, error) {
	return s.runValue(chromedp.Text(chromedp.CSS("body")))
}

// URL returns the browser's current location.
func (s *Session) URL() (string, error) {
	return s.runValue(chromedp.Location())
}

// Eval runs raw JavaScript in the page and returns the JSON-decoded value of
// the last expression (nil when the expression evaluates to nothing).
func (s *Session) Eval(expr string) (any, error) {
	raw, err := s.runValue(chromedp.Evaluate[[]byte](expr))
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw), nil
	}
	return v, nil
}

// WaitReady waits until the element matched by selector is present and visible.
func (s *Session) WaitReady(selector string) error {
	return s.run(chromedp.WaitReady(chromedp.CSS(selector)))
}

// snapshotJS walks the visible interactive elements, tags each with a stable
// data-kenosm index and returns a compact description of every one so the model
// can pick what to click or type into.
const snapshotJS = `(() => {
  const sel = 'a,button,input,select,textarea,[role="button"],[onclick],[tabindex]';
  const els = Array.from(document.querySelectorAll(sel)).filter((el) => {
    const r = el.getBoundingClientRect();
    return r.width > 0 && r.height > 0;
  });
  els.forEach((el, i) => el.setAttribute('data-kenosm', String(i)));
  return els.map((el, i) => {
    const tag = el.tagName.toLowerCase();
    const id = el.id ? '#' + el.id : '';
    let cls = '';
    if (typeof el.className === 'string' && el.className.trim()) {
      cls = '.' + el.className.trim().split(/\s+/).join('.');
    }
    const attrs = [];
    if (el.getAttribute('name')) attrs.push('name=' + el.getAttribute('name'));
    if (el.type) attrs.push('type=' + el.type);
    if (el.getAttribute('role')) attrs.push('role=' + el.getAttribute('role'));
    let text = (el.innerText || el.value || el.getAttribute('placeholder') || el.getAttribute('aria-label') || '').trim().replace(/\s+/g, ' ').slice(0, 90);
    return i + ' | <' + tag + id + cls + (attrs.length ? ' ' + attrs.join(' ') : '') + '> ' + text;
  });
})()`

// Snapshot returns the visible interactive elements (each tagged with its
// index) so callers can click/type by index afterwards.
func (s *Session) Snapshot() ([]string, error) {
	return s.runValue(chromedp.Evaluate[[]string](snapshotJS))
}

// SelectorFor converts a user-supplied target into a CSS selector. A bare
// integer is treated as the index returned by Snapshot.
func SelectorFor(target string) string {
	t := strings.TrimSpace(target)
	if t == "" {
		return ""
	}
	if isDigits(t) {
		return fmt.Sprintf(`[data-kenosm="%s"]`, t)
	}
	return t
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
