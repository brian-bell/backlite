package orchestrator

import (
	"bytes"
	"context"
	"fmt"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/backflow-labs/backflow/internal/config"
)

// S3Uploader uploads data to S3 (agent output, offloaded task config).
type S3Uploader struct {
	client *s3.Client
	bucket string
}

// NewS3Uploader creates a new S3Uploader. Returns nil if no bucket is configured.
func NewS3Uploader(ctx context.Context, cfg *config.Config) (*S3Uploader, error) {
	if cfg.S3Bucket == "" {
		return nil, nil
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.AWSRegion))
	if err != nil {
		return nil, fmt.Errorf("load AWS config for S3: %w", err)
	}

	return &S3Uploader{
		client: s3.NewFromConfig(awsCfg),
		bucket: cfg.S3Bucket,
	}, nil
}

// Upload stores data in S3 and returns the s3:// URL.
func (u *S3Uploader) Upload(ctx context.Context, key string, data []byte) (string, error) {
	contentType := "text/plain"
	_, err := u.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &u.bucket,
		Key:         &key,
		Body:        bytes.NewReader(data),
		ContentType: &contentType,
	})
	if err != nil {
		return "", fmt.Errorf("s3 put: %w", err)
	}

	return fmt.Sprintf("s3://%s/%s", u.bucket, key), nil
}

// PresignGetURL returns a pre-signed GET URL for the given S3 key.
func (u *S3Uploader) PresignGetURL(ctx context.Context, key string, expiry time.Duration) (string, error) {
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
