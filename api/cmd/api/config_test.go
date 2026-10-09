package main

import (
	"strings"
	"testing"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestLoadConfig(t *testing.T) {
	valid := map[string]string{
		"API_PORT": "8080", "DATABASE_URL": "postgres://localhost/locker", "PUBLIC_BASE_URL": "https://api.locker.center/",
	}

	t.Run("valid", func(t *testing.T) {
		cfg, err := loadConfig(env(valid))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Port != 8080 || cfg.DatabaseURL != valid["DATABASE_URL"] || cfg.PublicBaseURL != "https://api.locker.center" {
			t.Fatalf("unexpected config: %+v", cfg)
		}
	})

	// Only the origin is kept: a stray "/", "?" or "#" never reaches the links.
	for _, base := range []string{"https://api.locker.center/", "https://api.locker.center?", "https://api.locker.center#"} {
		vars := map[string]string{"API_PORT": "8080", "DATABASE_URL": "x", "PUBLIC_BASE_URL": base}
		cfg, err := loadConfig(env(vars))
		if err != nil || cfg.PublicBaseURL != "https://api.locker.center" {
			t.Fatalf("%q: base = %q, err = %v; want https://api.locker.center", base, cfg.PublicBaseURL, err)
		}
	}

	tests := []struct {
		name string
		vars map[string]string
		want []string
	}{
		{"one missing", map[string]string{"API_PORT": "8080", "PUBLIC_BASE_URL": "https://x.test"}, []string{"DATABASE_URL"}},
		{"all missing", map[string]string{}, []string{"API_PORT", "DATABASE_URL", "PUBLIC_BASE_URL"}},
		{"invalid port", map[string]string{"API_PORT": "99999", "DATABASE_URL": "x", "PUBLIC_BASE_URL": "https://x.test"}, []string{"API_PORT", "99999"}},
		{"non-numeric port", map[string]string{"API_PORT": "abc", "DATABASE_URL": "x", "PUBLIC_BASE_URL": "https://x.test"}, []string{"API_PORT", "abc"}},
	}
	// Invite links are PUBLIC_BASE_URL + "/i/<token>": anything but a bare
	// http(s) origin would produce broken or misleading links.
	for _, bad := range []string{"api.locker.center", "ftp://api.locker.center", "https://", "https://api.locker.center/v1",
		"https://api.locker.center?x=1", "https://user@api.locker.center", "javascript:alert(1)"} {
		vars := map[string]string{"API_PORT": "8080", "DATABASE_URL": "x", "PUBLIC_BASE_URL": bad}
		tests = append(tests, struct {
			name string
			vars map[string]string
			want []string
		}{"bad base " + bad, vars, []string{"PUBLIC_BASE_URL"}})
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
