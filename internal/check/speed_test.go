package check

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"proxy-checker/internal/domain"
	"proxy-checker/internal/providers"
	"proxy-checker/internal/proxy"
	"proxy-checker/internal/store"
	"proxy-checker/internal/testkit"
)

func TestCalculateMbps(t *testing.T) {
	got, err := CalculateMbps(8_000_000, 2*time.Second)
	if err != nil || got != 32 {
		t.Fatalf("mbps=%v err=%v", got, err)
	}
	if got, err := CalculateMbps(1_000_000, time.Second); err != nil || got != 8 {
		t.Fatalf("mbps=%v err=%v", got, err)
	}
	if got, err := CalculateMbps(125_000, 100*time.Millisecond); err != nil || got != 10 {
		t.Fatalf("mbps=%v err=%v", got, err)
	}
	for name, tc := range map[string]struct {
		n int64
		d time.Duration
	}{
		"zero duration": {1, 0}, "negative duration": {1, -time.Second}, "zero bytes": {0, time.Second}, "negative bytes": {-5, time.Second},
	} {
		if _, err := CalculateMbps(tc.n, tc.d); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestLowConfidenceThreshold(t *testing.T) {
	if !LowConfidence(249 * time.Millisecond) {
		t.Error("249ms must be low confidence")
	}
	if LowConfidence(250 * time.Millisecond) {
		t.Error("250ms is not low confidence")
	}
}

func TestSpeedConfig(t *testing.T) {
	d := DefaultSpeedConfig()
	if d.SampleBytes != 8<<20 || d.WarmupBytes != 256<<10 || d.DirectionTimeout != 30*time.Second || d.Validate() != nil {
		t.Fatalf("defaults: %+v", d)
	}
	for name, mod := range map[string]func(*SpeedConfig){
		"sample too small": func(c *SpeedConfig) { c.SampleBytes = 0 },
		"sample too big":   func(c *SpeedConfig) { c.SampleBytes = 64<<20 + 1 },
		"warmup negative":  func(c *SpeedConfig) { c.WarmupBytes = -1 },
		"warmup too big":   func(c *SpeedConfig) { c.WarmupBytes = 4<<20 + 1 },
		"timeout short":    func(c *SpeedConfig) { c.DirectionTimeout = 999 * time.Millisecond },
		"timeout long":     func(c *SpeedConfig) { c.DirectionTimeout = 121 * time.Second },
	} {
		c := DefaultSpeedConfig()
		mod(&c)
		if c.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
	max := SpeedConfig{SampleBytes: 64 << 20, WarmupBytes: 0, DirectionTimeout: 120 * time.Second}
	if max.Validate() != nil {
		t.Error("maximum configuration rejected")
	}
}

type speedRig struct {
	t     *testing.T
	ca    *testkit.CA
	svc   *testkit.SpeedService
	clock *testkit.Clock
	db    *store.Store
	chk   *SpeedChecker
	proxy domain.Proxy
}

func newSpeedRig(t *testing.T) *speedRig {
	t.Helper()
	ctx := context.Background()
	ca := testkit.NewCA(t)
	svc := testkit.NewSpeedService(t, ca)
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	clock := testkit.NewClock(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))
	px := testkit.NewConnectProxy(t, testkit.ProxyOptions{Resolve: svc.Resolve()})
	host, port := px.Endpoint()
	return &speedRig{
		t: t, ca: ca, svc: svc, clock: clock, db: db,
		chk:   &SpeedChecker{Service: providers.NewCloudflareAt(svc.NamedURL("")), Pauses: db, Clock: clock},
		proxy: domain.Proxy{ID: 5, Name: "fx", Protocol: domain.ProtocolHTTP, Host: host, Port: port},
	}
}

func smallConfig() SpeedConfig {
	return SpeedConfig{SampleBytes: 256 << 10, WarmupBytes: 64 << 10, DirectionTimeout: 5 * time.Second}
}

func (r *speedRig) measure(ctx context.Context, cfg SpeedConfig) domain.Speed {
	r.t.Helper()
	c, err := proxy.NewClient(r.proxy, 60*time.Second, proxy.WithRootCAs(r.ca.Pool))
	if err != nil {
		r.t.Fatal(err)
	}
	return r.chk.Measure(ctx, r.proxy, c, cfg)
}

func codeOf(tr domain.Transfer) string {
	if tr.Failure == nil {
		return ""
	}
	return tr.Failure.Code
}

func sum(rs []testkit.DownloadRecord) (n int64) {
	for _, r := range rs {
		n += r.Sent
	}
	return
}

func sumUp(rs []testkit.UploadRecord) (n int64) {
	for _, r := range rs {
		n += r.Received
	}
	return
}

func TestMeasureSuccessCountsRealBytes(t *testing.T) {
	r := newSpeedRig(t)
	cfg := smallConfig()
	res := r.measure(context.Background(), cfg)

	if res.Provider != "cloudflare" || !strings.Contains(res.Method, "warmup") || res.ProxyID != 5 || !res.CheckedAt.Equal(r.clock.Now()) {
		t.Fatalf("%+v", res)
	}
	for name, tr := range map[string]domain.Transfer{"download": res.Download, "upload": res.Upload} {
		if tr.Failure != nil || tr.Mbps == nil || *tr.Mbps <= 0 || tr.Bytes != cfg.SampleBytes || tr.Duration <= 0 {
			t.Fatalf("%s: %+v", name, tr)
		}
		want, _ := CalculateMbps(tr.Bytes, tr.Duration)
		if *tr.Mbps != want {
			t.Errorf("%s: Mbps %v is not bytes*8/duration (%v)", name, *tr.Mbps, want)
		}
	}
	downs, ups := r.svc.Downloads(), r.svc.Uploads()
	if len(downs) != 2 || len(ups) != 2 {
		t.Fatalf("requests: %d downloads, %d uploads", len(downs), len(ups))
	}
	if downs[0].Requested != cfg.WarmupBytes || downs[1].Requested != cfg.SampleBytes ||
		ups[0].ContentLength != cfg.WarmupBytes || ups[1].ContentLength != cfg.SampleBytes {
		t.Fatalf("warmup must precede the sample: %+v %+v", downs, ups)
	}
	for _, u := range ups {
		if u.Received != u.ContentLength || len(u.TransferEncoding) != 0 || u.ContentEncoding != "" {
			t.Errorf("upload not a plain fixed-length body: %+v", u)
		}
	}
	for _, d := range downs {
		if d.Sent != d.Requested || (d.AcceptEncoding != "identity" && d.AcceptEncoding != "") {
			t.Errorf("download: %+v", d)
		}
	}
	// Total bytes include warmups and are what really crossed the wire.
	if want := sum(downs) + sumUp(ups); res.TotalBytes != want || want != 2*(cfg.SampleBytes+cfg.WarmupBytes) {
		t.Fatalf("TotalBytes = %d, wire bytes = %d", res.TotalBytes, want)
	}
}

func TestLowConfidenceIsFlaggedForShortSamples(t *testing.T) {
	r := newSpeedRig(t)
	res := r.measure(context.Background(), SpeedConfig{SampleBytes: 8 << 10, DirectionTimeout: 5 * time.Second})
	if res.Download.Failure != nil || !res.Download.LowConfidence || !res.Upload.LowConfidence {
		t.Fatalf("a tiny sample on loopback is far below 250 ms: %+v %+v", res.Download, res.Upload)
	}
	r.svc.Set(testkit.SpeedBehavior{ChunkDelay: 40 * time.Millisecond, UploadReadDelay: 40 * time.Millisecond})
	res = r.measure(context.Background(), SpeedConfig{SampleBytes: 256 << 10, DirectionTimeout: 20 * time.Second}) // 8 chunks
	for name, tr := range map[string]domain.Transfer{"download": res.Download, "upload": res.Upload} {
		if tr.Mbps == nil || tr.LowConfidence || tr.Duration < 250*time.Millisecond {
			t.Errorf("%s: a >=250 ms sample is not low confidence: %+v", name, tr)
		}
	}
}

func TestDownloadProblemsKeepTheUploadResult(t *testing.T) {
	for name, tc := range map[string]struct {
		b    testkit.SpeedBehavior
		code string
		// whether the body is read at all before the problem is detected
		readsBody bool
	}{
		"short body":       {testkit.SpeedBehavior{NoContentLength: true, DownloadShortBy: 1000}, "invalid_response", true},
		"extra bytes":      {testkit.SpeedBehavior{NoContentLength: true, DownloadExtra: 1000}, "invalid_response", true},
		"length mismatch":  {testkit.SpeedBehavior{ContentLengthDelta: 10}, "invalid_response", false},
		"server error 500": {testkit.SpeedBehavior{DownloadStatus: 500}, "provider_unavailable", false},
	} {
		t.Run(name, func(t *testing.T) {
			r := newSpeedRig(t)
			r.svc.Set(tc.b)
			cfg := smallConfig()
			res := r.measure(context.Background(), cfg)
			if codeOf(res.Download) != tc.code || res.Download.Mbps != nil {
				t.Fatalf("download: %+v", res.Download)
			}
			if res.Download.Failure.Message == "" {
				t.Error("empty failure message")
			}
			if res.Upload.Failure != nil || res.Upload.Mbps == nil {
				t.Fatalf("a failed download must not hide a good upload: %+v", res.Upload)
			}
			var rcvd int64
			for _, d := range r.svc.Downloads() {
				rcvd += d.Sent
			}
			if tc.readsBody && res.TotalBytes < cfg.WarmupBytes {
				t.Errorf("TotalBytes %d lost failed-transfer bytes", res.TotalBytes)
			}
			if res.TotalBytes < 2*cfg.WarmupBytes {
				t.Errorf("TotalBytes %d must at least include both warmups", res.TotalBytes)
			}
		})
	}
}

func TestShortDownloadReportsBytesReceived(t *testing.T) {
	r := newSpeedRig(t)
	cfg := SpeedConfig{SampleBytes: 256 << 10, DirectionTimeout: 5 * time.Second}
	r.svc.Set(testkit.SpeedBehavior{NoContentLength: true, DownloadShortBy: 1000})
	res := r.measure(context.Background(), cfg)
	if res.Download.Bytes != cfg.SampleBytes-1000 || res.Download.Mbps != nil || codeOf(res.Download) != "invalid_response" {
		t.Fatalf("%+v", res.Download)
	}
	if want := sum(r.svc.Downloads()) + sumUp(r.svc.Uploads()); res.TotalBytes != want {
		t.Fatalf("TotalBytes %d, wire %d", res.TotalBytes, want)
	}
}

func TestUploadAcknowledgementIsValidated(t *testing.T) {
	for name, b := range map[string]testkit.SpeedBehavior{
		"reported more":   {UploadReportDelta: 5},
		"reported fewer":  {UploadReportDelta: -5},
		"missing header":  {UploadNoHeader: true},
		"500 after body":  {UploadStatus: 500},
		"redirect status": {UploadStatus: 302},
	} {
		r := newSpeedRig(t)
		r.svc.Set(b)
		cfg := SpeedConfig{SampleBytes: 256 << 10, DirectionTimeout: 5 * time.Second} // no warmup: the sample itself is checked
		res := r.measure(context.Background(), cfg)
		if res.Upload.Mbps != nil || res.Upload.Failure == nil {
			t.Errorf("%s: unconfirmed upload produced a rate: %+v", name, res.Upload)
		}
		if res.Download.Failure != nil || res.Download.Mbps == nil {
			t.Errorf("%s: download should be unaffected: %+v", name, res.Download)
		}
		if name != "500 after body" && name != "redirect status" && res.Upload.Bytes != cfg.SampleBytes {
			t.Errorf("%s: sent bytes = %d", name, res.Upload.Bytes)
		}
		want := map[string]string{"reported more": "invalid_response", "reported fewer": "invalid_response",
			"missing header": "invalid_response", "500 after body": "provider_unavailable", "redirect status": "invalid_response"}[name]
		if codeOf(res.Upload) != want {
			t.Errorf("%s: code %q, want %q", name, codeOf(res.Upload), want)
		}
	}
}

func TestRateLimitPausesTheWholeProviderBeforeAnotherProxy(t *testing.T) {
	r := newSpeedRig(t)
	ctx := context.Background()
	r.svc.Set(testkit.SpeedBehavior{DownloadStatus: 429, RetryAfter: "120"})
	cfg := smallConfig()
	res := r.measure(ctx, cfg)
	if codeOf(res.Download) != "provider_rate_limited" || codeOf(res.Upload) != "provider_rate_limited" {
		t.Fatalf("%+v %+v", res.Download, res.Upload)
	}
	if res.Upload.Bytes != 0 || len(r.svc.Uploads()) != 0 {
		t.Fatal("upload was attempted against a rate-limited provider")
	}
	pause, _ := r.db.GetProviderPause(ctx, "cloudflare")
	if pause.Reason != "rate_limited" || !pause.Until.Equal(r.clock.Now().Add(120*time.Second)) {
		t.Fatalf("pause %+v", pause)
	}

	// Another proxy must not touch the provider while it is paused.
	before := len(r.svc.Downloads())
	r.proxy.ID = 6
	again := r.measure(ctx, cfg)
	if len(r.svc.Downloads()) != before || len(r.svc.Uploads()) != 0 || again.TotalBytes != 0 {
		t.Fatal("traffic was generated during the provider cooldown")
	}
	if codeOf(again.Download) != "provider_rate_limited" || codeOf(again.Upload) != "provider_rate_limited" {
		t.Fatalf("%+v %+v", again.Download, again.Upload)
	}
	if g, err := r.chk.Gate(ctx); err != nil || !g.Paused || g.Reason != "rate_limited" {
		t.Fatalf("gate: %+v %v", g, err)
	}

	r.svc.Set(testkit.SpeedBehavior{})
	r.clock.Advance(121 * time.Second)
	ok := r.measure(ctx, cfg)
	if ok.Download.Mbps == nil || ok.Upload.Mbps == nil {
		t.Fatalf("not resumed after the cooldown: %+v %+v", ok.Download, ok.Upload)
	}
	if p, _ := r.db.GetProviderPause(ctx, "cloudflare"); p.Reason != "" || p.Attempt != 0 {
		t.Fatalf("success must clear the pause: %+v", p)
	}
}

func TestRateLimitWithoutRetryAfterBacksOff(t *testing.T) {
	r := newSpeedRig(t)
	ctx := context.Background()
	r.svc.Set(testkit.SpeedBehavior{DownloadStatus: 429})
	for i, d := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute} {
		r.measure(ctx, smallConfig())
		p, _ := r.db.GetProviderPause(ctx, "cloudflare")
		if !p.Until.Equal(r.clock.Now().Add(d)) {
			t.Fatalf("attempt %d: pause until %v, want %v", i+1, p.Until, r.clock.Now().Add(d))
		}
		r.clock.Advance(d)
	}
}

func TestUploadRateLimitPausesToo(t *testing.T) {
	r := newSpeedRig(t)
	r.svc.Set(testkit.SpeedBehavior{UploadStatus: 429, RetryAfter: "30"})
	res := r.measure(context.Background(), smallConfig())
	if codeOf(res.Upload) != "provider_rate_limited" || res.Download.Mbps == nil {
		t.Fatalf("%+v %+v", res.Download, res.Upload)
	}
	if p, _ := r.db.GetProviderPause(context.Background(), "cloudflare"); p.Reason != "rate_limited" {
		t.Fatalf("%+v", p)
	}
}

func TestForbiddenPausesUntilExplicitResume(t *testing.T) {
	r := newSpeedRig(t)
	ctx := context.Background()
	r.svc.Set(testkit.SpeedBehavior{DownloadStatus: 403})
	res := r.measure(ctx, smallConfig())
	if codeOf(res.Download) != "provider_forbidden" || codeOf(res.Upload) != "provider_forbidden" {
		t.Fatalf("%+v %+v", res.Download, res.Upload)
	}
	r.svc.Set(testkit.SpeedBehavior{})
	r.clock.Advance(500 * time.Hour)
	if g, _ := r.chk.Gate(ctx); !g.Paused || g.Reason != "forbidden" {
		t.Fatalf("a 403 pause ended with time: %+v", g)
	}
	n := len(r.svc.Downloads())
	r.measure(ctx, smallConfig())
	if len(r.svc.Downloads()) != n {
		t.Fatal("requests were sent while paused after 403")
	}
	if err := r.db.ClearProviderPause(ctx, "cloudflare"); err != nil {
		t.Fatal(err)
	}
	if res := r.measure(ctx, smallConfig()); res.Download.Mbps == nil || res.Upload.Mbps == nil {
		t.Fatalf("not resumed: %+v %+v", res.Download, res.Upload)
	}
}

func TestOtherServerErrorsDoNotPause(t *testing.T) {
	r := newSpeedRig(t)
	r.svc.Set(testkit.SpeedBehavior{DownloadStatus: 503})
	r.measure(context.Background(), smallConfig())
	if g, _ := r.chk.Gate(context.Background()); g.Paused {
		t.Fatalf("5xx must not start a provider pause: %+v", g)
	}
}

func TestStalledSampleTimesOutAndKeepsBytes(t *testing.T) {
	r := newSpeedRig(t)
	r.svc.Set(testkit.SpeedBehavior{StallAfter: 100_000, UploadStallAfter: 100_000})
	cfg := SpeedConfig{SampleBytes: 1 << 20, DirectionTimeout: time.Second}
	start := time.Now()
	res := r.measure(context.Background(), cfg)
	if el := time.Since(start); el > 6*time.Second {
		t.Fatalf("took %v", el)
	}
	if codeOf(res.Download) != "timeout" || res.Download.Mbps != nil || res.Download.Bytes < 90_000 || res.Download.Bytes > 110_000 {
		t.Fatalf("download: %+v", res.Download)
	}
	if res.Download.Duration < 900*time.Millisecond {
		t.Errorf("download duration %v is shorter than the timeout", res.Download.Duration)
	}
	if codeOf(res.Upload) != "timeout" || res.Upload.Mbps != nil || res.Upload.Bytes == 0 {
		t.Fatalf("upload: %+v", res.Upload)
	}
	if res.TotalBytes < res.Download.Bytes+res.Upload.Bytes {
		t.Fatalf("TotalBytes %d lost the failed transfers", res.TotalBytes)
	}
}

func TestCancellationStopsMidStream(t *testing.T) {
	r := newSpeedRig(t)
	r.svc.Set(testkit.SpeedBehavior{ChunkDelay: 50 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	res := r.measure(ctx, SpeedConfig{SampleBytes: 8 << 20, DirectionTimeout: 30 * time.Second})
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("cancellation took %v", el)
	}
	if codeOf(res.Download) != "canceled" || res.Download.Mbps != nil || res.Download.Bytes == 0 {
		t.Fatalf("download: %+v", res.Download)
	}
	if codeOf(res.Upload) != "canceled" || res.Upload.Bytes != 0 || len(r.svc.Uploads()) != 0 {
		t.Fatalf("upload must not start after cancellation: %+v", res.Upload)
	}
}

func TestInvalidConfigurationGeneratesNoTraffic(t *testing.T) {
	r := newSpeedRig(t)
	res := r.measure(context.Background(), SpeedConfig{SampleBytes: 100 << 20, DirectionTimeout: time.Second})
	if codeOf(res.Download) != "invalid_config" || codeOf(res.Upload) != "invalid_config" || res.TotalBytes != 0 {
		t.Fatalf("%+v", res)
	}
	if len(r.svc.Downloads())+len(r.svc.Uploads()) != 0 {
		t.Fatal("traffic generated with an invalid configuration")
	}
}

func TestMemoryStaysBoundedForLargeSamples(t *testing.T) {
	if testing.Short() {
		t.Skip("moves 64 MiB through loopback")
	}
	r := newSpeedRig(t)
	cfg := SpeedConfig{SampleBytes: 32 << 20, DirectionTimeout: 60 * time.Second}

	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var peak atomic.Uint64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(3 * time.Millisecond):
				runtime.ReadMemStats(&m)
				if m.HeapAlloc > peak.Load() {
					peak.Store(m.HeapAlloc)
				}
			}
		}
	}()
	res := r.measure(context.Background(), cfg)
	close(stop)
	<-done
	if res.Download.Mbps == nil || res.Upload.Mbps == nil {
		t.Fatalf("%+v %+v", res.Download, res.Upload)
	}
	growth := int64(peak.Load()) - int64(base.HeapAlloc)
	if growth > 16<<20 {
		t.Fatalf("heap grew by %d MiB while streaming a %d MiB sample", growth>>20, cfg.SampleBytes>>20)
	}
}

func TestUnmeasurableDurationGivesNoRateButIsNotAFailure(t *testing.T) {
	tr := domain.Transfer{Bytes: 1 << 20, Duration: 0}
	if err := finishTransfer(&tr); err == nil {
		t.Fatal("expected the nonpositive duration to be reported")
	}
	if tr.Mbps != nil || !tr.LowConfidence || tr.Failure != nil {
		t.Fatalf("%+v", tr)
	}
	ok := domain.Transfer{Bytes: 1_000_000, Duration: time.Second}
	if err := finishTransfer(&ok); err != nil || ok.Mbps == nil || *ok.Mbps != 8 || ok.LowConfidence {
		t.Fatalf("%+v %v", ok, err)
	}
}
