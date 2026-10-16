package main

import (
	"maps"
	"strings"
	"testing"

	"github.com/orrrrli/locker/api/internal/infrastructure/apns"
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

	t.Run("apple", func(t *testing.T) {
		for _, tc := range []struct {
			team, bundle, appID string
			ok                  bool
			msg                 string // part of the error, when it fails
		}{
			{"", "", "", true, ""},
			{"ABCDE12345", "center.locker.app", "ABCDE12345.center.locker.app", true, ""},
			{"ABCDE12345", "", "", false, "set both"},
			{"", "center.locker.app", "", false, "set both"},
			{"abcde12345", "center.locker.app", "", false, ""},
			{"ABCDE1234", "center.locker.app", "", false, ""},
			{"ABCDE12345", "locker", "", false, ""},
			{"ABCDE12345", "center.locker.app/../x", "", false, ""},
		} {
			vars := map[string]string{"API_PORT": "8080", "DATABASE_URL": "x", "PUBLIC_BASE_URL": "https://x.test",
				"APPLE_TEAM_ID": tc.team, "APPLE_BUNDLE_ID": tc.bundle}
			cfg, err := loadConfig(env(vars))
			if (err == nil) != tc.ok || cfg.AppleAppID != tc.appID {
				t.Fatalf("%q %q: app id %q, err %v; want %q, ok %v", tc.team, tc.bundle, cfg.AppleAppID, err, tc.appID, tc.ok)
			}
			if tc.msg != "" && !strings.Contains(err.Error(), tc.msg) {
				t.Fatalf("%q %q: err %v, want it to say %q", tc.team, tc.bundle, err, tc.msg)
			}
		}
	})

	t.Run("testflight url", func(t *testing.T) {
		base := map[string]string{"API_PORT": "8080", "DATABASE_URL": "x", "PUBLIC_BASE_URL": "https://x.test"}
		if cfg, err := loadConfig(env(base)); err != nil || cfg.DownloadURL != "" {
			t.Fatalf("unset: %q, %v", cfg.DownloadURL, err)
		}
		base["TESTFLIGHT_URL"] = "https://testflight.apple.com/join/AbC"
		if _, err := loadConfig(env(base)); err == nil || !strings.Contains(err.Error(), "APPLE_TEAM_ID") {
			t.Fatalf("without the Apple pair: err %v, want it to name APPLE_TEAM_ID", err)
		}
		base["APPLE_TEAM_ID"], base["APPLE_BUNDLE_ID"] = "ABCDE12345", "center.locker.app"
		for url, ok := range map[string]bool{
			"https://testflight.apple.com/join/AbC": true,
			"http://testflight.apple.com/join/AbC":  false,
			"javascript:alert(1)":                   false,
			"testflight.apple.com/join/AbC":         false,
		} {
			base["TESTFLIGHT_URL"] = url
			cfg, err := loadConfig(env(base))
			if (err == nil) != ok || (ok && cfg.DownloadURL != url) {
				t.Fatalf("%q: url %q, err %v; want ok %v", url, cfg.DownloadURL, err, ok)
			}
		}
	})

	t.Run("apns", func(t *testing.T) {
		apple := map[string]string{"APPLE_TEAM_ID": "ABCDE12345", "APPLE_BUNDLE_ID": "center.locker.app"}
		for _, tc := range []struct {
			name, path, keyID, env string
			apple                  bool
			host                   string // empty when push stays off
			msg                    string // part of the error, when it fails
		}{
			{"unset", "", "", "", true, "", ""},
			{"production", "/run/secrets/apns/k.p8", "KEY1234567", "", true, apns.ProductionHost, ""},
			{"production named", "/run/secrets/apns/k.p8", "KEY1234567", "production", true, apns.ProductionHost, ""},
			{"development", "/run/secrets/apns/k.p8", "KEY1234567", "development", true, apns.SandboxHost, ""},
			{"only the path", "/k.p8", "", "", true, "", "set both"},
			{"only the id", "", "KEY1234567", "", true, "", "set both"},
			{"bad id", "/k.p8", "key", "", true, "", "APNS_KEY_ID"},
			{"bad environment", "/k.p8", "KEY1234567", "sandbox", true, "", "APNS_ENVIRONMENT"},
			{"no apple pair", "/k.p8", "KEY1234567", "", false, "", "APPLE_TEAM_ID"},
		} {
			vars := map[string]string{"API_PORT": "8080", "DATABASE_URL": "x", "PUBLIC_BASE_URL": "https://x.test",
				"APNS_KEY_PATH": tc.path, "APNS_KEY_ID": tc.keyID, "APNS_ENVIRONMENT": tc.env}
			if tc.apple {
				maps.Copy(vars, apple)
			}
			cfg, err := loadConfig(env(vars))
			if (err == nil) != (tc.msg == "") || cfg.APNS.Host != tc.host {
				t.Fatalf("%s: host %q, err %v; want host %q, error %q", tc.name, cfg.APNS.Host, err, tc.host, tc.msg)
			}
			if tc.msg != "" && !strings.Contains(err.Error(), tc.msg) {
				t.Fatalf("%s: err %v, want it to say %q", tc.name, err, tc.msg)
			}
			if tc.host != "" && (cfg.APNSKeyPath != tc.path || cfg.APNS.TeamID != "ABCDE12345" ||
				cfg.APNS.KeyID != tc.keyID || cfg.APNS.Topic != "center.locker.app") {
				t.Fatalf("%s: unexpected push config %+v, key path %q", tc.name, cfg.APNS, cfg.APNSKeyPath)
			}
		}
	})

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
