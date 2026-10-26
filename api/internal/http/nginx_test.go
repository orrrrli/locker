package http

import (
	"os"
	"strings"
	"testing"
)

// requireNoLogLocation fails unless the nginx block for the exact location
// header keeps the request line out of every log. access_log off covers the
// access log; error_log crit keeps the request line out of the error log
// while the API is down. A proxy_set_header here would drop the server-level
// X-Real-IP and let clients pick their rate-limit IP.
func requireNoLogLocation(t *testing.T, location string) {
	t.Helper()
	conf, err := os.ReadFile("../../../deploy/nginx/api.locker.center.conf")
	if err != nil {
		t.Fatal(err)
	}
	_, block, found := strings.Cut(string(conf), "location "+location+" {")
	block, _, _ = strings.Cut(block, "}")
	if !found || !strings.Contains(block, "access_log off;") || !strings.Contains(block, "error_log /var/log/nginx/error.log crit;") ||
		strings.Contains(block, "proxy_set_header") {
		t.Fatalf("nginx: location %s must set access_log off and error_log crit, and no proxy_set_header; got %q", location, block)
	}
}

// Every location whose path or query carries a secret (token, device token,
// user location) must stay out of the nginx logs. /r/, /v/, /venues, /feed and
// /p/ are also checked next to their handler tests.
func TestNginxNoLogLocations(t *testing.T) {
	for _, location := range []string{"/i/", "/v/", "/r/", "/me/device-tokens/", "= /venues", "= /feed", "/p/", "/posts/shared/"} {
		t.Run(location, func(t *testing.T) { requireNoLogLocation(t, location) })
	}
}
