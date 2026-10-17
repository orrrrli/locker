package resend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeResend starts a server that records the last request and answers with
// status and body.
func fakeResend(t *testing.T, status int, body string) (*httptest.Server, *http.Request, *[]byte) {
	t.Helper()
	var got http.Request
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = *r.Clone(context.Background())
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &got, &gotBody
}

func newClient(srv *httptest.Server) *Client {
	return New(Config{Host: srv.URL, APIKey: "re_test", From: "Locker <no-reply@locker.center>", HTTP: srv.Client()})
}

func TestSendPostsTheEmail(t *testing.T) {
	srv, req, body := fakeResend(t, http.StatusOK, `{"id":"49a3999c-0ce1-4ea6-ab68-afcd6dc2e794"}`)

	err := newClient(srv).Send(context.Background(), "ana@example.com", "Verify", "Open the link")
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != http.MethodPost || req.URL.Path != "/emails" {
		t.Fatalf("request = %s %s, want POST /emails", req.Method, req.URL.Path)
	}
	if got := req.Header.Get("authorization"); got != "Bearer re_test" {
		t.Fatalf("authorization = %q", got)
	}
	if got := req.Header.Get("content-type"); got != "application/json" {
		t.Fatalf("content-type = %q", got)
	}
	var sent struct {
		From    string   `json:"from"`
		To      []string `json:"to"`
		Subject string   `json:"subject"`
		Text    string   `json:"text"`
	}
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.From != "Locker <no-reply@locker.center>" || len(sent.To) != 1 || sent.To[0] != "ana@example.com" ||
		sent.Subject != "Verify" || sent.Text != "Open the link" {
		t.Fatalf("body = %+v", sent)
	}
}

func TestSendReturnsResendError(t *testing.T) {
	srv, _, _ := fakeResend(t, http.StatusForbidden, `{"statusCode":403,"name":"validation_error","message":"The from address is not verified"}`)

	err := newClient(srv).Send(context.Background(), "ana@example.com", "s", "t")
	var e *Error
	if !errors.As(err, &e) || e.Status != http.StatusForbidden || e.Message != "The from address is not verified" {
		t.Fatalf("err = %v, want *Error 403 with Resend's message", err)
	}
}

func TestSendToleratesUnparsableErrorBody(t *testing.T) {
	srv, _, _ := fakeResend(t, http.StatusBadGateway, `<html>bad gateway</html>`)

	err := newClient(srv).Send(context.Background(), "ana@example.com", "s", "t")
	var e *Error
	if !errors.As(err, &e) || e.Status != http.StatusBadGateway || e.Message != "" {
		t.Fatalf("err = %v, want *Error 502 with no message", err)
	}
}

func TestSendHonorsContext(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(block) })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := newClient(srv).Send(ctx, "ana@example.com", "s", "t")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}
