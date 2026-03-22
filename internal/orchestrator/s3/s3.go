package s3

import (
	"bytes"
	"context"
	"fmt"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/backflow-labs/backflow/internal/config"
)

// Uploader uploads data to S3 (agent output, offloaded task config).
type Uploader struct {
	client *s3.Client
	bucket string
}

// NewUploader creates a new Uploader. Returns nil if no bucket is configured.
func NewUploader(ctx context.Context, cfg *config.Config) (*Uploader, error) {
	if cfg.S3Bucket == "" {
		return nil, nil
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.AWSRegion))
	if err != nil {
		return nil, fmt.Errorf("load AWS config for S3: %w", err)
	}

	return &Uploader{
		client: s3.NewFromConfig(awsCfg),
		bucket: cfg.S3Bucket,
	}, nil
}

// Upload stores data in S3 as text/plain and returns the s3:// URL.
func (u *Uploader) Upload(ctx context.Context, key string, data []byte) (string, error) {
	return u.upload(ctx, key, data, "text/plain")
}

// UploadJSON stores JSON data in S3 with the application/json content type.
func (u *Uploader) UploadJSON(ctx context.Context, key string, data []byte) (string, error) {
	return u.upload(ctx, key, data, "application/json")
}

func (u *Uploader) upload(ctx context.Context, key string, data []byte, contentType string) (string, error) {
	_, err := u.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &u.bucket,
		Key:         &key,
		Body:        bytes.NewReader(data),
		ContentType: &contentType,
	})
	if err != nil {
		return "", fmt.Errorf("s3 put %s: %w", key, err)
	}

	return fmt.Sprintf("s3://%s/%s", u.bucket, key), nil
}

// PresignGetURL returns a pre-signed GET URL for the given S3 key.
func (u *Uploader) PresignGetURL(ctx context.Context, key string, expiry time.Duration) (string, error) {
	presigner := s3.NewPresignClient(u.client)
	req, err := presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: &u.bucket,
		Key:    &key,
	}, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", fmt.Errorf("s3 presign: %w", err)
	}
	return req.URL, nil
}
