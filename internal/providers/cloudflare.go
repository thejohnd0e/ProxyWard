package providers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// CloudflareURL is the hosted speed-test service. It is a best-effort,
// replaceable adapter: no quota or availability guarantee exists for
// automated use (see docs/research/2026-10-19-public-services.md).
const CloudflareURL = "https://speed.cloudflare.com"

// uploadAckHeader carries the number of bytes the service accepted.
const uploadAckHeader = "cf-meta-upload-bytes"

// Cloudflare builds requests for, and validates responses of, the speed
// endpoints. It performs no I/O itself.
type Cloudflare struct {
	BaseURL string
}

// NewCloudflare returns the production adapter.
func NewCloudflare() *Cloudflare { return &Cloudflare{BaseURL: CloudflareURL} }

// NewCloudflareAt returns an adapter for another base URL (fixtures).
func NewCloudflareAt(base string) *Cloudflare {
	return &Cloudflare{BaseURL: strings.TrimRight(base, "/")}
}

// Name identifies the provider in results and pauses.
func (c *Cloudflare) Name() string { return "cloudflare" }

func (c *Cloudflare) err(kind Kind, status int) error {
	return &Error{Provider: c.Name(), Kind: kind, Status: status}
}

// DownloadRequest asks for exactly n bytes without compression.
func (c *Cloudflare) DownloadRequest(ctx context.Context, n int64) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/__down?bytes=%d", c.BaseURL, n), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", "proxy-checker")
	return req, nil
}

// UploadRequest posts n bytes read from body with a known Content-Length.
func (c *Cloudflare) UploadRequest(ctx context.Context, body io.Reader, n int64) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/__up", body)
	if err != nil {
		return nil, err
	}
	req.ContentLength = n
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("User-Agent", "proxy-checker")
	return req, nil
}

// checkStatus maps non-200 statuses to provider errors.
func (c *Cloudflare) checkStatus(resp *http.Response) error {
	switch s := resp.StatusCode; {
	case s == http.StatusOK:
		return nil
	case s == http.StatusTooManyRequests:
		return &Error{Provider: c.Name(), Kind: KindRateLimited, Status: s,
			RetryAfter: ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
	case s == http.StatusUnauthorized || s == http.StatusForbidden:
		return c.err(KindForbidden, s)
	case s >= 400:
		return c.err(KindUnavailable, s)
	default:
		return c.err(KindInvalidResponse, s)
	}
}

// CheckDownload validates the status and, when announced, the Content-Length.
func (c *Cloudflare) CheckDownload(resp *http.Response, n int64) error {
	if err := c.checkStatus(resp); err != nil {
		return err
	}
	if resp.ContentLength >= 0 && resp.ContentLength != n {
		return c.err(KindInvalidResponse, resp.StatusCode)
	}
	return nil
}

// CheckUpload validates the status and that the service acknowledged exactly
// n accepted bytes.
func (c *Cloudflare) CheckUpload(resp *http.Response, n int64) error {
	if err := c.checkStatus(resp); err != nil {
		return err
	}
	got, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get(uploadAckHeader)), 10, 64)
	if err != nil || got != n {
		return c.err(KindInvalidResponse, resp.StatusCode)
	}
	return nil
}
