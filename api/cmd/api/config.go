package main

import (
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/apns"
	"github.com/orrrrli/locker/api/internal/infrastructure/r2"
	"github.com/orrrrli/locker/api/internal/infrastructure/resend"
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
	// APNS is the push setup, without its key; APNSKeyPath is the path of
	// the .p8 key. Empty until the APNs key exists: notices then stay in the
	// inbox without a push.
	APNS        apns.Config
	APNSKeyPath string
	// Email is the Resend setup for verification and password-reset mail.
	// Empty APIKey until the Resend account exists.
	Email resend.Config
	// CardFees are MP's card fee rates and LockerFeeCents the commission
	// Locker adds to each card payment: the inputs of domain.CardTotal. The
	// card quote endpoint (BE-FEES-6) is the first reader.
	CardFees       domain.CardFeeRates
	LockerFeeCents int64
	// Photos is the R2 bucket for post photos and its bucket-scoped key.
	// Empty until the bucket exists (OPS-FEED-1): photo uploads then answer
	// 503.
	Photos r2.Config
}

var (
	appleTeamIDPattern   = regexp.MustCompile(`^[A-Z0-9]{10}$`)
	appleBundleIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$`)
	apnsKeyIDPattern     = regexp.MustCompile(`^[A-Z0-9]{10}$`)
	r2AccountIDPattern   = regexp.MustCompile(`^[0-9a-f]{32}$`)
	r2BucketPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
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

	// Push needs the key, its id, and the Apple pair: the team signs the
	// provider token and the bundle is the APNs topic.
	// APNS_ENVIRONMENT picks Apple's server: production (the default) for
	// TestFlight and App Store builds, development for builds run from Xcode.
	keyPath, keyID, apnsEnv := getenv("APNS_KEY_PATH"), getenv("APNS_KEY_ID"), getenv("APNS_ENVIRONMENT")
	host := map[string]string{"": apns.ProductionHost, "production": apns.ProductionHost, "development": apns.SandboxHost}[apnsEnv]
	switch {
	case host == "":
		errs = append(errs, fmt.Errorf("APNS_ENVIRONMENT must be production or development, got %q", apnsEnv))
	case keyPath == "" && keyID == "":
		// Not configured yet: notices stay inbox only.
	case keyPath == "" || keyID == "":
		errs = append(errs, errors.New("set both APNS_KEY_PATH and APNS_KEY_ID, or neither"))
	case !apnsKeyIDPattern.MatchString(keyID):
		errs = append(errs, fmt.Errorf("APNS_KEY_ID must be 10 uppercase letters or digits, got %q", keyID))
	case cfg.AppleAppID == "":
		errs = append(errs, errors.New("APNS_KEY_PATH needs APPLE_TEAM_ID and APPLE_BUNDLE_ID"))
	default:
		cfg.APNSKeyPath = keyPath
		cfg.APNS = apns.Config{Host: host, TeamID: team, KeyID: keyID, Topic: bundle}
	}

	// Email needs the API key and a sender on a domain verified in Resend.
	// EMAIL_FROM may carry a display name: "Locker <no-reply@locker.center>".
	apiKey, from := getenv("RESEND_API_KEY"), getenv("EMAIL_FROM")
	switch {
	case apiKey == "" && from == "":
		// Not configured yet: no email is sent.
	case apiKey == "" || from == "":
		errs = append(errs, errors.New("set both RESEND_API_KEY and EMAIL_FROM, or neither"))
	case !strings.HasPrefix(apiKey, "re_"):
		// Not echoed: it is a secret.
		errs = append(errs, errors.New("RESEND_API_KEY must be a Resend API key starting with re_"))
	default:
		if _, err := mail.ParseAddress(from); err != nil {
			errs = append(errs, fmt.Errorf("EMAIL_FROM must be an address like Locker <no-reply@locker.center>, got %q", from))
		} else {
			cfg.Email = resend.Config{Host: resend.Host, APIKey: apiKey, From: from}
		}
	}

	// Card fees (R26.5): MP's published MX rates unless the env overrides
	// them, so a rate change needs no code change. The Locker commission
	// stays 0 until activation gate G2.
	optionalCount := func(name string, def int64) int64 {
		v := getenv(name)
		if v == "" {
			return def
		}
		// 32 bits keeps each product in the fee math inside int64.
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n < 0 {
			errs = append(errs, fmt.Errorf("%s must be a whole number of 0 or more, got %q", name, v))
			return def
		}
		return n
	}
	cfg.CardFees = domain.CardFeeRates{
		PercentBps: optionalCount("MP_FEE_PERCENT_BPS", 349),
		FixedCents: optionalCount("MP_FEE_FIXED_CENTS", 400),
		TaxBps:     optionalCount("MP_FEE_TAX_BPS", 1600),
	}
	cfg.LockerFeeCents = optionalCount("LOCKER_FEE_FIXED_CENTS", 0)
	if err := cfg.CardFees.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("MP_FEE_PERCENT_BPS, MP_FEE_FIXED_CENTS, MP_FEE_TAX_BPS: %w", err))
	}

	// Photos need all four or none: a partial set is a half-done setup.
	photos := r2.Config{
		AccountID:       getenv("R2_ACCOUNT_ID"),
		Bucket:          getenv("R2_PHOTOS_BUCKET"),
		AccessKeyID:     getenv("R2_PHOTOS_ACCESS_KEY_ID"),
		SecretAccessKey: getenv("R2_PHOTOS_SECRET_ACCESS_KEY"),
	}
	set := 0
	for _, v := range []string{photos.AccountID, photos.Bucket, photos.AccessKeyID, photos.SecretAccessKey} {
		if v != "" {
			set++
		}
	}
	switch {
	case set == 0:
		// Not configured yet: photo uploads answer 503.
	case set < 4:
		errs = append(errs, errors.New("set all of R2_ACCOUNT_ID, R2_PHOTOS_BUCKET, R2_PHOTOS_ACCESS_KEY_ID and R2_PHOTOS_SECRET_ACCESS_KEY, or none"))
	case !r2AccountIDPattern.MatchString(photos.AccountID):
		// R2 values are never echoed: a secret pasted into the wrong
		// variable would otherwise land in the deploy logs.
		errs = append(errs, fmt.Errorf("R2_ACCOUNT_ID must be 32 lowercase hex characters, got %d characters", len(photos.AccountID)))
	case !r2BucketPattern.MatchString(photos.Bucket):
		errs = append(errs, fmt.Errorf("R2_PHOTOS_BUCKET must be 3 to 63 lowercase letters, digits or hyphens, got %d characters", len(photos.Bucket)))
	default:
		cfg.Photos = photos
	}

	if len(missing) > 0 {
		errs = append([]error{fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))}, errs...)
	}
	return cfg, errors.Join(errs...)
}
