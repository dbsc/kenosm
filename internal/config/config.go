package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"kenosm/internal/pricing"
)

// Config holds runtime configuration, loaded from environment variables.
type Config struct {
	Addr               string
	DatabaseURL        string
	SecretKey          string
	APIKey             string
	GeminiModel        string
	OrchestratorModel  string
	AvailableModels    []string
	DisabledModules    []string
	WorkspaceDir       string
	AssetsDir          string
	CommandTimeout     time.Duration
	ShutdownTimeout    time.Duration
	MaxConcurrentRuns  int
	PolishMaxRounds    int
	GoalMaxRounds      int
	AgentMaxIterations int
	STTCmd             string // optional speech-to-text command for voice input (see stt/transcribe.py)
	VoiceCaptureArgs   string // optional ffmpeg prefix for server-side mic capture (default "ffmpeg -f pulse -i default")
	LogRetentionDays   int
	AllowPrivateUpstream bool
	Prices             pricing.Table
}

// Load reads configuration from the environment, applying development defaults
// so the service runs out of the box. A local ".env" file (if present) is loaded
// first so `cp .env.example .env` works without exporting the variables.
func Load() (*Config, error) {
	loadDotEnv()

	cfg := &Config{
		Addr:               getenv("ADDR", ":8080"),
		DatabaseURL:        getenv("DATABASE_URL", "postgres://kenosm:kenosm@localhost:5432/kenosm?sslmode=disable"),
		SecretKey:          os.Getenv("SECRET_KEY"),
		APIKey:             os.Getenv("KENOSM_API_KEY"),
		GeminiModel:        getenv("GEMINI_MODEL", "gemini-3.8-flash"),
		OrchestratorModel:  getenv("ORCHESTRATOR_MODEL", "gemini-3.8-flash"),
		AvailableModels:    splitCSV(getenv("AVAILABLE_MODELS", "gemini-3.8-flash,gemini-3.7-flash,gemini-3.6-flash,gemini-3.5-flash,gemini-3.5-flash-lite,gemini-flash-latest")),
		DisabledModules:    splitCSV(getenv("DISABLED_MODULES", "")),
		WorkspaceDir:       getenv("WORKSPACE_DIR", "workspace"),
		AssetsDir:          getenv("ASSETS_DIR", "assets"),
		CommandTimeout:     durationFromEnv("COMMAND_TIMEOUT", 30*time.Second),
		ShutdownTimeout:    durationFromEnv("SHUTDOWN_TIMEOUT", 30*time.Second),
		MaxConcurrentRuns:  intFromEnv("MAX_CONCURRENT_RUNS", 1),
		PolishMaxRounds:    intFromEnv("POLISH_MAX_ROUNDS", 5),
		GoalMaxRounds:      intFromEnv("GOAL_MAX_ROUNDS", 2),
		AgentMaxIterations: intFromEnv("AGENT_MAX_ITERATIONS", 50),
		STTCmd:             getenv("STT_CMD", ""),
		VoiceCaptureArgs:   getenv("VOICE_CAPTURE_ARGS", ""),
		LogRetentionDays:   intFromEnv("LOG_RETENTION_DAYS", 30),
		AllowPrivateUpstream: isTrue("ALLOW_PRIVATE_UPSTREAM"),
		Prices:             pricing.DefaultTable(),
	}

	// MODEL_PRICES overrides the built-in price table with a JSON map, e.g.
	// {"gemini-3.8-flash":{"input":0.15,"output":0.6}} (USD per 1M tokens).
	if s := os.Getenv("MODEL_PRICES"); s != "" {
		if ov, err := pricing.ParseOverrides(s); err == nil {
			for k, v := range ov {
				cfg.Prices[k] = v
			}
		} else {
			fmt.Fprintf(os.Stderr, "WARNING: MODEL_PRICES is not valid JSON; ignoring: %v\n", err)
		}
	}

	if cfg.SecretKey == "" {
		cfg.SecretKey = "dev-insecure-secret-key-change-me"
		fmt.Fprintln(os.Stderr, "WARNING: SECRET_KEY not set; using an insecure development key. Set SECRET_KEY in production.")
	}

	if cfg.APIKey == "" {
		fmt.Fprintln(os.Stderr, "WARNING: KENOSM_API_KEY not set; API and proxy endpoints are unauthenticated. Set KENOSM_API_KEY to require a bearer key.")
		// Without authentication, never expose the server beyond loopback: an
		// open :8080 with shell/browser/desktop tools is remote code execution
		// for anyone on the network. A wildcard/empty-host ADDR is silently
		// narrowed to 127.0.0.1 so the app stays usable out of the box.
		if host, _, err := net.SplitHostPort(cfg.Addr); err == nil {
			if host == "" || host == "0.0.0.0" || host == "::" {
				cfg.Addr = "127.0.0.1:" + portOf(cfg.Addr)
				fmt.Fprintln(os.Stderr, "WARNING: no KENOSM_API_KEY — forcing ADDR to "+cfg.Addr)
			}
		}
	}

	return cfg, nil
}

