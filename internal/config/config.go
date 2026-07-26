// Package config loads the 12-factor environment configuration.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	DatabaseURL       string // required
	HistoryBaseURL    string // R2Z2 daily dumps + totals
	LiveBaseURL       string // R2Z2 ephemeral live feed (RedisQ replacement)
	RetentionDays     int    // bounds both pruning and backfill depth
	UserAgent         string // required (zKillboard etiquette)
	BackfillOnStart   bool
	ReconcileInterval time.Duration // how often the scheduler polls for new/grown dumps
	PruneInterval     time.Duration // how often the scheduler enforces retention
	HTTPAddr          string
}

// Load reads the environment, applies defaults, and validates required fields.
func Load() (Config, error) {
	c := Config{
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		HistoryBaseURL: env("HISTORY_BASE_URL", "https://r2z2.zkillboard.com/history"),
		LiveBaseURL:    env("LIVE_BASE_URL", "https://r2z2.zkillboard.com/ephemeral"),
		UserAgent:      os.Getenv("USER_AGENT"),
		HTTPAddr:       env("HTTP_ADDR", ":8080"),
	}

	var err error
	if c.RetentionDays, err = envInt("RETENTION_DAYS", 365); err != nil {
		return Config{}, err
	}
	if c.ReconcileInterval, err = envDuration("RECONCILE_INTERVAL", time.Hour); err != nil {
		return Config{}, err
	}
	if c.PruneInterval, err = envDuration("PRUNE_INTERVAL", 24*time.Hour); err != nil {
		return Config{}, err
	}
	c.BackfillOnStart = envBool("BACKFILL_ON_START", false)

	// Validate at the boundary: fail loudly on missing required config.
	switch {
	case c.DatabaseURL == "":
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	case c.UserAgent == "":
		return Config{}, fmt.Errorf("USER_AGENT is required (zKillboard etiquette)")
	case c.RetentionDays < 1:
		return Config{}, fmt.Errorf("RETENTION_DAYS must be >= 1, got %d", c.RetentionDays)
	case c.ReconcileInterval <= 0:
		return Config{}, fmt.Errorf("RECONCILE_INTERVAL must be > 0, got %s", c.ReconcileInterval)
	case c.PruneInterval <= 0:
		return Config{}, fmt.Errorf("PRUNE_INTERVAL must be > 0, got %s", c.PruneInterval)
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}
