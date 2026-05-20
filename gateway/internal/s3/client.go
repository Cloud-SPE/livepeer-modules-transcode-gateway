package s3

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Client wraps an aws-sdk-go-v2 S3 client configured for an
// S3-compatible endpoint (RustFS in dev, anything S3 elsewhere).
type Client struct {
	api             *awss3.Client
	bucket          string
	publicEndpoint  string
	presignTTL      time.Duration
	presigner       *awss3.PresignClient
}

// New returns a configured S3 client. When accessKey/secret are empty,
// returns (nil, nil) so callers can no-op the upload-url surface.
func New(ctx context.Context, region, endpoint, publicEndpoint, bucket, accessKey, secret string, presignTTLSecs int) (*Client, error) {
	if accessKey == "" || secret == "" {
		return nil, nil
	}
	if endpoint == "" {
		return nil, errors.New("s3: endpoint required")
	}
	if bucket == "" {
		return nil, errors.New("s3: bucket required")
	}
	creds := credentials.NewStaticCredentialsProvider(accessKey, secret, "")
	cfg := aws.Config{
		Region:      region,
		Credentials: creds,
	}
	// Server-to-S3 client uses the internal endpoint (e.g. http://rustfs:9000
	// inside the compose network) for HeadBucket and any in-process S3 calls.
	api := awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	if publicEndpoint == "" {
		publicEndpoint = endpoint
	}
	publicEndpoint = strings.TrimRight(publicEndpoint, "/")
	// The presigner signs URLs that browsers consume — so it must sign for
	// the public hostname (e.g. http://localhost:9000). SigV4 binds the
	// host header into the signature; mismatching internal vs. public would
	// produce signatures the browser can't replay.
	presignAPI := awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		o.BaseEndpoint = aws.String(publicEndpoint)
		o.UsePathStyle = true
	})
	ttl := time.Duration(presignTTLSecs) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &Client{
		api:            api,
		bucket:         bucket,
		publicEndpoint: publicEndpoint,
		presignTTL:     ttl,
		presigner:      awss3.NewPresignClient(presignAPI),
	}, nil
}

// HeadBucket is a cheap readiness probe (used by /health).
func (c *Client) HeadBucket(ctx context.Context) error {
	if c == nil {
		return errors.New("s3 not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := c.api.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: aws.String(c.bucket)})
	return err
}

// PresignPut returns a presigned PUT URL the client uses to upload bytes
// directly to S3, plus the canonical object URL (suitable for handing to
// the runner as input_url).
type PresignedPut struct {
	UploadURL string
	ObjectURL string
	ExpiresAt time.Time
}

func (c *Client) PresignPut(ctx context.Context, key, contentType string) (*PresignedPut, error) {
	if c == nil {
		return nil, errors.New("s3 not configured")
	}
	in := &awss3.PutObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	out, err := c.presigner.PresignPutObject(ctx, in, awss3.WithPresignExpires(c.presignTTL))
	if err != nil {
		return nil, fmt.Errorf("s3: presign put: %w", err)
	}
	return &PresignedPut{
		UploadURL: out.URL,
		ObjectURL: fmt.Sprintf("%s/%s/%s", c.publicEndpoint, c.bucket, key),
		ExpiresAt: time.Now().Add(c.presignTTL),
	}, nil
}

// Bucket returns the configured bucket name.
func (c *Client) Bucket() string {
	if c == nil {
		return ""
	}
	return c.bucket
}

// PublicObjectURL returns the browser-reachable URL for an object key.
// The bucket is anonymous-read in dev so this URL is directly playable;
// in production wrap with a signed-GET layer.
func (c *Client) PublicObjectURL(key string) string {
	if c == nil {
		return ""
	}
	return fmt.Sprintf("%s/%s/%s", c.publicEndpoint, c.bucket, key)
}

// DeleteObject removes a single object. Idempotent — returns nil when
// the key doesn't exist.
func (c *Client) DeleteObject(ctx context.Context, key string) error {
	if c == nil {
		return errors.New("s3 not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := c.api.DeleteObject(ctx, &awss3.DeleteObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	return err
}

// DeletePrefix removes every object under a key prefix. Lists with
// pagination then batch-deletes up to 1000 keys per round-trip
// (S3 DeleteObjects limit). Returns the total count deleted.
func (c *Client) DeletePrefix(ctx context.Context, prefix string) (int, error) {
	if c == nil {
		return 0, errors.New("s3 not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	deleted := 0
	var token *string
	for {
		list, err := c.api.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{
			Bucket:            aws.String(c.bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return deleted, fmt.Errorf("s3: list %s: %w", prefix, err)
		}
		if len(list.Contents) > 0 {
			ids := make([]s3types.ObjectIdentifier, 0, len(list.Contents))
			for _, obj := range list.Contents {
				ids = append(ids, s3types.ObjectIdentifier{Key: obj.Key})
			}
			_, err := c.api.DeleteObjects(ctx, &awss3.DeleteObjectsInput{
				Bucket: aws.String(c.bucket),
				Delete: &s3types.Delete{Objects: ids, Quiet: aws.Bool(true)},
			})
			if err != nil {
				return deleted, fmt.Errorf("s3: batch delete: %w", err)
			}
			deleted += len(ids)
		}
		if list.IsTruncated == nil || !*list.IsTruncated {
			break
		}
		token = list.NextContinuationToken
	}
	return deleted, nil
}

// KeyFromURL extracts the bucket key from a publicly-formatted object
// URL (the same shape PublicObjectURL produces). Used by handlers that
// accept a URL from the client and need to perform a bucket-scoped
// operation on the underlying key. Returns "" when the URL doesn't
// belong to this client's bucket / endpoint.
func (c *Client) KeyFromURL(url string) string {
	if c == nil {
		return ""
	}
	prefix := c.publicEndpoint + "/" + c.bucket + "/"
	if !strings.HasPrefix(url, prefix) {
		return ""
	}
	return strings.TrimPrefix(url, prefix)
}

// ObjectExists reports whether an object key is present in the bucket.
// Used by `/v1/abr/{id}` to detect "runner finished" without needing a
// runner-status round-trip — the master playlist's presence is the only
// observable "done" signal until status proxying lands.
func (c *Client) ObjectExists(ctx context.Context, key string) (bool, error) {
	if c == nil {
		return false, errors.New("s3 not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := c.api.HeadObject(ctx, &awss3.HeadObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		// HeadObject returns a 404 wrapped as a NotFound API error; we
		// don't bother distinguishing the various error types here because
		// callers only care about the boolean. Surface the underlying
		// error too so the handler can log it if it wants.
		return false, nil
	}
	return true, nil
}
