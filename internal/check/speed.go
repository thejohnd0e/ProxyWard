package check

import (
	"context"
	crand "crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
	"github.com/thejohnd0e/ProxyWard/internal/providers"
	"github.com/thejohnd0e/ProxyWard/internal/store"
)

// LowConfidenceBelow is the sample duration under which a rate is flagged as
// low confidence: such a sample is dominated by start-up effects.
const LowConfidenceBelow = 250 * time.Millisecond

// Configuration bounds.
const (
	minSampleBytes = 1
	maxSampleBytes = 64 << 20
	maxWarmupBytes = 4 << 20
	minTimeout     = time.Second
	maxTimeout     = 120 * time.Second
	ioChunk        = 32 << 10
)

// SpeedConfig sets the size and time limits of one measurement.
type SpeedConfig struct {
	SampleBytes      int64         // timed bytes per direction
	WarmupBytes      int64         // untimed bytes per direction before the sample
	DirectionTimeout time.Duration // limit for warmup plus sample of one direction
}

// DefaultSpeedConfig returns the specified defaults: 8 MiB samples after
// 256 KiB warmups, 30 s per direction.
func DefaultSpeedConfig() SpeedConfig {
	return SpeedConfig{SampleBytes: 8 << 20, WarmupBytes: 256 << 10, DirectionTimeout: 30 * time.Second}
}

// Validate enforces the bounds: samples of 1 byte to 64 MiB, warmups up to
// 4 MiB, timeouts of 1 to 120 s.
func (c SpeedConfig) Validate() error {
	switch {
	case c.SampleBytes < minSampleBytes || c.SampleBytes > maxSampleBytes:
		return errors.New("sample size must be between 1 byte and 64 MiB")
	case c.WarmupBytes < 0 || c.WarmupBytes > maxWarmupBytes:
		return errors.New("warmup size must be between 0 and 4 MiB")
	case c.DirectionTimeout < minTimeout || c.DirectionTimeout > maxTimeout:
		return errors.New("direction timeout must be between 1 and 120 seconds")
	}
	return nil
}

// Method describes how the numbers were produced, for storage with results.
func (c SpeedConfig) Method() string {
	return fmt.Sprintf("app-stream-v1: download then upload through the proxy, warmup=%dB sample=%dB timeout=%s",
		c.WarmupBytes, c.SampleBytes, c.DirectionTimeout)
}

// CalculateMbps returns megabits per second: bytes * 8 / seconds / 1e6.
func CalculateMbps(bytes int64, elapsed time.Duration) (float64, error) {
	if bytes <= 0 {
		return 0, errors.New("no bytes were transferred")
	}
	if elapsed <= 0 {
		return 0, errors.New("elapsed time must be positive")
	}
	return float64(bytes) * 8 / elapsed.Seconds() / 1e6, nil
}

// LowConfidence reports whether a sample of this duration is too short to
// trust as more than a rough indication.
func LowConfidence(elapsed time.Duration) bool { return elapsed < LowConfidenceBelow }

// PauseStore persists provider-wide pauses (implemented by *store.Store).
type PauseStore interface {
	GetProviderPause(ctx context.Context, provider string) (store.ProviderPause, error)
	SetProviderPause(ctx context.Context, provider, reason string, until time.Time) error
	ClearProviderPause(ctx context.Context, provider string) error
}

// Gate reports whether the speed provider may be used.
type Gate struct {
	Paused bool
	Reason string // "rate_limited" or "forbidden"
	Until  time.Time
}

// SpeedChecker measures throughput through a proxy against one service.
type SpeedChecker struct {
	Service *providers.Cloudflare
	Pauses  PauseStore
	Clock   domain.Clock
}

// Gate returns the current provider pause, if any.
func (c *SpeedChecker) Gate(ctx context.Context) (Gate, error) {
	p, err := c.Pauses.GetProviderPause(ctx, c.Service.Name())
	if err != nil {
		return Gate{}, err
	}
	if !p.Active(c.Clock.Now()) {
		return Gate{}, nil
	}
	return Gate{Paused: true, Reason: p.Reason, Until: p.Until}, nil
}

func pauseFailure(reason string) *domain.Failure {
	if reason == store.GateForbidden {
		return NewFailure(CodeProviderForbidden)
	}
	return NewFailure(CodeProviderRateLimited)
}

