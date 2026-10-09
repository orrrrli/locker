package main

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

type config struct {
	Port        int
	DatabaseURL string
	// PublicBaseURL is where clients reach the API, like
	// https://api.locker.center. Invite links are built from it.
	PublicBaseURL string
	// AppleAppID is "<team id>.<bundle id>", the app Universal Links open.
	// Empty until the Apple Developer account exists.
	AppleAppID string
	// DownloadURL is the public TestFlight link the invite landing points to.
	// Optional: without it the page says the app is not out yet.
	DownloadURL string
}

var (
	appleTeamIDPattern   = regexp.MustCompile(`^[A-Z0-9]{10}$`)
	appleBundleIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$`)
)

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

	if base := getenv("PUBLIC_BASE_URL"); base == "" {
		missing = append(missing, "PUBLIC_BASE_URL")
	} else if u, err := url.Parse(base); err != nil || (u.Scheme != "https" && u.Scheme != "http") ||
		u.Host == "" || strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		errs = append(errs, fmt.Errorf("PUBLIC_BASE_URL must be an http(s) origin like https://api.locker.center, got %q", base))
	} else {
		// Only the origin: a stray "?" or "#" in the variable would otherwise
		// put the invite token in a query string or fragment.
		cfg.PublicBaseURL = u.Scheme + "://" + u.Host
	}

	// Both or neither: one without the other is a half-done setup that would
	// otherwise ship without anyone noticing.
	team, bundle := getenv("APPLE_TEAM_ID"), getenv("APPLE_BUNDLE_ID")
	switch {
	case team == "" && bundle == "":
		// Not configured yet: apple-app-site-association is a 404.
	case team == "" || bundle == "":
		errs = append(errs, errors.New("set both APPLE_TEAM_ID and APPLE_BUNDLE_ID, or neither"))
	case !appleTeamIDPattern.MatchString(team):
		errs = append(errs, fmt.Errorf("APPLE_TEAM_ID must be 10 uppercase letters or digits, got %q", team))
	case !appleBundleIDPattern.MatchString(bundle):
		errs = append(errs, fmt.Errorf("APPLE_BUNDLE_ID must look like center.locker.app, got %q", bundle))
	default:
		cfg.AppleAppID = team + "." + bundle
	}

	// The download button tells people to reopen the link once the app is
	// installed; without the Apple pair there is no apple-app-site-association,
	// iOS keeps opening the link in the browser, and the page would loop.
	if dl := getenv("TESTFLIGHT_URL"); dl != "" {
		if u, err := url.Parse(dl); err != nil || u.Scheme != "https" || u.Host == "" {
			errs = append(errs, fmt.Errorf("TESTFLIGHT_URL must be an https URL, got %q", dl))
		} else if team == "" || bundle == "" {
			errs = append(errs, errors.New("TESTFLIGHT_URL needs APPLE_TEAM_ID and APPLE_BUNDLE_ID"))
		} else {
			cfg.DownloadURL = dl
		}
	}

	if len(missing) > 0 {
		errs = append([]error{fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))}, errs...)
	}
	return cfg, errors.Join(errs...)
}