// portOf returns the port part of "host:port" (defaulting to 8080).
func portOf(addr string) string {
	if _, port, err := net.SplitHostPort(addr); err == nil && port != "" {
		return port
	}
	return "8080"
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// readDotEnv parses KEY=VALUE lines from ".env" in the working directory into a
// map, without touching the process environment. It skips blank lines and "#"
// comments, understands an optional "export " prefix and surrounding quotes, and
// strips trailing inline comments. Returns nil when there is no .env file.
func readDotEnv() map[string]string {
	data, err := os.ReadFile(".env")
	if err != nil {
		return nil
	}
	m := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		eq := -1
		inSingle, inDouble := false, false
		for i := 0; i < len(line); i++ {
			switch line[i] {
			case '\'':
				if !inDouble {
					inSingle = !inSingle
				}
			case '"':
				if !inSingle {
					inDouble = !inDouble
				}
			case '=':
				if !inSingle && !inDouble {
					eq = i
					i = len(line)
				}
			}
		}
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := stripInlineComment(strings.TrimSpace(line[eq+1:]))
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		}
		if key != "" {
			m[key] = val
		}
	}
	return m
}

// loadDotEnv exports ".env" values into the process environment when the key is
// not already set, so a real shell environment always takes precedence.
func loadDotEnv() {
	for k, v := range readDotEnv() {
		if _, ok := os.LookupEnv(k); !ok {
			_ = os.Setenv(k, v)
		}
	}
}

// Lookup returns a config value: the process environment wins, then a local
// ".env" file, then the default. It is the single .env parser shared by the
// server (via loadDotEnv) and the CLI, which reads ADDR/KENOSM_API_KEY without
// exporting the whole file.
func Lookup(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	if m := readDotEnv(); m != nil {
		if v, ok := m[key]; ok && v != "" {
			return v
		}
	}
	return def
}

// stripInlineComment cuts a trailing "# comment" from a value, leaving
// hash marks inside quotes (and without a preceding space) untouched.
func stripInlineComment(val string) string {
	inSingle, inDouble := false, false
	for i := 0; i < len(val); i++ {
		switch val[i] {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble && i > 0 && (val[i-1] == ' ' || val[i-1] == '	') {
				return strings.TrimSpace(val[:i])
			}
		}
	}
	return val
}

func splitCSV(s string) []string {
	var out []string
	for _, m := range strings.Split(s, ",") {
		m = strings.TrimSpace(m)
		if m != "" {
			out = append(out, m)
		}
	}
	return out
}

func durationFromEnv(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func intFromEnv(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		// 0 is a valid value (e.g. MAX_CONCURRENT_RUNS=0 disables runs);
		// only negative or unparsable input falls back to the default.
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return def
}

// isTrue reports whether a boolean-ish env var is set to a truthy value.
func isTrue(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
