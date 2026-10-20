package proxy

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"

	"proxy-checker/internal/domain"
)

// SOCKS5 constants (RFC 1928, RFC 1929).
const (
	socksVersion      = 5
	socksAuthVersion  = 1
	methodNoAuth      = 0x00
	methodUserPass    = 0x02
	methodUnacceptabl = 0xFF
	cmdConnect        = 0x01
	atypIPv4          = 0x01
	atypDomain        = 0x03
	atypIPv6          = 0x04
)

// socks5Connect performs the SOCKS5 handshake on conn and asks the proxy to
// connect to target. Hostnames are sent to the proxy unresolved, so DNS
// resolution happens remotely. Exactly the reply bytes are consumed.
func socks5Connect(ctx context.Context, conn net.Conn, target string, creds *domain.Credentials) (net.Conn, error) {
	release := guard(ctx, conn)
	defer release()
	fail := func(err error) (net.Conn, error) { return nil, ioFailure(ctx, err) }

	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return nil, err
	}

	methods := []byte{methodNoAuth}
	if creds != nil {
		methods = []byte{methodNoAuth, methodUserPass}
	}
	if _, err := conn.Write(append([]byte{socksVersion, byte(len(methods))}, methods...)); err != nil {
		return fail(err)
	}
	var choice [2]byte
	if _, err := io.ReadFull(conn, choice[:]); err != nil {
		return fail(protocolOr(err))
	}
	if choice[0] != socksVersion {
		return nil, ErrProxyProtocol
	}
	switch choice[1] {
	case methodNoAuth:
	case methodUserPass:
		if creds == nil {
			return nil, ErrAuthRequired
		}
		if err := socksUserPass(conn, creds); err != nil {
			return fail(err)
		}
	case methodUnacceptabl:
		if creds == nil {
			return nil, ErrAuthRequired
		}
		return nil, ErrAuthFailed
	default:
		return nil, ErrProxyProtocol
	}

	req := []byte{socksVersion, cmdConnect, 0}
	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.Is4() {
			a := addr.As4()
			req = append(req, atypIPv4)
			req = append(req, a[:]...)
		} else {
			a := addr.As16()
			req = append(req, atypIPv6)
			req = append(req, a[:]...)
		}
	} else {
		if len(host) == 0 || len(host) > 255 {
			return nil, errors.New("invalid target host")
		}
		req = append(req, atypDomain, byte(len(host)))
		req = append(req, host...)
	}
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if _, err := conn.Write(req); err != nil {
		return fail(err)
	}

	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return fail(protocolOr(err))
	}
	if head[0] != socksVersion {
		return nil, ErrProxyProtocol
	}
	if rep := head[1]; rep != 0 {
		if rep == 0x02 { // connection not allowed by ruleset
			return nil, ErrAuthFailed
		}
		return nil, &TunnelError{Code: int(rep)}
	}
	var skip int
	switch head[3] {
	case atypIPv4:
		skip = 4 + 2
	case atypIPv6:
		skip = 16 + 2
	case atypDomain:
		var l [1]byte
		if _, err := io.ReadFull(conn, l[:]); err != nil {
			return fail(protocolOr(err))
		}
		skip = int(l[0]) + 2
	default:
		return nil, ErrProxyProtocol
	}
	if _, err := io.CopyN(io.Discard, conn, int64(skip)); err != nil {
		return fail(protocolOr(err))
	}
	return conn, nil
}

func socksUserPass(conn net.Conn, creds *domain.Credentials) error {
	msg := []byte{socksAuthVersion, byte(len(creds.Username))}
	msg = append(msg, creds.Username...)
	msg = append(msg, byte(len(creds.Password)))
	msg = append(msg, creds.Password...)
	if _, err := conn.Write(msg); err != nil {
		return err
	}
	var resp [2]byte
	if _, err := io.ReadFull(conn, resp[:]); err != nil {
		return protocolOr(err)
	}
	if resp[0] != socksAuthVersion {
		return ErrProxyProtocol
	}
	if resp[1] != 0 {
		return ErrAuthFailed
	}
	return nil
}

// protocolOr maps a premature end of stream to ErrProxyProtocol and keeps
// network errors (timeouts, resets) as they are.
func protocolOr(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return ErrProxyProtocol
	}
	return err
}
