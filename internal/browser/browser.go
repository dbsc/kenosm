package browser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Config configures a headless browser used to screenshot pages.
type Config struct {
	Bin     string
	Timeout time.Duration
}

// Browser drives a headless Chromium to render a page and capture a screenshot.
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

// Screenshot renders target (an http(s) URL or a local file path) and returns
// a PNG screenshot.
func (b *Browser) Screenshot(ctx context.Context, target string) ([]byte, error) {
	url := toURL(target)

	tmp, err := os.CreateTemp("", "kenosm-shot-*.png")
	if err != nil {
		return nil, err
	}
	name := tmp.Name()
	tmp.Close()
	os.Remove(name) // chromium will (re)create it
	defer os.Remove(name)

	cctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()

	args := []string{
		"--headless",
		"--disable-gpu",
		"--no-sandbox",
		"--hide-scrollbars",
		"--window-size=1024,640",
		"--virtual-time-budget=1500",
		"--screenshot=" + name,
		url,
	}
	cmd := exec.CommandContext(cctx, b.bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("browser screenshot: %w: %s", err, strings.TrimSpace(string(out)))
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errors.New("browser produced an empty screenshot")
	}
	return data, nil
}

// toURL converts a local file path into a file:// URL and passes http(s) URLs
// through unchanged.
func toURL(target string) string {
	t := strings.TrimSpace(target)
	if strings.HasPrefix(t, "http://") || strings.HasPrefix(t, "https://") {
		return t
	}
	abs, err := filepath.Abs(t)
	if err != nil {
		abs = t
	}
	return "file://" + filepath.ToSlash(abs)
}
