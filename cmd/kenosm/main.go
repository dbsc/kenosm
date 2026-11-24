// Command kenosm is the one-binary entry point: HTTP server, terminal chat,
// setup wizard and version info.
//
//	kenosm             run the server (API + gateway + web UI)
//	kenosm serve       same as bare invocation
//	kenosm chat        terminal chat with the agent swarm (Claude-Code style)
//	kenosm chat --serve   start the server first, then chat
//	kenosm setup       interactive first-run wizard (.env + database)
//	kenosm version     print the version
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"kenosm/internal/app"
	"kenosm/internal/config"
	"kenosm/internal/webview"
)

const version = "0.2.0"

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
		args = args[1:]
	}
	switch cmd {
	case "serve", "start", "server", "run":
		app.Run()
	case "chat":
		chatCommand(args)
	case "app", "ui", "open":
		appCommand()
	case "setup", "init":
		setupCommand()
	case "version", "--version", "-v":
		fmt.Println("kenosm", version)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Print(`kenosm — API gateway + autonomous agent swarm with a real browser.

Usage:
  kenosm               run the server (API + gateway + web UI)
  kenosm serve         same as bare invocation
  kenosm chat          terminal chat with the swarm (Claude-Code style)
  kenosm chat --serve  start the server first, then chat
  kenosm app           open the web UI in a standalone app window
  kenosm setup         interactive first-run wizard (.env + database)
  kenosm version       print the version

Chat commands:
  /swarm | /single | /polish   switch the run mode for the next message
  /new                         start a new chat
  exit | quit                  leave
`)
}

// ---------------------------------------------------------------------------
// Terminal chat
// ---------------------------------------------------------------------------

// Styles for ANSI terminals. Disabled automatically when the output is not a
// TTY (pipes, CI logs).
var (
	cReset  = "\x1b[0m"
	cDim    = "\x1b[2m"
	cBold   = "\x1b[1m"
	cUser   = "\x1b[1;32m" // green: the human
	cAgent  = "\x1b[1;36m" // cyan: an agent talking
	cTool   = "\x1b[2;37m" // dim: tool activity
	cVerify = "\x1b[1;33m" // yellow: goal verification
	cErr    = "\x1b[1;31m"
	cFinal  = "\x1b[0m"
)

func detectTTY() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func chatCommand(args []string) {
	if !detectTTY() {
		cReset, cDim, cBold, cUser, cAgent, cTool, cVerify, cErr = "", "", "", "", "", "", "", ""
	}

	addr := localAddr(config.Lookup("ADDR", "127.0.0.1:8080"))
	key := config.Lookup("KENOSM_API_KEY", "")

	startServer := false
	for _, a := range args {
		if a == "--serve" {
			startServer = true
		}
	}

	if startServer {
		fmt.Printf("%sstarting server (%s)…%s\n", cDim, addr, cReset)
		cmd := exec.Command(os.Args[0], "serve")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "%scannot start server: %v%s\n", cErr, err, cReset)
			os.Exit(1)
		}
		defer func() { _ = cmd.Process.Kill() }()
		if !waitHealth(addr, key, 30*time.Second) {
			fmt.Fprintf(os.Stderr, "%sserver did not become healthy%s\n", cErr, cReset)
			os.Exit(1)
		}
	}

	if !waitHealth(addr, key, 3*time.Second) {
		fmt.Printf("%sno server at %s — start it with `kenosm serve`, or `kenosm chat --serve`%s\n", cErr, addr, cReset)
		os.Exit(1)
	}
	fmt.Printf("%skenosm %s · server %s%s\n", cDim, version, addr, cReset)

	chat := createChat(addr, key)
	mode := "single"
	reader := bufio.NewReader(os.Stdin)

	for {
		fmt.Printf("%syou › %s", cUser, cReset)
		line, err := reader.ReadString('\n')
		if err != nil {
			fmt.Println()
			return
		}
		text := strings.TrimSpace(line)
		switch strings.ToLower(text) {
		case "", "help":
			fmt.Printf("%s/swarm · /single · /polish · /new · exit%s\n", cDim, cReset)
			continue
		case "exit", "quit", "/exit", "/quit":
			return
		case "/swarm", "/single", "/polish":
			mode = strings.TrimPrefix(text, "/")
			fmt.Printf("%smode: %s%s\n", cDim, mode, cReset)
			continue
		case "/new":
			chat = createChat(addr, key)
			fmt.Printf("%snew chat %s%s\n", cDim, chat, cReset)
			continue
		}

		runID, err := sendMessage(addr, key, chat, text, mode)
		if err != nil {
			fmt.Printf("%serror: %v%s\n", cErr, err, cReset)
			continue
		}
		streamRun(addr, key, runID)
	}
}

