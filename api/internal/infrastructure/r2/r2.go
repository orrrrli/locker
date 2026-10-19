// Package r2 talks to the Cloudflare R2 bucket for post photos through R2's
// S3-compatible API. It signs the PUT URLs the app uploads photos with
// (BE-FEED-1-T1), heads objects to check them (BE-FEED-2-T3) and deletes
// them; it never moves photo bytes itself.
package r2

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/orrrrli/locker/api/internal/domain"
)

// requestTimeout bounds one call to R2, retries aside.
const requestTimeout = 10 * time.Second

// Config is the bucket and the bucket-scoped access key (OPS-FEED-1).
type Config struct {
	AccountID       string
	AccessKeyID     string
	SecretAccessKey string
	Bucket          string
	// Endpoint overrides https://<AccountID>.r2.cloudflarestorage.com; tests
	// point it at a local server.
	Endpoint string
}

type Client struct {
	s3      *s3.Client
	presign *s3.PresignClient
	bucket  string
}

func New(cfg Config) *Client {
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "https://" + cfg.AccountID + ".r2.cloudflarestorage.com"
	}
	c := s3.New(s3.Options{
		// R2 takes "auto" as its only region.
		Region:       "auto",
		BaseEndpoint: aws.String(endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		// The SDK has no timeout of its own; a slow R2 would otherwise hold
		// the ticker, and every job behind DeleteUnused, indefinitely.
		HTTPClient: awshttp.NewBuildableClient().WithTimeout(requestTimeout),
		// Path style keeps the bucket out of the host name, so a test server
		// on 127.0.0.1 works the same as R2.
		UsePathStyle: true,
		// Defensive: presigning already skips default checksums in this SDK
		// version; this keeps a later SDK from adding checksum parameters the
		// app would have to send too. R2 does not need them.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &Client{s3: c, presign: s3.NewPresignClient(c), bucket: cfg.Bucket}
}

// PresignPut returns a URL that accepts one PUT of exactly size bytes of
// contentType to key until ttl passes. Content-Type, Content-Length and
// If-None-Match: * are signed headers: a PUT with any other value fails R2's
// signature check, and once the object exists R2 documents a 412, so a
// photo cannot be swapped after a post checked it (BE-FEED-7). Untested
// through a presigned URL until the bucket exists (OPS-FEED-1 checklist).
func (c *Client) PresignPut(ctx context.Context, key, contentType string, size int64, ttl time.Duration) (string, error) {
	req, err := c.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(c.bucket),
		Key:           aws.String(key),
		ContentType:   aws.String(contentType),
		ContentLength: aws.Int64(size),
		IfNoneMatch:   aws.String("*"),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("r2: presign put: %w", err)
	}
	return req.URL, nil
}

// Delete removes the object; deleting a key that was never uploaded is not
// an error.
func (c *Client) Delete(ctx context.Context, key string) error {
	if _, err := c.s3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(key)}); err != nil {
		return fmt.Errorf("r2: delete: %w", err)
	}
	return nil
}

// Head returns the object's type and size; domain.ErrNotFound when it was
// never uploaded (BE-FEED-2-T3).
func (c *Client) Head(ctx context.Context, key string) (string, int64, error) {
	out, err := c.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(key)})
	var re *awshttp.ResponseError
	if errors.As(err, &re) && re.HTTPStatusCode() == http.StatusNotFound {
		return "", 0, domain.ErrNotFound
	}
	if err != nil {
		return "", 0, fmt.Errorf("r2: head: %w", err)
	}
	return aws.ToString(out.ContentType), aws.ToInt64(out.ContentLength), nil
}

// PresignGet returns a URL that serves the object at key until ttl passes.
// Untested against a real bucket until OPS-FEED-1.
func (c *Client) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	req, err := c.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("r2: presign get: %w", err)
	}
	return req.URL, nil
}
