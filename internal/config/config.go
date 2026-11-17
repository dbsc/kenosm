package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"kenosm/internal/pricing"
)

// Config holds runtime configuration, loaded from environment variables.
type Config struct {
	Addr              string
	DatabaseURL       string
	SecretKey         string
	APIKey            string
	GeminiModel       string
	OrchestratorModel string
	AvailableModels   []string
	DisabledModules   []string
	WorkspaceDir      string
	CommandTimeout    time.Duration
	ShutdownTimeout   time.Duration
	MaxConcurrentRuns int
	LogRetentionDays  int
	Prices            pricing.Table
}

// Load reads configuration from the environment, applying development defaults
// so the service runs out of the box.
func Load() (*Config, error) {
	cfg := &Config{
		Addr:              getenv("ADDR", ":8080"),
		DatabaseURL:       getenv("DATABASE_URL", "postgres://kenosm:kenosm@localhost:5432/kenosm?sslmode=disable"),
		SecretKey:         os.Getenv("SECRET_KEY"),
		APIKey:            os.Getenv("KENOSM_API_KEY"),
		GeminiModel:       getenv("GEMINI_MODEL", "gemini-3.8-flash"),
		OrchestratorModel: getenv("ORCHESTRATOR_MODEL", "gemini-3.8-flash"),
		AvailableModels:   splitCSV(getenv("AVAILABLE_MODELS", "gemini-3.8-flash,gemini-3.7-flash,gemini-3.6-flash,gemini-3.5-flash,gemini-3.5-flash-lite,gemini-flash-latest")),
		DisabledModules:   splitCSV(getenv("DISABLED_MODULES", "")),
		WorkspaceDir:      getenv("WORKSPACE_DIR", "workspace"),
		CommandTimeout:    durationFromEnv("COMMAND_TIMEOUT", 30*time.Second),
		ShutdownTimeout:   durationFromEnv("SHUTDOWN_TIMEOUT", 30*time.Second),
		MaxConcurrentRuns: intFromEnv("MAX_CONCURRENT_RUNS", 1),
		LogRetentionDays:  intFromEnv("LOG_RETENTION_DAYS", 30),
		Prices:            pricing.DefaultTable(),
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
	}

	return cfg, nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
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
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