func waitHealth(addr, key string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/healthz", nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return true
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func createChat(addr, key string) string {
	resp, err := apiJSON(addr, key, http.MethodPost, "/api/v1/chats", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%scannot create chat: %v%s\n", cErr, err, cReset)
		return ""
	}
	var c struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(resp), &c)
	return c.ID
}

func apiJSON(addr, key, method, path string, body any) (string, error) {
	var rd *strings.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return "", err
		}
		rd = strings.NewReader(string(b))
	} else {
		rd = strings.NewReader("")
	}
	req, err := http.NewRequest(method, "http://"+addr+path, rd)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var sb strings.Builder
	_, _ = io.Copy(&sb, resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(sb.String()))
	}
	return sb.String(), nil
}

func sendMessage(addr, key, chatID, text, mode string) (string, error) {
	resp, err := apiJSON(addr, key, http.MethodPost, "/api/v1/chats/"+chatID+"/messages",
		map[string]string{"text": text, "mode": mode})
	if err != nil {
		return "", err
	}
	var out struct {
		RunID string `json:"run_id"`
	}
	_ = json.Unmarshal([]byte(resp), &out)
	return out.RunID, nil
}

// streamRun tails the run's SSE stream and renders it in the terminal,
// Claude-Code style: dim tool activity, colored agents, a highlighted
// verification pass and the final answer.
func streamRun(addr, key, runID string) {
	url := "http://" + addr + "/api/v1/swarm/run/" + runID + "/events"
	if key != "" {
		url += "?key=" + key
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Printf("%sstream error: %v%s\n", cErr, err, cReset)
		return
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	seenSub := map[string]bool{}

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var e struct {
			Type    string `json:"type"`
			Agent   string `json:"agent"`
			Model   string `json:"model"`
			Tool    string `json:"tool"`
			Detail  string `json:"detail"`
			Error   string `json:"error"`
			Result  *struct {
				Final string `json:"final"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e); err != nil {
			continue
		}
		switch e.Type {
		case "planning":
			fmt.Printf("%splanning…%s\n", cDim, cReset)
		case "subtask":
			key := e.Agent + e.Detail
			if !seenSub[key] {
				seenSub[key] = true
				fmt.Printf("%s● %s%s %s→ %s%s\n", cAgent, e.Agent, cReset, cDim, trunc(e.Detail, 120), cReset)
			}
		case "tool":
			fmt.Printf("%s  ⚙ %s %s%s\n", cTool, e.Tool, trunc(e.Detail, 100), cReset)
		case "subtask_done":
			fmt.Printf("%s  ✓ %s done%s\n", cDim, e.Agent, cReset)
		case "verify":
			fmt.Printf("%s⟳ verify: %s%s\n", cVerify, trunc(e.Detail, 200), cReset)
		case "fix_round":
			fmt.Printf("%s⟳ %s%s\n", cVerify, trunc(e.Detail, 200), cReset)
		case "warning":
			fmt.Printf("%s! %s%s\n", cVerify, trunc(e.Detail, 200), cReset)
		case "error":
			fmt.Printf("%s✗ %s%s\n", cErr, e.Error, cReset)
			return
		case "stopped":
			fmt.Printf("%s■ stopped%s\n", cDim, cReset)
			return
		case "done":
			if e.Result != nil {
				fmt.Printf("\n%s%s%s\n", cBold, wrap(e.Result.Final), cReset)
			}
			return
		}
	}
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func wrap(s string) string {
	const width = 88
	var sb strings.Builder
	for _, line := range strings.Split(s, "\n") {
		r := []rune(line)
		for len(r) > width {
			sb.WriteString(string(r[:width]) + "\n")
			r = r[width:]
		}
		sb.WriteString(string(r) + "\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// ---------------------------------------------------------------------------
// Setup wizard
// ---------------------------------------------------------------------------

// localAddr turns a server listen address into a dialable loopback address
// for a browser client: ":8080", "0.0.0.0:8080" and "[::]:8080" all become
// 127.0.0.1:8080. Non-loopback hosts are kept as-is.
func localAddr(addr string) string {
	if !strings.Contains(addr, ":") {
		addr += ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "127.0.0.1:8080"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// ---------------------------------------------------------------------------
// Database auto-start
// ---------------------------------------------------------------------------

// ensureEnv makes a first-run local install self-provisioning: when SECRET_KEY
// or KENOSM_API_KEY is missing from .env it generates and appends it, so the
// server starts without a setup wizard and without unauthenticated warnings.
func ensureEnv() {
	var missing []string
	if config.Lookup("SECRET_KEY", "") == "" {
		missing = append(missing, "SECRET_KEY="+randomHex(32))
	}
	if config.Lookup("KENOSM_API_KEY", "") == "" {
		missing = append(missing, "KENOSM_API_KEY="+randomHex(24))
	}
	if len(missing) == 0 {
		return
	}

	// Read the existing .env verbatim so comments and unknown lines survive;
	// append only the missing keys.
	var sb strings.Builder
	if data, err := os.ReadFile(".env"); err == nil {
		sb.Write(data)
		if !strings.HasSuffix(string(data), "\n") {
			sb.WriteString("\n")
		}
	}
	for _, line := range missing {
		sb.WriteString(line + "\n")
	}
	if err := os.WriteFile(".env", []byte(sb.String()), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "cannot write .env: %v\n", err)
		return
	}
	fmt.Printf("%sgenerated missing secret(s) in .env%s\n", cDim, cReset)
}

// ensureDatabase brings up the local PostgreSQL container via docker compose
// when the configured DATABASE_URL targets a loopback host that isn't reachable
// yet. It returns nil when there's nothing to do (remote/external DB, no
// docker, or the DB is already up) — the server's own migrate step reports any
// real connectivity problem with a clearer error.
func ensureDatabase() error {
	dbURL := config.Lookup("DATABASE_URL", "postgres://kenosm:kenosm@localhost:5432/kenosm?sslmode=disable")
	hostPort, local := dbTarget(dbURL)
	if !local || hostPort == "" {
		return nil
	}
	if tcpReachable(hostPort, time.Second) {
		return nil // already running
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return nil // no docker: an external postgres may still serve localhost
	}
	composeDir := locateComposeDir()
	if composeDir == "" {
		return nil // no compose file to manage
	}

	fmt.Printf("%sstarting database (docker compose up -d db)…%s\n", cDim, cReset)
	cmd := exec.Command("docker", "compose", "up", "-d", "db")
	cmd.Dir = composeDir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose up -d db: %w", err)
	}
	// The container is created (image already pulled), but postgres needs a few
	// seconds to accept connections on first boot.
	if !waitTCP(hostPort, 60*time.Second) {
		return fmt.Errorf("database at %s did not become ready", hostPort)
	}
	return nil
}

// dbTarget returns the host:port a DATABASE_URL connects to and whether that
// host is loopback (i.e. a candidate for the local docker compose database).
func dbTarget(dbURL string) (hostPort string, local bool) {
	u, err := url.Parse(dbURL)
	if err != nil {
		return "", false
	}
	host := u.Hostname()
	if host == "" {
		return "", false
	}
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	hostPort = net.JoinHostPort(host, port)
	local = host == "localhost" || host == "127.0.0.1" || host == "::1"
	return hostPort, local
}

// locateComposeDir finds the directory holding docker-compose.yml (or
// compose.yml): the current working directory first, then the directory the
// kenosm binary lives in, so an installed binary still manages its repo's DB.
func locateComposeDir() string {
	composeNames := []string{"docker-compose.yml", "compose.yml"}
	for _, name := range composeNames {
		if _, err := os.Stat(name); err == nil {
			if wd, err := os.Getwd(); err == nil {
				return wd
			}
		}
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for _, name := range composeNames {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				return dir
			}
		}
	}
	return ""
}

func tcpReachable(addr string, d time.Duration) bool {
	conn, err := net.DialTimeout("tcp", addr, d)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func waitTCP(addr string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if tcpReachable(addr, 2*time.Second) {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// appCommand opens the web UI as a real desktop application. It makes sure a
// server is running (starting one detached if needed), then renders the UI in
// a NATIVE window: an embedded WebKitGTK webview compiled straight into the
// kenosm binary (no external browser). Falls back to a chromeless chromium
// window, then to the default browser, when the native webview is unavailable.
func appCommand() {
	ensureEnv() // auto-generate missing secrets so there's no manual setup

	addr := localAddr(config.Lookup("ADDR", "127.0.0.1:8080"))
	key := config.Lookup("KENOSM_API_KEY", "")

	if err := ensureDatabase(); err != nil {
		fmt.Fprintf(os.Stderr, "%swarning: %v%s\n", cErr, err, cReset)
	}

	if !waitHealth(addr, key, 2*time.Second) {
		fmt.Printf("%sno server at %s — starting one…%s\n", cDim, addr, cReset)
		cmd := exec.Command(os.Args[0], "serve")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "%scannot start server: %v%s\n", cErr, err, cReset)
			os.Exit(1)
		}
		if !waitHealth(addr, key, 30*time.Second) {
			fmt.Fprintf(os.Stderr, "%sserver did not become healthy%s\n", cErr, cReset)
			os.Exit(1)
		}
	}

	url := "http://" + addr + "/"
	if err := runNativeWindow(url, key); err == nil {
		return
	} else {
		fmt.Printf("%snative window unavailable (%v) — falling back to chromium%s\n", cDim, err, cReset)
	}
	if p, err := exec.LookPath("chromium"); err == nil {
		home, _ := os.UserHomeDir()
		profile := filepath.Join(home, ".kenosm", "app-profile")
		c := exec.Command(p, "--app="+url, "--user-data-dir="+profile, "--class=kenosm")
		if err := c.Start(); err == nil {
			return
		}
	}
	if p, err := exec.LookPath("xdg-open"); err == nil {
		_ = exec.Command(p, url).Start()
		return
	}
	fmt.Printf("open %s in your browser\n", url)
}

// runNativeWindow renders the web UI in an embedded WebKitGTK window. It must
// be called from the main goroutine (the webview runs on the locked OS thread)
// and blocks until the window closes. The API key from .env is pre-seeded into
// the webview's storage so the first launch doesn't ask for it.
func runNativeWindow(url, apiKey string) (err error) {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return fmt.Errorf("no display server (headless session)")
	}
	defer func() {
		if r := recover(); r != nil {
			// A failed native webview (missing libs, bad environment) must not
			// take the CLI down: the caller falls back to chromium.
			err = fmt.Errorf("webview panic: %v", r)
		}
	}()
	w := webview.New(true) // devtools enabled: the UI is the app's whole surface
	defer w.Destroy()
	w.SetTitle("kenosm — agent swarm")
	w.SetSize(1440, 900, webview.HintNone)
	if apiKey != "" {
		// json.Marshal yields a valid JS string literal, so any character in
		// the key survives injection unchanged.
		if jsKey, mErr := json.Marshal(apiKey); mErr == nil {
			w.Init("localStorage.setItem('kenosm_key', " + string(jsKey) + ");")
		}
	}
	w.Navigate(url)
	w.Run()
	return nil
}

// setupCommand is the interactive first-run wizard: dependency checks, .env
// generation and the database container.
func setupCommand() {
	fmt.Println("kenosm setup — first-run wizard")
	fmt.Println()

	// Dependency checks.
	checks := []struct {
		name string
		path string
	}{
		{"docker", "docker"},
		{"chromium (browser)", "chromium"},
		{"xdotool (desktop, optional)", "xdotool"},
	}
	for _, c := range checks {
		if p, err := exec.LookPath(c.path); err == nil {
			fmt.Printf("  ✓ %-28s %s\n", c.name, p)
		} else {
			fmt.Printf("  ✗ %-28s not found\n", c.name)
		}
	}
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)
	ask := func(prompt, def string) string {
		if def != "" {
			fmt.Printf("%s [%s]: ", prompt, def)
		} else {
			fmt.Printf("%s: ", prompt)
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			return def
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return def
		}
		return line
	}

	// Database.
	dbURL := config.Lookup("DATABASE_URL", "")
	if dbURL == "" {
		dbURL = "postgres://kenosm:kenosm@localhost:5432/kenosm?sslmode=disable"
	}
	dbURL = ask("PostgreSQL URL", dbURL)

	// Secrets.
	secret := config.Lookup("SECRET_KEY", "")
	if secret == "" {
		secret = randomHex(32)
	}
	secret = ask("SECRET_KEY (generated)", secret)

	apiKey := config.Lookup("KENOSM_API_KEY", "")
	if apiKey == "" {
		apiKey = randomHex(24)
	}
	apiKey = ask("KENOSM_API_KEY (generated)", apiKey)

	addr := ask("Listen address", "127.0.0.1:8080")

	// Write .env (preserve unknown lines).
	env := map[string]string{
		"DATABASE_URL": dbURL,
		"SECRET_KEY":   secret,
		"KENOSM_API_KEY": apiKey,
		"ADDR":         addr,
	}
	existing := map[string]string{}
	if data, err := os.ReadFile(".env"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if eq := strings.IndexByte(line, '='); eq > 0 {
				k := strings.TrimSpace(line[:eq])
				if _, managed := env[k]; !managed {
					existing[k] = line
				}
			}
		}
	}
	var sb strings.Builder
	for k, v := range env {
		sb.WriteString(k + "=" + v + "\n")
	}
	for _, line := range existing {
		sb.WriteString(line + "\n")
	}
	if err := os.WriteFile(".env", []byte(sb.String()), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "cannot write .env: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("  ✓ .env written")

	// Database container.
	if _, err := exec.LookPath("docker"); err == nil {
		if strings.EqualFold(ask("Start the PostgreSQL container now? (y/n)", "y"), "y") {
			cmd := exec.Command("docker", "compose", "up", "-d", "db")
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			if err := cmd.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "docker compose failed: %v\n", err)
			} else {
				fmt.Println("  ✓ database container started")
			}
		}
	}

	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  kenosm serve        start the server")
	fmt.Println("  open http://" + addr + "  (web UI; add your first agent there)")
	fmt.Println("  kenosm chat --serve  or chat from the terminal right away")
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		fmt.Fprintln(os.Stderr, "cannot read secure random bytes:", err)
		os.Exit(1)
	}
	const hex = "0123456789abcdef"
	out := make([]byte, 0, n*2)
	for _, v := range b {
		out = append(out, hex[v>>4], hex[v&0x0f])
	}
	return string(out)
}
