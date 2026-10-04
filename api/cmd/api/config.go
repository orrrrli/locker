package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type config struct {
	Port        int
	DatabaseURL string
}

// loadConfig reads the API configuration through getenv (os.Getenv in production).
// It reports every missing variable at once instead of failing on the first one.
func loadConfig(getenv func(string) string) (config, error) {
	var cfg config
	var missing []string
	var errs []error

	port := getenv("API_PORT")
	if port == "" {
		missing = append(missing, "API_PORT")
	} else if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		errs = append(errs, fmt.Errorf("API_PORT must be a port number between 1 and 65535, got %q", port))
	} else {
		cfg.Port = p
	}

	cfg.DatabaseURL = getenv("DATABASE_URL")
	if cfg.DatabaseURL == "" {
		missing = append(missing, "DATABASE_URL")
	}

	if len(missing) > 0 {
		errs = append([]error{fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))}, errs...)
	}
	return cfg, errors.Join(errs...)
}
