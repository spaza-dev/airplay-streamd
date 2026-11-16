package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type StorageConfig struct {
	Endpoint  string
	Bucket    string
	Region    string
	AccessKey string
	SecretKey string
	Prefix    string
	PathStyle bool
}

type ObjectInfo struct {
	Key          string
	Size         int64
	LastModified time.Time
	ETag         string
}

type StorageClient struct {
	cfg    StorageConfig
	client *s3.Client
}

func storageConfigFromEnv() StorageConfig {
	return StorageConfig{
		Endpoint:  envOr("S3_ENDPOINT", ""),
		Bucket:    strings.TrimSpace(os.Getenv("S3_BUCKET")),
		Region:    envOr("S3_REGION", "auto"),
		AccessKey: envOr("S3_ACCESS_KEY", os.Getenv("S3_ACCESS_KEY_ID")),
		SecretKey: envOr("S3_SECRET_KEY", os.Getenv("S3_SECRET_ACCESS_KEY")),
		Prefix:    os.Getenv("S3_PREFIX"),
		PathStyle: envBool("S3_PATH_STYLE", true),
	}
}

func NewStorageClient(ctx context.Context, cfg StorageConfig) (*StorageClient, error) {
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, fmt.Errorf("S3_BUCKET is required")
	}
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = "auto"
	}
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	if cfg.AccessKey != "" || cfg.SecretKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load storage config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if strings.TrimSpace(cfg.Endpoint) != "" {
			o.BaseEndpoint = aws.String(strings.TrimRight(cfg.Endpoint, "/"))
		}
		o.UsePathStyle = cfg.PathStyle
	})
	return &StorageClient{cfg: cfg, client: client}, nil
}

func normalizePrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	prefix = strings.TrimLeft(prefix, "/")
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return prefix
}

func (s *StorageClient) key(key string) string {
	return normalizePrefix(s.cfg.Prefix) + strings.TrimLeft(key, "/")
}

func (s *StorageClient) ListCommonPrefixes(ctx context.Context, prefix string) ([]string, error) {
	input := &s3.ListObjectsV2Input{
		Bucket:    aws.String(s.cfg.Bucket),
		Prefix:    aws.String(s.key(prefix)),
		Delimiter: aws.String("/"),
	}
	paginator := s3.NewListObjectsV2Paginator(s.client, input)
	seen := map[string]bool{}
	var out []string
	base := s.key(prefix)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, cp := range page.CommonPrefixes {
			value := strings.TrimPrefix(aws.ToString(cp.Prefix), base)
			value = strings.TrimSuffix(value, "/")
			if value != "" && !seen[value] {
				seen[value] = true
				out = append(out, value)
			}
		}
	}
	return out, nil
}

func (s *StorageClient) GetBytes(ctx context.Context, key string, maxBytes int64) ([]byte, error) {
	resp, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(s.key(key)),
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	reader := io.Reader(resp.Body)
	if maxBytes > 0 {
		reader = io.LimitReader(resp.Body, maxBytes+1)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if maxBytes > 0 && int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("object %s exceeds %d bytes", key, maxBytes)
	}
	return data, nil
}

func (s *StorageClient) DownloadToFile(ctx context.Context, key, path string) (int64, error) {
	resp, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(s.key(key)),
	})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return 0, err
	}
	tmp := path + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return 0, err
	}
	n, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return n, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return n, closeErr
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return n, err
	}
	return n, nil
}

func (s *StorageClient) Head(ctx context.Context, key string) (*ObjectInfo, error) {
	resp, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(s.key(key)),
	})
	if err != nil {
		return nil, err
	}
	return &ObjectInfo{
		Key:          key,
		Size:         aws.ToInt64(resp.ContentLength),
		LastModified: aws.ToTime(resp.LastModified).UTC(),
		ETag:         strings.Trim(aws.ToString(resp.ETag), `"`),
	}, nil
}

func (s *StorageClient) Exists(ctx context.Context, key string) bool {
	_, err := s.Head(ctx, key)
	return err == nil
}
