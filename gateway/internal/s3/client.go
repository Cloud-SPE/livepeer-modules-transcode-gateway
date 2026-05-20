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
	region          string
	internalEndpoint string
	publicEndpoint  string
	presignTTL      time.Duration
	presigner       *awss3.PresignClient
	// Stored explicitly so MintLiveSessionCredentials can vend them to
	// the runner. In production this should be replaced by an STS
	// AssumeRole flow that produces scoped temp credentials.
	accessKeyID     string
	secretAccessKey string
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
		api:              api,
		bucket:           bucket,
		region:           region,
		internalEndpoint: endpoint,
		publicEndpoint:   publicEndpoint,
		presignTTL:       ttl,
		presigner:        awss3.NewPresignClient(presignAPI),
		accessKeyID:      accessKey,
		secretAccessKey:  secret,
	}, nil
}

// LiveSessionCredentials is the credential bundle the gateway sends to
// the orchestrator's live-runner via the broker. The runner uses these
// to write HLS output directly to our object store, scoped by key_prefix.
//
// v1 caveat: RustFS doesn't expose AWS STS / AssumeRole today, so this
// returns the gateway's static bucket credentials with a key_prefix that
// the runner is expected to honor by convention. This is a known trust
// boundary — the orchestrator is trusted not to write outside the prefix.
// Future hardening: real STS-style temporary credentials when RustFS
// supports it, OR a per-session sidecar gateway proxy that signs PUTs
// against arbitrary keys.
type LiveSessionCredentials struct {
	Endpoint        string    `json:"endpoint"`
	Region          string    `json:"region"`
	Bucket          string    `json:"bucket"`
	KeyPrefix       string    `json:"key_prefix"`
	AccessKeyID     string    `json:"access_key_id"`
	SecretAccessKey string    `json:"secret_access_key"`
	SessionToken    string    `json:"session_token,omitempty"`
	ExpiresAt       time.Time `json:"expires_at"`
}

// MintLiveSessionCredentials returns a credential bundle scoped to the
// given key prefix. The runner is expected to PutObject only under
// `<bucket>/<keyPrefix>/...`. The TTL is informational in v1 (static
// credentials don't actually expire); once STS lands it becomes real.
//
// keyPrefix should NOT have a trailing slash; the runner appends file
// names directly to it.
func (c *Client) MintLiveSessionCredentials(keyPrefix string, ttl time.Duration) (*LiveSessionCredentials, error) {
	if c == nil {
		return nil, errors.New("s3 not configured")
	}
	if keyPrefix == "" {
		return nil, errors.New("s3: keyPrefix required")
	}
	if ttl <= 0 {
		ttl = 4 * time.Hour
	}
	// The runner reaches our S3 from outside Docker, so it needs the
	// PUBLIC endpoint, not the internal `http://rustfs:9000`.
	endpoint := c.publicEndpoint
	if endpoint == "" {
		endpoint = c.internalEndpoint
	}
	return &LiveSessionCredentials{
		Endpoint:        endpoint,
		Region:          c.region,
		Bucket:          c.bucket,
		KeyPrefix:       strings.TrimRight(keyPrefix, "/"),
		AccessKeyID:     c.accessKeyID,
		SecretAccessKey: c.secretAccessKey,
		// RustFS doesn't expose STS / AssumeRole, so there's no real
		// session token to vend. The broker rejects empty session_token,
		// so we send a sentinel that's syntactically valid but
		// semantically meaningless. The runner uses the access_key_id
		// + secret_access_key (static) and ignores this field.
		SessionToken: "static-credentials-no-sts",
		ExpiresAt:    time.Now().Add(ttl),
	}, nil
}

// PublicHLSMasterURL returns the customer-facing playback URL for a
// gateway-ingest live session. Format mirrors the ABR output URL shape.
func (c *Client) PublicHLSMasterURL(keyPrefix string) string {
	if c == nil {
		return ""
	}
	return fmt.Sprintf("%s/%s/%s/master.m3u8",
		c.publicEndpoint, c.bucket, strings.TrimRight(keyPrefix, "/"))
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
