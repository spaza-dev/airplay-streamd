package main

import (
	"bytes"
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
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type StorageConfig struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	Endpoint  string `json:"endpoint"`
	Bucket    string `json:"bucket"`
	Region    string `json:"region"`
	AccessKey string `json:"access_key,omitempty"`
	SecretKey string `json:"-"`
	Prefix    string `json:"prefix"`
	PathStyle bool   `json:"path_style"`
	UpdatedAt string `json:"updated_at,omitempty"`
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

func NewStorageClient(ctx context.Context, cfg StorageConfig) (*StorageClient, error) {
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, fmt.Errorf("storage %q: bucket is required", cfg.Name)
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

func (s *StorageClient) ListObjects(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	input := &s3.ListObjectsV2Input{
		Bucket: aws.String(s.cfg.Bucket),
		Prefix: aws.String(s.key(prefix)),
	}
	paginator := s3.NewListObjectsV2Paginator(s.client, input)
	var out []ObjectInfo
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Contents {
			key := aws.ToString(item.Key)
			out = append(out, ObjectInfo{
				Key:          key,
				Size:         aws.ToInt64(item.Size),
				LastModified: aws.ToTime(item.LastModified).UTC(),
				ETag:         strings.Trim(aws.ToString(item.ETag), `"`),
			})
		}
	}
	return out, nil
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

func (s *StorageClient) PutBytes(ctx context.Context, key string, data []byte, contentType string) error {
	if contentType == "" {
		contentType = contentTypeFor(key)
	}
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.cfg.Bucket),
		Key:           aws.String(s.key(key)),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
		ContentType:   aws.String(contentType),
	})
	return err
}

func (s *StorageClient) PutFile(ctx context.Context, key, path, contentType string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.cfg.Bucket),
		Key:           aws.String(s.key(key)),
		Body:          f,
		ContentLength: aws.Int64(info.Size()),
		ContentType:   aws.String(contentType),
	})
	return err
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

func (s *StorageClient) PresignGet(ctx context.Context, key string, expires time.Duration) (string, error) {
	presigner := s3.NewPresignClient(s.client)
	req, err := presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(s.key(key)),
	}, func(opts *s3.PresignOptions) {
		opts.Expires = expires
	})
	if err != nil {
		return "", err
	}
	return req.URL, nil
}

func (s *StorageClient) Test(ctx context.Context) error {
	_, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(s.cfg.Bucket),
		MaxKeys: aws.Int32(1),
		Prefix:  aws.String(normalizePrefix(s.cfg.Prefix)),
	})
	return err
}

func contentTypeFor(key string) string {
	switch strings.ToLower(filepath.Ext(key)) {
	case ".json":
		return "application/json"
	case ".mdb":
		return "application/octet-stream"
	default:
		return "application/octet-stream"
	}
}

func (s *StorageClient) DeletePrefix(ctx context.Context, prefix string) error {
	objects, err := s.ListObjects(ctx, prefix)
	if err != nil {
		return err
	}
	for start := 0; start < len(objects); start += 1000 {
		end := start + 1000
		if end > len(objects) {
			end = len(objects)
		}
		ids := make([]s3types.ObjectIdentifier, 0, end-start)
		for _, object := range objects[start:end] {
			key := object.Key
			ids = append(ids, s3types.ObjectIdentifier{Key: aws.String(key)})
		}
		if _, err := s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(s.cfg.Bucket),
			Delete: &s3types.Delete{Objects: ids, Quiet: aws.Bool(true)},
		}); err != nil {
			return err
		}
	}
	return nil
}
