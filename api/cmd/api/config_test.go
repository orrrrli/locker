package main

import (
	"strings"
	"testing"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestLoadConfig(t *testing.T) {
	valid := map[string]string{"API_PORT": "8080", "DATABASE_URL": "postgres://localhost/locker"}

	t.Run("valid", func(t *testing.T) {
		cfg, err := loadConfig(env(valid))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Port != 8080 || cfg.DatabaseURL != valid["DATABASE_URL"] {
			t.Fatalf("unexpected config: %+v", cfg)
		}
	})

	tests := []struct {
		name string
		vars map[string]string
		want []string
	}{
		{"one missing", map[string]string{"API_PORT": "8080"}, []string{"DATABASE_URL"}},
		{"all missing", map[string]string{}, []string{"API_PORT", "DATABASE_URL"}},
		{"invalid port", map[string]string{"API_PORT": "99999", "DATABASE_URL": "x"}, []string{"API_PORT", "99999"}},
		{"non-numeric port", map[string]string{"API_PORT": "abc", "DATABASE_URL": "x"}, []string{"API_PORT", "abc"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadConfig(env(tt.vars))
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not mention %q", err, w)
				}
			}
		})
	}
}
