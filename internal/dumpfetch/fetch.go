// Package dumpfetch resolves a dump URI to a local file path, downloading
// remote sources (S3, HTTP/HTTPS) to a temp file when necessary.
package dumpfetch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Fetch resolves uri to a local file path ready for use as a dump source.
//
// Supported URI forms:
//   - local path (no scheme): returned as-is; cleanup is a no-op
//   - s3://bucket/key: downloaded via the AWS SDK using ambient credentials
//   - http:// or https://: downloaded via net/http
//
// The caller must invoke cleanup() when the file is no longer needed.
// For local paths cleanup is a no-op; for downloads it removes the temp file.
func Fetch(ctx context.Context, uri string) (localPath string, cleanup func(), err error) {
	noop := func() {}
	switch {
	case strings.HasPrefix(uri, "s3://"):
		return fetchS3(ctx, uri)
	case strings.HasPrefix(uri, "http://"), strings.HasPrefix(uri, "https://"):
		return fetchHTTP(ctx, uri)
	default:
		return filepath.Clean(uri), noop, nil
	}
}

func fetchS3(ctx context.Context, uri string) (string, func(), error) {
	trimmed := strings.TrimPrefix(uri, "s3://")
	idx := strings.IndexByte(trimmed, '/')
	if idx < 0 {
		return "", nil, fmt.Errorf("dumpfetch: invalid S3 URI %q (expected s3://bucket/key)", uri)
	}
	bucket := trimmed[:idx]
	key := trimmed[idx+1:]

	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("dumpfetch: load AWS config: %w", err)
	}

	out, err := s3.NewFromConfig(cfg).GetObject(ctx, &s3.GetObjectInput{
		Bucket: &bucket,
		Key:    &key,
	})
	if err != nil {
		return "", nil, fmt.Errorf("dumpfetch: S3 get %s: %w", uri, err)
	}
	defer func() { _ = out.Body.Close() }()

	return writeTempFile(out.Body)
}

func fetchHTTP(ctx context.Context, uri string) (string, func(), error) {
	parsed, err := url.Parse(uri)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", nil, fmt.Errorf("dumpfetch: %q is not a valid http or https URI", uri)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", nil, fmt.Errorf("dumpfetch: build request for %s: %w", uri, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("dumpfetch: fetch %s: %w", uri, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("dumpfetch: fetch %s: HTTP %d", uri, resp.StatusCode)
	}
	return writeTempFile(resp.Body)
}

func writeTempFile(r io.Reader) (string, func(), error) {
	f, err := os.CreateTemp("", "ditto-dump-*.gz")
	if err != nil {
		return "", nil, fmt.Errorf("dumpfetch: create temp file: %w", err)
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	if _, err := io.Copy(f, r); err != nil {
		cleanup()
		_ = f.Close()
		return "", nil, fmt.Errorf("dumpfetch: write temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("dumpfetch: close temp file: %w", err)
	}
	return f.Name(), cleanup, nil
}
