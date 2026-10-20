package testkit

import (
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

// SocksProxy is a fixture SOCKS5 proxy supporting no authentication and
// username/password authentication (RFC 1929).
type SocksProxy struct{ *base }

// NewSocksProxy starts a SOCKS5 proxy. ProxyOptions.CA is ignored.
func NewSocksProxy(t testing.TB, opts ProxyOptions) *SocksProxy {
	t.Helper()
	p := &SocksProxy{newBase(t, opts, false)}
	p.serve(p.handle)
	t.Cleanup(p.Close)
	return p
}

func (p *SocksProxy) handle(c net.Conn) {
	c.SetDeadline(time.Now().Add(10 * time.Second))
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(c, hdr); err != nil || hdr[0] != 5 {
		return
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return
	}
	has := func(m byte) bool {
		for _, x := range methods {
			if x == m {
				return true
			}
		}
		return false
	}
	authorized := true
	switch {
	case p.opts.Username != "":
		if !has(2) {
			c.Write([]byte{5, 0xFF})
			return
		}
		c.Write([]byte{5, 2})
		ok, err := p.readUserPass(c)
		if err != nil {
			return
		}
		authorized = ok
		status := byte(0)
		if !ok {
			status = 1
		}
		c.Write([]byte{1, status})
		if !ok {
			p.record(ProxyRequest{Authorized: false})
			return
		}
	case has(0):
		c.Write([]byte{5, 0})
	default:
		c.Write([]byte{5, 0xFF})
		return
	}

	req := make([]byte, 4)
	if _, err := io.ReadFull(c, req); err != nil || req[0] != 5 || req[1] != 1 {
		return
	}
	var host string
	isDomain := false
	switch req[3] {
	case 1:
		b := make([]byte, 4)
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		host = net.IP(b).String()
	case 4:
		b := make([]byte, 16)
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		host = net.IP(b).String()
	case 3:
		l := make([]byte, 1)
		if _, err := io.ReadFull(c, l); err != nil {
			return
		}
		b := make([]byte, l[0])
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		host, isDomain = string(b), true
	default:
		return
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(c, pb); err != nil {
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb))))
	p.record(ProxyRequest{Target: target, AtypDomain: isDomain, Authorized: authorized})

	reply := func(code byte) { c.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0}) }
	if code := p.opts.ReplyCode; code != 0 {
		reply(code)
		return
	}
	up, err := p.dialTarget(target)
	if err != nil {
		reply(5)
		return
	}
	defer up.Close()
	reply(0)
	c.SetDeadline(time.Time{})
	pipe(c, up)
}

func (p *SocksProxy) readUserPass(c net.Conn) (bool, error) {
	v := make([]byte, 2)
	if _, err := io.ReadFull(c, v); err != nil || v[0] != 1 {
		return false, io.ErrUnexpectedEOF
	}
	user := make([]byte, v[1])
	if _, err := io.ReadFull(c, user); err != nil {
		return false, err
	}
	l := make([]byte, 1)
	if _, err := io.ReadFull(c, l); err != nil {
		return false, err
	}
	pass := make([]byte, l[0])
	if _, err := io.ReadFull(c, pass); err != nil {
		return false, err
	}
	return string(user) == p.opts.Username && string(pass) == p.opts.Password, nil
}
