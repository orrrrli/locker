// Package r2 talks to the Cloudflare R2 bucket for post photos through R2's
// S3-compatible API. It signs the PUT URLs the app uploads photos with
// (BE-FEED-1-T1) and deletes objects; it never moves photo bytes itself.
package r2

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

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
		// Path style keeps the bucket out of the host name, so a test server
		// on 127.0.0.1 works the same as R2.
		UsePathStyle: true,
		// Without this the SDK adds checksum parameters the app would have to
		// send too; R2 does not need them.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &Client{s3: c, presign: s3.NewPresignClient(c), bucket: cfg.Bucket}
}

// PresignPut returns a URL that accepts one PUT of exactly size bytes of
// contentType to key, until ttl passes. Content-Type and Content-Length are
// signed headers: a PUT with any other value fails R2's signature check.
func (c *Client) PresignPut(ctx context.Context, key, contentType string, size int64, ttl time.Duration) (string, error) {
	req, err := c.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(c.bucket),
		Key:           aws.String(key),
		ContentType:   aws.String(contentType),
		ContentLength: aws.Int64(size),
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
