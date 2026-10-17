// Package resend sends transactional email through Resend's HTTP API
// (https://resend.com/docs/api-reference/emails/send-email), over the
// standard library's net/http: verification and password-reset links (R3.2,
// R3.3).
package resend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	Host = "https://api.resend.com"
	// requestTimeout bounds one send, so a stalled Resend cannot hold the
	// caller forever.
	requestTimeout = 10 * time.Second
)

// Config is what New needs. From is the sender, like
// "Locker <no-reply@locker.center>", on a domain verified in Resend.
type Config struct {
	Host   string // Host, or the fake server's URL in tests
	APIKey string
	From   string
	// HTTP is the client sends go through; nil means one with
	// requestTimeout. Tests pass the fake server's.
	HTTP *http.Client
}

// Client sends email through Resend. Build it with New.
type Client struct {
	http   *http.Client
	host   string
	apiKey string
	from   string
}

// New returns a Client for cfg.
func New(cfg Config) *Client {
	hc := cfg.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: requestTimeout}
	}
	return &Client{http: hc, host: cfg.Host, apiKey: cfg.APIKey, from: cfg.From}
}

// Error is a send Resend answered with a status other than 2xx. Message is
// Resend's own explanation, like "The from address is not verified".
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	return "resend: status " + strconv.Itoa(e.Status) + " " + e.Message
}

// Send delivers one plain-text email to one recipient and returns once Resend
// has accepted it. It does not retry: the caller decides whether a failed
// email fails its request. It satisfies auth.Mailer.
//
// ponytail: text only; an HTML body is added when there is a design for it.
func (c *Client) Send(ctx context.Context, to, subject, text string) error {
	body, err := json.Marshal(struct {
		From    string   `json:"from"`
		To      []string `json:"to"`
		Subject string   `json:"subject"`
		Text    string   `json:"text"`
	}{c.from, []string{to}, subject, text})
	if err != nil {
		return fmt.Errorf("resend: encode: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.host+"/emails", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("resend: %w", err)
	}
	req.Header.Set("authorization", "Bearer "+c.apiKey)
	req.Header.Set("content-type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		// Drop the *url.Error wrapper so the message stays short; the URL
		// holds no secret, but the cause is what matters in the log.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("resend: send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	var r struct {
		Message string `json:"message"`
	}
	// The message is best effort: a body that does not parse leaves it empty.
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&r)
	return &Error{Status: resp.StatusCode, Message: r.Message}
}
