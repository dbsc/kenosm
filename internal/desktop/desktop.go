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
	"syscall"
	"time"
)

// Config configures desktop automation.
type Config struct {
	Timeout time.Duration
}

// Window is a top-level window the agents can target.
type Window struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Desktop drives the user's desktop: launching programs, enumerating and
// focusing windows, taking screenshots of the screen or a specific window, and
// injecting mouse/keyboard input. It shells out to the standard X11 toolchain
// (xdotool for input and window control, plus a screenshot tool).
type Desktop struct {
	xdotool string
	shot    string
	timeout time.Duration
}

// New detects the required tools and returns a Desktop, or an error describing
// what is missing. The caller may treat a non-nil error as "desktop automation
// unavailable" and disable the feature without failing to start.
func New(cfg Config) (*Desktop, error) {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	d := &Desktop{timeout: timeout}
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
	return d, nil
}

// Available reports whether the desktop tools are present.
func (d *Desktop) Available() bool { return d != nil && d.xdotool != "" }

func lookup(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
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

// Windows returns the visible top-level windows (id + title).
func (d *Desktop) Windows() ([]Window, error) {
	out, err := d.run(d.xdotool, "search", "--onlyvisible", "--name", ".*")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(out)
	wins := make([]Window, 0, len(ids))
	for _, id := range ids {
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

// Focus raises and focuses a window by numeric id or by a title substring.
func (d *Desktop) Focus(target string) error {
	id, err := d.windowID(target)
	if err != nil {
		return err
	}
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

// MouseMove moves the pointer to absolute screen coordinates.
func (d *Desktop) MouseMove(x, y int) error {
	_, err := d.run(d.xdotool, "mousemove", "--sync", strconv.Itoa(x), strconv.Itoa(y))
	return err
}

// Click moves the pointer to (x, y) and presses a mouse button: "left",
// "middle" or "right".
func (d *Desktop) Click(x, y int, button string) error {
	b := mouseButton(button)
	if _, err := d.run(d.xdotool, "mousemove", "--sync", strconv.Itoa(x), strconv.Itoa(y)); err != nil {
		return err
	}
	_, err := d.run(d.xdotool, "click", b)
	return err
}

// Type types text into the focused window as keyboard input.
func (d *Desktop) Type(text string) error {
	_, err := d.run(d.xdotool, "type", "--delay", "25", text)
	return err
}

// Key presses a key or key combination (e.g. "Return", "ctrl+c", "alt+Tab").
func (d *Desktop) Key(key string) error {
	_, err := d.run(d.xdotool, "key", "--clearmodifiers", key)
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
// numeric window id.
func (d *Desktop) windowID(target string) (string, error) {
	t := strings.TrimSpace(target)
	if isDigits(t) {
		return t, nil
	}
	out, err := d.run(d.xdotool, "search", "--onlyvisible", "--name", t)
	if err != nil {
		return "", err
	}
	ids := strings.Fields(out)
	if len(ids) == 0 {
		return "", fmt.Errorf("no visible window matches %q", target)
	}
	return ids[0], nil
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
