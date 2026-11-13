package desktop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Config configures desktop automation.
type Config struct {
	Timeout     time.Duration
	LayoutDelay time.Duration // how long to wait after switching keyboard layout
}

// Window is a top-level window the agents can target.
type Window struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Desktop drives the user's desktop: launching programs, enumerating and
// focusing windows, taking screenshots, and injecting keyboard/mouse input.
//
// Window enumeration, focus and screenshots go through the X11 toolchain
// (xdotool + a screenshot tool). Keyboard input is injected through ydotool
// (kernel uinput) when available, because synthetic X11 key events are
// unreliable on Wayland (lost key-ups stick keys and cause autorepeat);
// if ydotool is missing it falls back to xdotool for X11 sessions.
type Desktop struct {
	xdotool     string
	shot        string
	ydotool     string
	ydSocket    string
	timeout     time.Duration
	layoutDelay time.Duration
	gs          bool           // gsettings available for layout switching
	mu          sync.Mutex     // serializes input that depends on the keyboard layout
	focused     string         // last window focused via Focus (target for the xdotool fallback)
	uinput      *uinputPointer // virtual absolute pointer (nil when unavailable)
}

// New detects the required tools and returns a Desktop, or an error describing
// what is missing. The caller may treat a non-nil error as "desktop automation
// unavailable" and disable the feature without failing to start.
func New(cfg Config) (*Desktop, error) {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	delay := cfg.LayoutDelay
	if delay <= 0 {
		delay = 300 * time.Millisecond
	}
	d := &Desktop{timeout: timeout, layoutDelay: delay}
	d.xdotool = lookup("xdotool")
	if d.xdotool == "" {
		return nil, errors.New("desktop automation requires xdotool (pacman -S xdotool)")
	}
	for _, name := range []string{"maim", "scrot", "gnome-screenshot", "grim", "import"} {
		if p := lookup(name); p != "" {
			d.shot = p
			break
		}
	}
	if d.shot == "" {
		return nil, errors.New("desktop automation requires a screenshot tool (maim, scrot, gnome-screenshot, grim or import)")
	}
	d.gs = lookup("gsettings") != ""
	if d.ydotool = lookup("ydotool"); d.ydotool != "" {
		if d.ydSocket = detectYdSocket(); d.ydSocket == "" {
			d.ydotool = "" // no ydotoold daemon running
		}
	}
	// Create the virtual absolute pointer (uinput) so the cursor can be moved on
	// Wayland, where XTEST and REL pointer events are ignored. If it fails (no
	// /dev/uinput access), mouse tools fall back to xdotool (X11 only).
	if w, h, err := d.ScreenSize(); err == nil {
		if p, err := newUinputPointer(w, h); err == nil {
			d.uinput = p
		}
	}
	return d, nil
}

// Available reports whether the desktop tools are present.
func (d *Desktop) Available() bool { return d != nil && d.xdotool != "" }

// Close releases any kernel resources held by the desktop automation layer (the
// virtual uinput pointer device). It is safe to call on a nil Desktop.
func (d *Desktop) Close() {
	if d == nil {
		return
	}
	if d.uinput != nil {
		d.uinput.close()
		d.uinput = nil
	}
}

func lookup(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}

// detectYdSocket returns the path to a running ydotoold socket, or "".
func detectYdSocket() string {
	if s := os.Getenv("YDOTOOL_SOCKET"); s != "" {
		if _, err := os.Stat(s); err == nil {
			return s
		}
	}
	candidates := []string{"/tmp/.ydotool_socket"}
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		candidates = append(candidates, filepath.Join(rt, ".ydotool_socket"))
	}
	for _, s := range candidates {
		if _, err := os.Stat(s); err == nil {
			return s
		}
	}
	return ""
}

// run runs a command with the session timeout and returns trimmed combined output.
func (d *Desktop) run(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("%s timed out", filepath.Base(name))
	}
	return strings.TrimSpace(string(out)), err
}

