package blob

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Bucket struct {
	client *minio.Client
	name   string
}

func NewBucket(endpoint, user, pass, name string) (*Bucket, error) {
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(user, pass, ""),
		Secure: false,
	})
	if err != nil {
		return nil, fmt.Errorf("minio client init failed: %w", err)
	}

	ctx := context.Background()
	exists, err := client.BucketExists(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("bucket check failed: %w", err)
	}
	if !exists {
		if err = client.MakeBucket(ctx, name, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("bucket creation failed: %w", err)
		}
	}

	return &Bucket{client: client, name: name}, nil
}

// UploadPodcastMetadata uploads show-level assets directly to the podcast root path.
func (b *Bucket) UploadPodcastMetadata(ctx context.Context, podcastID, assetType, filename, contentType string, body io.Reader, size int64, sourceURL string) (string, error) {
	ext := extensionFromContentType(contentType)
	objectKey := fmt.Sprintf("%s/%s/%s%s", podcastID, assetType, filename, ext)

	opts := minio.PutObjectOptions{
		ContentType: contentType,
		UserMetadata: map[string]string{
			"podcast-id": podcastID,
		},
	}

	if sourceURL != "" {
		opts.UserMetadata["source-url"] = sourceURL
	}

	_, err := b.client.PutObject(ctx, b.name, objectKey, body, size, opts)
	if err != nil {
		return "", fmt.Errorf("metadata upload failed: %w", err)
	}

	return objectKey, nil
}

// UploadAsset streams episode-level media to an entity-first, deterministic path.
// TODO rename
func (b *Bucket) UploadAsset(ctx context.Context, podcastID, episodeGUID, assetType, filename, contentType string, body io.Reader, size int64, sourceURL string) (string, error) {
	ext := extensionFromContentType(contentType)
	objectKey := fmt.Sprintf("%s/%s/%s/%s%s", podcastID, episodeGUID, assetType, filename, ext)

	opts := minio.PutObjectOptions{
		ContentType: contentType,
		UserMetadata: map[string]string{
			"podcast-id":   podcastID,
			"episode-guid": episodeGUID,
		},
	}

	if sourceURL != "" {
		opts.UserMetadata["source-url"] = sourceURL
	}

	_, err := b.client.PutObject(ctx, b.name, objectKey, body, size, opts)
	if err != nil {
		return "", fmt.Errorf("episode asset upload failed: %w", err)
	}

	return objectKey, nil
}

// UploadSystemAsset uploads a fixed platform asset at a deterministic key.
// TODO rename
func (b *Bucket) UploadSystemAsset(ctx context.Context, objectKey, contentType string, data []byte) (string, error) {
	_, err := b.client.PutObject(ctx, b.name, objectKey, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return "", fmt.Errorf("system asset upload failed: %w", err)
	}
	return objectKey, nil
}

func (b *Bucket) UploadJSON(ctx context.Context, objectKey string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal failed: %w", err)
	}

	_, err = b.client.PutObject(ctx, b.name, objectKey, bytes.NewReader(payload), int64(len(payload)), minio.PutObjectOptions{
		ContentType: "application/json",
	})
	return err
}

func (b *Bucket) Download(ctx context.Context, objectKey string) (io.ReadCloser, error) {
	obj, err := b.client.GetObject(ctx, b.name, objectKey, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	if _, err := obj.Stat(); err != nil {
		return nil, fmt.Errorf("object inaccessible: %w", err)
	}
	return obj, nil
}

func extensionFromContentType(ct string) string {
	ct = strings.ToLower(ct)
	switch {
	case strings.Contains(ct, "audio/mpeg"), strings.Contains(ct, "audio/mp3"):
		return ".mp3"
	case strings.Contains(ct, "audio/mp4"), strings.Contains(ct, "audio/x-m4a"):
		return ".m4a"
	case strings.Contains(ct, "audio/ogg"):
		return ".ogg"
	case strings.Contains(ct, "audio/opus"):
		return ".opus"
	case strings.Contains(ct, "image/jpeg"), strings.Contains(ct, "image/jpg"):
		return ".jpg"
	case strings.Contains(ct, "image/png"):
		return ".png"
	case strings.Contains(ct, "xml"):
		return ".xml"
	default:
		if strings.Contains(ct, "image") {
			return ".img"
		}
		return ".audio"
	}
}
