package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"proxy-checker/internal/domain"
)

// Stage tells where in the proxy path a failure happened. Failures at these
// stages involve only the proxy, never the public target service.
type Stage int

const (
	// StageConnect is opening the TCP connection to the proxy.
	StageConnect Stage = iota + 1
	// StageProxyTLS is the TLS handshake with an HTTPS proxy.
	StageProxyTLS
	// StageTunnel is the CONNECT or SOCKS5 negotiation.
	StageTunnel
)

func (s Stage) String() string {
	switch s {
	case StageConnect:
		return "connect"
	case StageProxyTLS:
		return "proxy TLS"
	case StageTunnel:
		return "tunnel"
	}
	return "unknown"
}

// StageError wraps a failure that happened while reaching a target through a
// proxy, before any byte of the target conversation was exchanged.
type StageError struct {
	Stage Stage
	Err   error
}

func (e *StageError) Error() string { return "proxy " + e.Stage.String() + ": " + e.Err.Error() }
func (e *StageError) Unwrap() error { return e.Err }

// Proxy-side failures reported by the dialers. They never contain credentials.
var (
	ErrAuthRequired  = errors.New("proxy requires authentication")
	ErrAuthFailed    = errors.New("proxy rejected the credentials")
	ErrTunnelFailed  = errors.New("proxy could not open the tunnel")
	ErrProxyProtocol = errors.New("proxy sent an invalid reply")
)

// TunnelError carries the HTTP status or SOCKS5 reply code of a refused tunnel.
type TunnelError struct{ Code int }

func (e *TunnelError) Error() string {
	return "proxy could not open the tunnel (code " + strconv.Itoa(e.Code) + ")"
}
func (e *TunnelError) Is(target error) bool { return target == ErrTunnelFailed }

// Option customizes NewClient.
type Option func(*dialer)

// WithRootCAs sets the trusted roots for both the proxy (HTTPS proxies) and
// target TLS verification. It exists for tests with local certificate
// authorities; verification itself is never disabled.
func WithRootCAs(pool *x509.CertPool) Option { return func(d *dialer) { d.roots = pool } }

const maxReplyBytes = 64 << 10

type dialer struct {
	proxy     domain.Proxy
	proxyAddr string
	timeout   time.Duration
	roots     *x509.CertPool
}

// NewClient returns an HTTP client that sends every request through p and
// nowhere else. There is no direct fallback: if the proxy cannot be used the
// request fails. Environment proxy variables, redirects and compression are
// disabled, and certificates of the proxy (for HTTPS proxies) and of the
// target are always verified.
func NewClient(p domain.Proxy, timeout time.Duration, opts ...Option) (*http.Client, error) {
	if timeout <= 0 {
		return nil, errors.New("timeout must be positive")
	}
	if !p.Protocol.Valid() || strings.TrimSpace(p.Host) == "" || p.Port == 0 {
		return nil, errors.New("invalid proxy endpoint")
	}
	if p.Auth != nil {
		if p.Auth.Username == "" {
			return nil, errors.New("proxy username must not be empty")
		}
		if p.Protocol == domain.ProtocolSOCKS5 && (len(p.Auth.Username) > 255 || len(p.Auth.Password) > 255) {
			return nil, errors.New("SOCKS5 credentials are limited to 255 bytes")
		}
	}
	d := &dialer{
		proxy:     p,
		proxyAddr: net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))),
		timeout:   timeout,
	}
	for _, o := range opts {
		o(d)
	}
	tr := &http.Transport{
		Proxy:                  nil, // never consult HTTP_PROXY, HTTPS_PROXY or ALL_PROXY
		DialContext:            d.dialTarget,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: d.roots},
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		MaxResponseHeaderBytes: 16 << 10,
		DisableKeepAlives:      true,
		DisableCompression:     true,
		ForceAttemptHTTP2:      false,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

// dialTarget opens a tunnel to addr (host:port) through the proxy. It is the
// only way the transport creates connections.
func (d *dialer) dialTarget(ctx context.Context, network, addr string) (net.Conn, error) {
	if !strings.HasPrefix(network, "tcp") {
		return nil, fmt.Errorf("unsupported network %q", network)
	}
	nd := net.Dialer{Timeout: d.timeout}
	conn, err := nd.DialContext(ctx, "tcp", d.proxyAddr)
	if err != nil {
		return nil, &StageError{Stage: StageConnect, Err: err}
	}
	if d.proxy.Protocol == domain.ProtocolHTTPS {
		tc := tls.Client(conn, &tls.Config{ServerName: d.proxy.Host, RootCAs: d.roots, MinVersion: tls.VersionTLS12})
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, &StageError{Stage: StageProxyTLS, Err: err}
		}
		conn = tc
	}
	var tunnel net.Conn
	if d.proxy.Protocol == domain.ProtocolSOCKS5 {
		tunnel, err = socks5Connect(ctx, conn, addr, d.proxy.Auth)
	} else {
		tunnel, err = connectTunnel(ctx, conn, addr, d.proxy.Auth)
	}
	if err != nil {
		conn.Close()
		return nil, &StageError{Stage: StageTunnel, Err: err}
	}
	return tunnel, nil
}

// guard applies ctx's deadline to conn and aborts blocked I/O when ctx ends.
// The returned function stops the watcher and clears the deadline.
func guard(ctx context.Context, conn net.Conn) (release func()) {
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		select {
		case <-ctx.Done():
			_ = conn.SetDeadline(time.Unix(1, 0))
		case <-stop:
		}
	}()
	return func() {
		close(stop)
		<-finished
		_ = conn.SetDeadline(time.Time{})
	}
}

// ioFailure prefers the context's error (canceled or deadline exceeded) over
// the I/O error that its deadline caused.
func ioFailure(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

// connectTunnel sends an HTTP CONNECT request on conn and returns a
// connection positioned right after the proxy's reply. Bytes the proxy sent
// after its reply headers are preserved.
func connectTunnel(ctx context.Context, conn net.Conn, target string, creds *domain.Credentials) (net.Conn, error) {
	release := guard(ctx, conn)
	defer release()

	var req strings.Builder
	req.WriteString("CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\nUser-Agent: proxy-checker\r\n")
	if creds != nil {
		token := base64.StdEncoding.EncodeToString([]byte(creds.Username + ":" + creds.Password))
		req.WriteString("Proxy-Authorization: Basic " + token + "\r\n")
	}
	req.WriteString("\r\n")
	if _, err := io.WriteString(conn, req.String()); err != nil {
		return nil, ioFailure(ctx, err)
	}

	br := bufio.NewReaderSize(io.LimitReader(conn, maxReplyBytes), 4096)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) || ctx.Err() != nil {
			return nil, ioFailure(ctx, err)
		}
		return nil, ErrProxyProtocol
	}
	resp.Body.Close()

	switch code := resp.StatusCode; {
	case code >= 200 && code < 300:
	case code == http.StatusUnauthorized || code == http.StatusForbidden || code == http.StatusProxyAuthRequired:
		if creds == nil {
			return nil, ErrAuthRequired
		}
		return nil, ErrAuthFailed
	default:
		return nil, &TunnelError{Code: code}
	}
	if br.Buffered() == 0 {
		return conn, nil
	}
	return &bufferedConn{Conn: conn, r: io.MultiReader(io.LimitReader(br, int64(br.Buffered())), conn)}, nil
}

// bufferedConn serves bytes that were read ahead before reading from the connection.
type bufferedConn struct {
	net.Conn
	r io.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