type direction struct {
	transfer domain.Transfer
	total    int64 // every byte moved, warmup and failures included
	err      error
}

// Measure downloads and then uploads through client, which must route
// through p, with the service's cooldown applied first. Each direction is
// independent: a failure in one keeps the other's result. No rate is ever
// reported for a sample that did not complete and verify.
func (c *SpeedChecker) Measure(ctx context.Context, p domain.Proxy, client *http.Client, cfg SpeedConfig) domain.Speed {
	res := domain.Speed{ProxyID: p.ID, Provider: c.Service.Name(), Method: cfg.Method()}
	finish := func() domain.Speed {
		res.CheckedAt = c.Clock.Now()
		return res
	}
	failBoth := func(f *domain.Failure) domain.Speed {
		d, u := *f, *f
		res.Download.Failure, res.Upload.Failure = &d, &u
		return finish()
	}

	if err := cfg.Validate(); err != nil {
		return failBoth(NewFailure(CodeInvalidConfig))
	}
	gate, err := c.Gate(ctx)
	if err != nil {
		return failBoth(NewFailure(CodeProviderUnavailable))
	}
	if gate.Paused {
		return failBoth(pauseFailure(gate.Reason))
	}

	dl := c.download(ctx, client, cfg)
	res.Download, res.TotalBytes = dl.transfer, dl.total
	c.afterDirection(ctx, dl.err)

	// The download may just have started a pause; honour it before uploading.
	if ctx.Err() != nil {
		res.Upload.Failure = failureFor(ctx.Err())
		return finish()
	}
	switch gate, err := c.Gate(ctx); {
	case err != nil:
		res.Upload.Failure = NewFailure(CodeProviderUnavailable)
	case gate.Paused:
		res.Upload.Failure = pauseFailure(gate.Reason)
	default:
		ul := c.upload(ctx, client, cfg)
		res.Upload = ul.transfer
		res.TotalBytes += ul.total
		c.afterDirection(ctx, ul.err)
	}
	return finish()
}

// afterDirection updates the provider pause from a direction's outcome.
func (c *SpeedChecker) afterDirection(ctx context.Context, err error) {
	name, now := c.Service.Name(), c.Clock.Now()
	if err == nil {
		if p, gerr := c.Pauses.GetProviderPause(ctx, name); gerr == nil && (p.Reason != "" || p.Attempt > 0) {
			_ = c.Pauses.ClearProviderPause(ctx, name)
		}
		return
	}
	var pe *providers.Error
	if !errors.As(err, &pe) {
		return
	}
	switch pe.Kind {
	case providers.KindRateLimited:
		wait := pe.RetryAfter
		if wait <= 0 {
			attempt := 0
			if p, gerr := c.Pauses.GetProviderPause(ctx, name); gerr == nil {
				attempt = p.Attempt
			}
			wait = providers.Backoff(attempt + 1)
		}
		_ = c.Pauses.SetProviderPause(ctx, name, store.GateRateLimited, now.Add(wait))
	case providers.KindForbidden:
		_ = c.Pauses.SetProviderPause(ctx, name, store.GateForbidden, time.Time{})
	}
}

func failureFor(err error) *domain.Failure {
	f := Classify(err).Failure
	return &f
}

// finishTransfer derives the rate and confidence of a completed sample. A
// sample whose duration the clock cannot resolve (zero) gets no rate and is
// flagged low confidence: it completed, so it is not a failure, but a rate
// derived from it would be invented.
func finishTransfer(t *domain.Transfer) error {
	mbps, err := CalculateMbps(t.Bytes, t.Duration)
	if err != nil {
		t.LowConfidence = true
		return err
	}
	t.Mbps = &mbps
	t.LowConfidence = LowConfidence(t.Duration)
	return nil
}

func (c *SpeedChecker) invalid() error {
	return &providers.Error{Provider: c.Service.Name(), Kind: providers.KindInvalidResponse}
}

