package backup

import (
	"context"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type S3Uploader struct {
	cfg UploadConfig
}

func NewS3Uploader(cfg UploadConfig) *S3Uploader {
	return &S3Uploader{cfg: cfg}
}

func (u *S3Uploader) Upload(ctx context.Context, input UploadInput) (UploadResult, error) {
	file, err := os.Open(input.ArtifactPath)
	if err != nil {
		return UploadResult{}, fmt.Errorf("open backup artifact for upload: %w", err)
	}
	defer file.Close()

	loadOptions := []func(*config.LoadOptions) error{}
	if u.cfg.Region != "" {
		loadOptions = append(loadOptions, config.WithRegion(u.cfg.Region))
	}
	awsCfg, err := config.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		return UploadResult{}, fmt.Errorf("load aws config: %w", err)
	}
	if awsCfg.Region == "" && u.cfg.Endpoint != "" {
		// The SDK still requires a signing region when BaseEndpoint points at
		// an S3-compatible provider.
		awsCfg.Region = "auto"
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if u.cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(u.cfg.Endpoint)
		}
		o.UsePathStyle = u.cfg.PathStyle
	})

	out, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(input.Bucket),
		Key:    aws.String(input.Key),
		Body:   file,
	})
	if err != nil {
		return UploadResult{}, fmt.Errorf("put backup artifact to s3: %w", err)
	}

	result := UploadResult{}
	if out.ETag != nil {
		result.ETag = *out.ETag
	}
	return result, nil
}