// runYd runs ydotool against the detected daemon socket.
func (d *Desktop) runYd(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.ydotool, args...)
	cmd.Env = append(os.Environ(), "YDOTOOL_SOCKET="+d.ydSocket)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("ydotool timed out")
	}
	if err != nil {
		return fmt.Errorf("ydotool: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Launch starts a program detached in its own session (so it survives kenosm)
// and returns its PID. The program inherits the user's display environment.
func (d *Desktop) Launch(program string, args []string, dir string) (int, error) {
	cmd := exec.Command(program, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	go cmd.Wait() // reap in the background; the child is already detached
	return cmd.Process.Pid, nil
}

// Windows returns the visible top-level windows (id + title), skipping the
// compositor's window decorations.
func (d *Desktop) Windows() ([]Window, error) {
	out, err := d.run(d.xdotool, "search", "--onlyvisible", "--name", ".*")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(out)
	wins := make([]Window, 0, len(ids))
	for _, id := range ids {
		if d.isFrame(id) {
			continue
		}
		title, _ := d.run(d.xdotool, "getwindowname", id)
		if len(wins) >= 200 {
			break
		}
		wins = append(wins, Window{ID: id, Title: title})
	}
	return wins, nil
}

// ActiveWindow returns the id and title of the currently focused window.
func (d *Desktop) ActiveWindow() (string, string, error) {
	id, err := d.run(d.xdotool, "getactivewindow")
	if err != nil {
		return "", "", err
	}
	title, _ := d.run(d.xdotool, "getwindowname", id)
	return id, title, nil
}

// Focus raises and focuses a window by numeric id or by a title substring, and
// remembers it as the target for subsequent input.
func (d *Desktop) Focus(target string) error {
	id, err := d.windowID(target)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.focused = id
	d.mu.Unlock()
	_, err = d.run(d.xdotool, "windowactivate", "--sync", id)
	return err
}

// ScreenSize returns the display geometry in pixels (width, height).
func (d *Desktop) ScreenSize() (int, int, error) {
	out, err := d.run(d.xdotool, "getdisplaygeometry")
	if err != nil {
		return 0, 0, err
	}
	f := strings.Fields(out)
	if len(f) < 2 {
		return 0, 0, fmt.Errorf("unexpected geometry output %q", out)
	}
	w, _ := strconv.Atoi(f[0])
	h, _ := strconv.Atoi(f[1])
	return w, h, nil
}

// MouseMove moves the pointer to absolute screen coordinates. It uses the
// virtual absolute pointer (uinput) so it works on Wayland; when that device is
// unavailable it falls back to xdotool (X11 sessions only).
func (d *Desktop) MouseMove(x, y int) error {
	if d.uinput != nil {
		return d.uinput.move(x, y)
	}
	_, err := d.run(d.xdotool, "mousemove", "--sync", strconv.Itoa(x), strconv.Itoa(y))
	return err
}

// Click moves the pointer to (x, y) and presses a mouse button: "left",
// "middle" or "right".
func (d *Desktop) Click(x, y int, button string) error {
	if d.uinput != nil {
		if err := d.uinput.move(x, y); err != nil {
			return err
		}
		code := uinputButton(button)
		if err := d.uinput.button(code, true); err != nil {
			return err
		}
		return d.uinput.button(code, false)
	}
	b := mouseButton(button)
	if _, err := d.run(d.xdotool, "mousemove", "--sync", strconv.Itoa(x), strconv.Itoa(y)); err != nil {
		return err
	}
	_, err := d.run(d.xdotool, "click", b)
	return err
}

// Type types text into the focused window. Two things make it reliable:
//
//  1. It uses ydotool (kernel uinput) rather than xdotool, because synthetic
//     X11 key events are unreliable on Wayland.
//  2. It temporarily switches the GNOME input source to US English, because
//     the compositor maps physical keycodes through the user's active layout
//     (a Russian layout would turn "echo" into Cyrillic). The user's layout is
//     restored afterwards.
func (d *Desktop) Type(text string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	orig := ""
	if d.gs {
		if cur := d.currentSources(); cur != "" && !usOnly(cur) {
			if err := d.setSources(usSources); err == nil {
				orig = cur
				time.Sleep(d.layoutDelay)
			}
		}
	}

	var err error
	if d.ydotool != "" {
		err = d.runYd("type", text)
	} else {
		args := []string{"type", "--delay", "25"}
		if d.focused != "" {
			args = append(args, "--window", d.focused)
		}
		args = append(args, text)
		_, err = d.run(d.xdotool, args...)
	}

	if orig != "" {
		_ = d.setSources(orig)
	}
	return err
}

// Key presses a key or key combination (e.g. "Return", "Escape", "ctrl+c",
// "alt+Tab", "shift+a"). Key names are translated to raw Linux keycodes and
// injected via ydotool when available.
func (d *Desktop) Key(key string) error {
	if d.ydotool != "" {
		if seq, ok := keySequence(key); ok {
			args := append([]string{"key"}, strings.Fields(seq)...)
			return d.runYd(args...)
		}
	}
	args := []string{"key", "--clearmodifiers"}
	if d.focused != "" {
		args = append(args, "--window", d.focused)
	}
	args = append(args, key)
	_, err := d.run(d.xdotool, args...)
	return err
}

// Scroll scrolls the wheel at the current pointer position: "up" or "down".
func (d *Desktop) Scroll(direction string, amount int) error {
	if amount <= 0 {
		amount = 1
	}
	btn := "4" // wheel up
	if strings.EqualFold(direction, "down") {
		btn = "5"
	}
	_, err := d.run(d.xdotool, "click", "--repeat", strconv.Itoa(amount), btn)
	return err
}

// Screenshot captures the whole screen as a PNG.
func (d *Desktop) Screenshot() ([]byte, error) {
	return d.capture("")
}

// ScreenshotWindow captures a specific window (by id or title) as a PNG.
func (d *Desktop) ScreenshotWindow(target string) ([]byte, error) {
	id, err := d.windowID(target)
	if err != nil {
		return nil, err
	}
	return d.capture(id)
}

// windowID resolves a target (a numeric window id or a title substring) into a
// numeric window id. When matching by title, it skips the compositor's window
// decorations (which share the client's title) and returns the real window.
func (d *Desktop) windowID(target string) (string, error) {
	t := strings.TrimSpace(target)
	if isDigits(t) {
		return t, nil
	}
	out, err := d.run(d.xdotool, "search", "--onlyvisible", "--name", t)
	if err != nil {
		return "", err
	}
	for _, id := range strings.Fields(out) {
		if d.isFrame(id) {
			continue
		}
		return id, nil
	}
	return "", fmt.Errorf("no visible window matches %q", target)
}

// wmFrameClasses are WM_CLASS values of window decorations that compositors
// create around XWayland clients. They inherit the client's WM_NAME, so a
// title search matches them too and they must be skipped.
var wmFrameClasses = []string{
	"mutter-x11-frames", // GNOME on Wayland
	"kwin_x11",          // KDE
	"openbox",
	"xfwm4",
	"metacity",
}

// isFrame reports whether a window id is a compositor decoration rather than a
// real application window.
func (d *Desktop) isFrame(id string) bool {
	out, err := d.run("xprop", "-id", id, "WM_CLASS")
	if err != nil {
		return false
	}
	lower := strings.ToLower(out)
	for _, c := range wmFrameClasses {
		if strings.Contains(lower, c) {
			return true
		}
	}
	return false
}

// capture produces a PNG screenshot of the screen (target == "") or a window.
func (d *Desktop) capture(target string) ([]byte, error) {
	tmp, err := os.CreateTemp("", "kenosm-desktop-*.png")
	if err != nil {
		return nil, err
	}
	name := tmp.Name()
	tmp.Close()
	os.Remove(name)
	defer os.Remove(name)

	base := filepath.Base(d.shot)
	var args []string
	switch {
	case strings.HasPrefix(base, "maim"):
		if target != "" {
			args = append(args, "-i", target)
		}
		args = append(args, name)
	case strings.HasPrefix(base, "scrot"):
		if target != "" {
			args = append(args, "-u") // scrot captures the active window only
		}
		args = append(args, name)
	case strings.HasPrefix(base, "gnome-screenshot"):
		args = append(args, "-f", name)
		if target != "" {
			args = append(args, "-w")
		}
	case strings.HasPrefix(base, "grim"):
		args = append(args, name) // grim captures the whole output
	case strings.HasPrefix(base, "import"):
		win := target
		if win == "" {
			win = "root"
		}
		args = append(args, "-window", win, name)
	default:
		return nil, fmt.Errorf("unsupported screenshot tool %q", d.shot)
	}

	if _, err := d.run(d.shot, args...); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errors.New("screenshot tool produced an empty image")
	}
	return data, nil
}

// usSources is the GNOME input-source list that leaves only US English active.
const usSources = "[('xkb','us')]"

// currentSources returns the active GNOME input-sources list, or "" if the
// setting cannot be read (no session bus, not GNOME, etc.).
func (d *Desktop) currentSources() string {
	out, err := d.run("gsettings", "get", "org.gnome.desktop.input-sources", "sources")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (d *Desktop) setSources(v string) error {
	_, err := d.run("gsettings", "set", "org.gnome.desktop.input-sources", "sources", v)
	return err
}

// usOnly reports whether the input-sources string is exactly US English.
func usOnly(s string) bool {
	return strings.ReplaceAll(s, " ", "") == "[('xkb','us')]"
}

// Linux input keycodes for the modifier keys (linux/input-event-codes.h).
const (
	keyLeftCtrl  = 29
	keyLeftShift = 42
	keyLeftAlt   = 56
	keyLeftMeta  = 125
)

// keycodes maps key names (lowercase) to Linux input keycodes. Only the keys an
// agent is likely to press are listed.
var keycodes = map[string]int{
	"esc": 1, "escape": 1,
	"1": 2, "2": 3, "3": 4, "4": 5, "5": 6, "6": 7, "7": 8, "8": 9, "9": 10, "0": 11,
	"minus": 12, "-": 12, "equal": 13, "=": 13,
	"backspace": 14, "tab": 15,
	"q": 16, "w": 17, "e": 18, "r": 19, "t": 20, "y": 21, "u": 22, "i": 23, "o": 24, "p": 25,
	"enter": 28, "return": 28,
	"a": 30, "s": 31, "d": 32, "f": 33, "g": 34, "h": 35, "j": 36, "k": 37, "l": 38,
	"semicolon": 39, "apostrophe": 40, "grave": 41, "backslash": 43,
	"z": 44, "x": 45, "c": 46, "v": 47, "b": 48, "n": 49, "m": 50,
	"comma": 51, "dot": 52, "slash": 53,
	"space": 57,
	"f1":    59, "f2": 60, "f3": 61, "f4": 62, "f5": 63, "f6": 64, "f7": 65, "f8": 66, "f9": 67, "f10": 68,
	"f11": 87, "f12": 88,
	"home": 102, "up": 103, "pageup": 104, "left": 105, "right": 106, "end": 107, "down": 108, "pagedown": 109,
	"insert": 110, "delete": 111, "del": 111,
}

// keySequence translates a key name or combination (e.g. "ctrl+c") into the
// ydotool keycode sequence "29:1 46:1 46:0 29:0". It returns ok=false for keys
// it cannot translate.
func keySequence(key string) (string, bool) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(key)), "+")
	var mods []int
	main := ""
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		switch p {
		case "ctrl", "control":
			mods = append(mods, keyLeftCtrl)
		case "alt":
			mods = append(mods, keyLeftAlt)
		case "shift":
			mods = append(mods, keyLeftShift)
		case "super", "win", "cmd", "meta":
			mods = append(mods, keyLeftMeta)
		default:
			kc, ok := keycodes[p]
			if !ok {
				return "", false
			}
			main = strconv.Itoa(kc)
		}
	}
	if main == "" {
		return "", false
	}
	seq := make([]string, 0, 2*len(mods)+2)
	for _, m := range mods {
		seq = append(seq, fmt.Sprintf("%d:1", m))
	}
	seq = append(seq, main+":1", main+":0")
	for i := len(mods) - 1; i >= 0; i-- {
		seq = append(seq, fmt.Sprintf("%d:0", mods[i]))
	}
	return strings.Join(seq, " "), true
}

func mouseButton(button string) string {
	switch strings.ToLower(strings.TrimSpace(button)) {
	case "middle", "2":
		return "2"
	case "right", "3":
		return "3"
	default:
		return "1" // left
	}
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
