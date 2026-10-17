package main

import (
	"maps"
	"strings"
	"testing"

	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/apns"
	"github.com/orrrrli/locker/api/internal/infrastructure/resend"
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

	t.Run("email", func(t *testing.T) {
		for _, tc := range []struct {
			name, key, from string
			msg             string // part of the error, when it fails
		}{
			{"unset", "", "", ""},
			{"set", "re_abc123", "Locker <no-reply@locker.center>", ""},
			{"bare address", "re_abc123", "no-reply@locker.center", ""},
			{"only the key", "re_abc123", "", "set both"},
			{"only the sender", "", "no-reply@locker.center", "set both"},
			{"not a resend key", "sk_abc123", "no-reply@locker.center", "RESEND_API_KEY"},
			{"bad sender", "re_abc123", "locker.center", "EMAIL_FROM"},
		} {
			vars := map[string]string{"API_PORT": "8080", "DATABASE_URL": "x", "PUBLIC_BASE_URL": "https://x.test",
				"RESEND_API_KEY": tc.key, "EMAIL_FROM": tc.from}
			cfg, err := loadConfig(env(vars))
			if (err == nil) != (tc.msg == "") {
				t.Fatalf("%s: err %v, want error %q", tc.name, err, tc.msg)
			}
			if tc.msg != "" && !strings.Contains(err.Error(), tc.msg) {
				t.Fatalf("%s: err %v, want it to say %q", tc.name, err, tc.msg)
			}
			if tc.msg != "" && strings.Contains(err.Error(), tc.key) && tc.key != "" {
				t.Fatalf("%s: err %v leaks the API key", tc.name, err)
			}
			want := resend.Config{}
			if tc.msg == "" && tc.key != "" {
				want = resend.Config{Host: resend.Host, APIKey: tc.key, From: tc.from}
			}
			if cfg.Email != want {
				t.Fatalf("%s: email config %+v, want %+v", tc.name, cfg.Email, want)
			}
		}
	})

	t.Run("card fees", func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			vars      map[string]string
			rates     domain.CardFeeRates
			lockerFee int64
			msg       string // part of the error, when it fails
		}{
			{"defaults", nil, domain.CardFeeRates{PercentBps: 349, FixedCents: 400, TaxBps: 1600}, 0, ""},
			{"override", map[string]string{"MP_FEE_PERCENT_BPS": "399", "MP_FEE_FIXED_CENTS": "300",
				"MP_FEE_TAX_BPS": "800", "LOCKER_FEE_FIXED_CENTS": "232"},
				domain.CardFeeRates{PercentBps: 399, FixedCents: 300, TaxBps: 800}, 232, ""},
			{"zero is allowed", map[string]string{"MP_FEE_TAX_BPS": "0"},
				domain.CardFeeRates{PercentBps: 349, FixedCents: 400}, 0, ""},
			{"not a number", map[string]string{"MP_FEE_PERCENT_BPS": "3.49"}, domain.CardFeeRates{}, 0, "MP_FEE_PERCENT_BPS"},
			{"negative", map[string]string{"LOCKER_FEE_FIXED_CENTS": "-1"}, domain.CardFeeRates{}, 0, "LOCKER_FEE_FIXED_CENTS"},
			{"too large", map[string]string{"MP_FEE_FIXED_CENTS": "9999999999"}, domain.CardFeeRates{}, 0, "MP_FEE_FIXED_CENTS"},
			{"rate reaches 100%", map[string]string{"MP_FEE_PERCENT_BPS": "5000", "MP_FEE_TAX_BPS": "10000"},
				domain.CardFeeRates{}, 0, "below 100%"},
			{"tax above 100%", map[string]string{"MP_FEE_PERCENT_BPS": "0", "MP_FEE_TAX_BPS": "10001"},
				domain.CardFeeRates{}, 0, "at most 100%"},
		} {
			vars := map[string]string{"API_PORT": "8080", "DATABASE_URL": "x", "PUBLIC_BASE_URL": "https://x.test"}
			maps.Copy(vars, tc.vars)
			cfg, err := loadConfig(env(vars))
			if (err == nil) != (tc.msg == "") {
				t.Fatalf("%s: err %v, want error %q", tc.name, err, tc.msg)
			}
			if tc.msg != "" {
				if !strings.Contains(err.Error(), tc.msg) {
					t.Fatalf("%s: err %v, want it to say %q", tc.name, err, tc.msg)
				}
				continue
			}
			if cfg.CardFees != tc.rates || cfg.LockerFeeCents != tc.lockerFee {
				t.Fatalf("%s: rates %+v, fee %d; want %+v, %d", tc.name, cfg.CardFees, cfg.LockerFeeCents, tc.rates, tc.lockerFee)
			}
		}

		// A config change alone moves the total (R26.5): no code change.
		for _, tc := range []struct {
			vars  map[string]string
			total int64
		}{
			{nil, 2568},
			{map[string]string{"LOCKER_FEE_FIXED_CENTS": "232"}, 2810},
			{map[string]string{"MP_FEE_PERCENT_BPS": "399", "MP_FEE_FIXED_CENTS": "300", "MP_FEE_TAX_BPS": "800",
				"LOCKER_FEE_FIXED_CENTS": "232"}, 2672},
		} {
			vars := map[string]string{"API_PORT": "8080", "DATABASE_URL": "x", "PUBLIC_BASE_URL": "https://x.test"}
			maps.Copy(vars, tc.vars)
			cfg, err := loadConfig(env(vars))
			if err != nil {
				t.Fatal(err)
			}
			if total, _ := domain.CardTotal(2000, cfg.LockerFeeCents, cfg.CardFees); total != tc.total {
				t.Fatalf("%v: total for 2000 = %d, want %d", tc.vars, total, tc.total)
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
