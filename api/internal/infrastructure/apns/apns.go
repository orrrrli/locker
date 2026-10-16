// Package apns pushes stored notices to iOS devices through the Apple Push
// Notification service, over the standard library's HTTP/2 (R11.7). It
// satisfies notifications.Pusher.
package apns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/orrrrli/locker/api/internal/domain"
)

const (
	ProductionHost = "https://api.push.apple.com"
	SandboxHost    = "https://api.sandbox.push.apple.com"
	// requestTimeout bounds one push, so a stalled APNs connection cannot
	// hold the sending goroutine forever.
	requestTimeout = 10 * time.Second
)

// Client sends pushes to APNs. Build it with New.
type Client struct {
	http  *http.Client
	host  string
	topic string
	auth  func() (string, error)
}

// Push sends n to every token in the background and returns at once: the
// notice is already in the inbox, so the request that stored it does not
// wait on Apple. Failures are logged, never returned.
//
// ponytail: one goroutine per notice, not a queue; a shutdown mid-send drops
// the rest of that notice's pushes, which the inbox still holds.
func (c *Client) Push(ctx context.Context, n domain.Notification, tokens []string) {
	go c.send(context.WithoutCancel(ctx), n, tokens)
}

// send pushes n to each token in turn. HTTP/2 multiplexes them over one
// connection, so one at a time costs little.
func (c *Client) send(ctx context.Context, n domain.Notification, tokens []string) {
	body, err := payload(n)
	if err != nil {
		slog.ErrorContext(ctx, "apns: build payload", "notification_id", n.ID, "err", err)
		return
	}
	for _, token := range tokens {
		if err := c.sendOne(ctx, token, body); err != nil {
			slog.WarnContext(ctx, "apns: push", "notification_id", n.ID, "err", err)
		}
	}
}

// Error is a push APNs answered with a status other than 200. Reason is
// Apple's reason string, like BadDeviceToken.
type Error struct {
	Status int
	Reason string
}

func (e *Error) Error() string {
	return "apns: status " + strconv.Itoa(e.Status) + " " + e.Reason
}

func (c *Client) sendOne(ctx context.Context, token string, body []byte) error {
	bearer, err := c.auth()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.host+"/3/device/"+url.PathEscape(token), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("authorization", "bearer "+bearer)
	req.Header.Set("apns-topic", c.topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	req.Header.Set("content-type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("apns: send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	var r struct {
		Reason string `json:"reason"`
	}
	// The reason is best effort: a body that does not parse leaves it empty.
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&r)
	return &Error{Status: resp.StatusCode, Reason: r.Reason}
}

// payload is the APNs body: the alert the lock screen shows, plus
// notification_id so the app can open the notice and mark it read (R11.8).
func payload(n domain.Notification) ([]byte, error) {
	type alert struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	type aps struct {
		Alert alert  `json:"alert"`
		Sound string `json:"sound"`
	}
	return json.Marshal(struct {
		APS            aps   `json:"aps"`
		NotificationID int64 `json:"notification_id"`
	}{aps{alert{n.Title, n.Body}, "default"}, n.ID})
}
