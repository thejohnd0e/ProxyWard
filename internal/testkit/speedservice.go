package testkit

import (
	"io"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"
)

// SpeedBehavior switches fixture misbehaviour on. The zero value is a
// well-behaved service.
type SpeedBehavior struct {
	// Download
	DownloadStatus     int           // 0 means 200
	RetryAfter         string        // sent with non-200 statuses
	DownloadShortBy    int64         // send this many bytes fewer than requested
	DownloadExtra      int64         // send this many bytes more (needs NoContentLength)
	NoContentLength    bool          // chunked response without Content-Length
	ContentLengthDelta int64         // announce and send requested+delta bytes
	ChunkDelay         time.Duration // pause after every 32 KiB written
	StallAfter         int64         // stop sending after this many bytes and block

	// Upload
	UploadStatus      int           // 0 means 200
	UploadReportDelta int64         // added to the reported byte count
	UploadNoHeader    bool          // omit the acknowledgement header
	UploadReadDelay   time.Duration // pause after every 32 KiB read
	UploadStallAfter  int64         // stop reading after this many bytes and block
}

// DownloadRecord describes a download request as the service saw it.
type DownloadRecord struct {
	Requested      int64
	Sent           int64 // bytes actually written to the connection
	AcceptEncoding string
}

// UploadRecord describes an upload request as the service saw it.
type UploadRecord struct {
	ContentLength    int64 // from the request header (-1 when unknown)
	Received         int64 // bytes actually read from the body
	TransferEncoding []string
	ContentEncoding  string
}

// SpeedService is an HTTPS fixture imitating the speed-test endpoints. It
// counts the bytes that really cross the wire; it never echoes client counters.
type SpeedService struct {
	*Target

	quit      chan struct{} // closed on cleanup to release stalled handlers
	mu        sync.Mutex
	behavior  SpeedBehavior
	downloads []DownloadRecord
	uploads   []UploadRecord
}

// NewSpeedService starts the fixture.
func NewSpeedService(t testing.TB, ca *CA) *SpeedService {
	t.Helper()
	s := &SpeedService{quit: make(chan struct{})}
	s.Target = NewTarget(t, ca, s.serve)
	// Registered after the target's own cleanup, so it runs first and frees
	// handlers that a stalled client left blocked.
	t.Cleanup(func() { close(s.quit) })
	return s
}

// Set replaces the fixture behaviour.
func (s *SpeedService) Set(b SpeedBehavior) {
	s.mu.Lock()
	s.behavior = b
	s.mu.Unlock()
}

// Downloads returns the recorded download requests.
func (s *SpeedService) Downloads() []DownloadRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]DownloadRecord(nil), s.downloads...)
}

// Uploads returns the recorded upload requests.
func (s *SpeedService) Uploads() []UploadRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]UploadRecord(nil), s.uploads...)
}

func (s *SpeedService) current() SpeedBehavior {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.behavior
}

// Serve handles one request, so the speed endpoints can be mounted on another server.
func (s *SpeedService) Serve(w http.ResponseWriter, r *http.Request) { s.serve(w, r) }

func (s *SpeedService) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/__down" && r.Method == http.MethodGet:
		s.download(w, r)
	case r.URL.Path == "/__up" && r.Method == http.MethodPost:
		s.upload(w, r)
	default:
		http.NotFound(w, r)
	}
}

var chunk = func() []byte {
	b := make([]byte, 32<<10)
	for i := range b {
		b[i] = byte(i*131 + 17)
	}
	return b
}()

func (s *SpeedService) download(w http.ResponseWriter, r *http.Request) {
	b := s.current()
	n, err := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
	rec := DownloadRecord{Requested: n, AcceptEncoding: r.Header.Get("Accept-Encoding")}
	if st := b.DownloadStatus; st != 0 {
		if b.RetryAfter != "" {
			w.Header().Set("Retry-After", b.RetryAfter)
		}
		w.WriteHeader(st)
		s.recordDownload(rec)
		return
	}
	if err != nil || n < 0 {
		http.Error(w, "bad bytes", http.StatusBadRequest)
		return
	}
	total := n + b.ContentLengthDelta
	toSend := total - b.DownloadShortBy + b.DownloadExtra
	if !b.NoContentLength {
		w.Header().Set("Content-Length", strconv.FormatInt(total-b.DownloadShortBy, 10))
		toSend = total - b.DownloadShortBy
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	defer func() { s.recordDownload(rec) }()
	var sent int64
	for sent < toSend {
		if b.StallAfter > 0 && sent >= b.StallAfter {
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			select {
			case <-r.Context().Done():
			case <-s.quit:
			}
			rec.Sent = sent
			return
		}
		size := int64(len(chunk))
		if toSend-sent < size {
			size = toSend - sent
		}
		if b.StallAfter > 0 && sent+size > b.StallAfter {
			size = b.StallAfter - sent
		}
		wn, werr := w.Write(chunk[:size])
		sent += int64(wn)
		rec.Sent = sent
		if werr != nil {
			return
		}
		// Always pause briefly after the first chunk so even tiny samples span
		// more than the coarse timer resolution of some platforms.
		delay := b.ChunkDelay
		if delay == 0 && sent == size {
			delay = time.Millisecond
		}
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
	}
	rec.Sent = sent
}

func (s *SpeedService) recordDownload(rec DownloadRecord) {
	s.mu.Lock()
	s.downloads = append(s.downloads, rec)
	s.mu.Unlock()
}

func (s *SpeedService) upload(w http.ResponseWriter, r *http.Request) {
	b := s.current()
	rec := UploadRecord{ContentLength: r.ContentLength, TransferEncoding: r.TransferEncoding, ContentEncoding: r.Header.Get("Content-Encoding")}
	defer func() {
		s.mu.Lock()
		s.uploads = append(s.uploads, rec)
		s.mu.Unlock()
	}()
	buf := make([]byte, 32<<10)
	for {
		if b.UploadStallAfter > 0 && rec.Received >= b.UploadStallAfter {
			// The server does not notice a client that left while the body is
			// unread, so also wait for fixture shutdown.
			select {
			case <-r.Context().Done():
			case <-s.quit:
			}
			return
		}
		n, err := r.Body.Read(buf)
		rec.Received += int64(n)
		delay := b.UploadReadDelay
		if delay == 0 && rec.Received == int64(n) {
			delay = time.Millisecond
		}
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return
		}
	}
	if !b.UploadNoHeader {
		w.Header().Set("cf-meta-upload-bytes", strconv.FormatInt(rec.Received+b.UploadReportDelta, 10))
	}
	if st := b.UploadStatus; st != 0 {
		if b.RetryAfter != "" {
			w.Header().Set("Retry-After", b.RetryAfter)
		}
		w.WriteHeader(st)
		return
	}
	w.WriteHeader(http.StatusOK)
}
