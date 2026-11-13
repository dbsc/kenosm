package browser

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
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
}

// NewSession launches a headless browser and returns a session bound to parent.
// The browser process is torn down when parent is canceled (or Close is called).
func (b *Browser) NewSession(parent context.Context) (*Session, error) {
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts,
		chromedp.ExecPath(b.bin),
		chromedp.NoSandbox,
		chromedp.DisableGPU,
		chromedp.Flag("hide-scrollbars", true),
		chromedp.WindowSize(1280, 800),
		chromedp.UserAgent("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"),
	)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(parent, opts...)
	ctx, cancelCtx := chromedp.NewContext(allocCtx, chromedp.WithLogf(func(string, ...any) {}))

	s := &Session{
		ctx:     ctx,
		timeout: b.timeout,
		cancel: func() {
			cancelCtx()
			cancelAlloc()
		},
	}

	// Start the browser now, bound to the long-lived ctx, so that per-action
	// timeouts below never tear the whole browser down (see NewContext docs).
	if err := chromedp.Do(ctx); err != nil {
		cancelCtx()
		cancelAlloc()
		return nil, err
	}
	return s, nil
}

// Close shuts the browser down. Safe to call more than once.
func (s *Session) Close() {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}

// run executes a batch of void actions under the session timeout.
func (s *Session) run(actions ...chromedp.Action[chromedp.Void]) error {
	ctx, cancel := context.WithTimeout(s.ctx, s.timeout)
	defer cancel()
	return chromedp.Do(ctx, actions...)
}

// runValue executes a single value-returning action under the session timeout.
func (s *Session) runValue[T any](a chromedp.Action[T]) (T, error) {
	ctx, cancel := context.WithTimeout(s.ctx, s.timeout)
	defer cancel()
	return chromedp.Run(ctx, a)
}

// Navigate loads a URL and waits for the page body to settle.
func (s *Session) Navigate(url string) error {
	return s.run(
		chromedp.Navigate(url),
		chromedp.WaitReady(chromedp.CSS("body")),
		chromedp.Sleep(700*time.Millisecond),
	)
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