func (c *SpeedChecker) download(ctx context.Context, client *http.Client, cfg SpeedConfig) direction {
	ctx, cancel := context.WithTimeout(ctx, cfg.DirectionTimeout)
	defer cancel()
	var d direction
	if cfg.WarmupBytes > 0 {
		n, _, err := c.fetch(ctx, client, cfg.WarmupBytes)
		d.total += n
		if err != nil {
			d.err, d.transfer.Failure = err, failureFor(err)
			return d
		}
	}
	n, elapsed, err := c.fetch(ctx, client, cfg.SampleBytes)
	d.total += n
	d.transfer.Bytes, d.transfer.Duration = n, elapsed
	if err != nil {
		d.err, d.transfer.Failure = err, failureFor(err)
		return d
	}
	_ = finishTransfer(&d.transfer)
	return d
}

// fetch downloads exactly size bytes, streaming through a fixed buffer. The
// timer starts at the first body read and stops at the end of the body.
func (c *SpeedChecker) fetch(ctx context.Context, client *http.Client, size int64) (read int64, elapsed time.Duration, err error) {
	req, err := c.Service.DownloadRequest(ctx, size)
	if err != nil {
		return 0, 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	if err := c.Service.CheckDownload(resp, size); err != nil {
		return 0, 0, err
	}
	buf := make([]byte, ioChunk)
	start := time.Now()
	for {
		n, rerr := resp.Body.Read(buf)
		read += int64(n)
		if read > size {
			return read, time.Since(start), c.invalid()
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return read, time.Since(start), rerr
		}
	}
	elapsed = time.Since(start)
	if read < size {
		return read, elapsed, c.invalid()
	}
	return read, elapsed, nil
}

func (c *SpeedChecker) upload(ctx context.Context, client *http.Client, cfg SpeedConfig) direction {
	ctx, cancel := context.WithTimeout(ctx, cfg.DirectionTimeout)
	defer cancel()
	var d direction
	if cfg.WarmupBytes > 0 {
		sent, _, err := c.post(ctx, client, cfg.WarmupBytes)
		d.total += sent
		if err != nil {
			d.err, d.transfer.Failure = err, failureFor(err)
			return d
		}
	}
	sent, elapsed, err := c.post(ctx, client, cfg.SampleBytes)
	d.total += sent
	d.transfer.Bytes, d.transfer.Duration = sent, elapsed
	if err != nil {
		d.err, d.transfer.Failure = err, failureFor(err)
		return d
	}
	_ = finishTransfer(&d.transfer)
	return d
}

// post uploads size incompressible bytes with a known Content-Length. The
// timer starts when the transport first reads the body (transmission start)
// and stops when the response confirming the upload has arrived.
func (c *SpeedChecker) post(ctx context.Context, client *http.Client, size int64) (sent int64, elapsed time.Duration, err error) {
	body := newUploadBody(ctx, size)
	req, err := c.Service.UploadRequest(ctx, body, size)
	if err != nil {
		return 0, 0, err
	}
	resp, err := client.Do(req)
	end := time.Now()
	if err != nil {
		return body.sent(), body.elapsed(end), err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if err := c.Service.CheckUpload(resp, size); err != nil {
		return body.sent(), body.elapsed(end), err
	}
	return body.sent(), body.elapsed(end), nil
}

// uploadBody generates a bounded stream of incompressible bytes and records
// how many were handed to the transport and when the first one was.
type uploadBody struct {
	ctx       context.Context
	remaining int64
	rng       *rand.ChaCha8

	once  sync.Once
	start time.Time
	n     atomic.Int64
}

func newUploadBody(ctx context.Context, size int64) *uploadBody {
	var seed [32]byte
	_, _ = crand.Read(seed[:])
	return &uploadBody{ctx: ctx, remaining: size, rng: rand.NewChaCha8(seed)}
}

func (b *uploadBody) Read(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	if b.remaining <= 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > ioChunk {
		n = ioChunk
	}
	if n > b.remaining {
		n = b.remaining
	}
	b.once.Do(func() { b.start = time.Now() })
	_, _ = b.rng.Read(p[:n])
	b.remaining -= n
	b.n.Add(n)
	return int(n), nil
}

func (b *uploadBody) sent() int64 { return b.n.Load() }

// elapsed is the time from the first body read to end, or 0 if no byte was read.
func (b *uploadBody) elapsed(end time.Time) time.Duration {
	started := false
	b.once.Do(func() {}) // mark as done so a late first Read cannot race the check below
	if !b.start.IsZero() {
		started = true
	}
	if !started {
		return 0
	}
	return end.Sub(b.start)
}
