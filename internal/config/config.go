package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Config holds runtime configuration, loaded from environment variables.
type Config struct {
	Addr              string
	DatabaseURL       string
	SecretKey         string
	GeminiModel       string
	OrchestratorModel string
	AvailableModels   []string
	WorkspaceDir      string
	CommandTimeout    time.Duration
	ShutdownTimeout   time.Duration
}

// Load reads configuration from the environment, applying development defaults
// so the service runs out of the box.
func Load() (*Config, error) {
	cfg := &Config{
		Addr:              getenv("ADDR", ":8080"),
		DatabaseURL:       getenv("DATABASE_URL", "postgres://kenosm:kenosm@localhost:5432/kenosm?sslmode=disable"),
		SecretKey:         os.Getenv("SECRET_KEY"),
		GeminiModel:       getenv("GEMINI_MODEL", "gemini-3.5-flash"),
		OrchestratorModel: getenv("ORCHESTRATOR_MODEL", "gemini-3.5-flash"),
		AvailableModels:   splitModels(getenv("AVAILABLE_MODELS", "gemini-3.8-flash,gemini-3.7-flash,gemini-3.6-flash,gemini-3.5-flash,gemini-3.5-flash-lite,gemini-flash-latest")),
		WorkspaceDir:      getenv("WORKSPACE_DIR", "workspace"),
		CommandTimeout:    durationFromEnv("COMMAND_TIMEOUT", 30*time.Second),
		ShutdownTimeout:   30 * time.Second,
	}

	if cfg.SecretKey == "" {
		cfg.SecretKey = "dev-insecure-secret-key-change-me"
		fmt.Fprintln(os.Stderr, "WARNING: SECRET_KEY not set; using an insecure development key. Set SECRET_KEY in production.")
	}

	return cfg, nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func splitModels(s string) []string {
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
