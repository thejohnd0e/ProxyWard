// Package proxy parses and validates proxy endpoints and import lists.
package proxy

import (
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
)

// Parse error codes. They are stable identifiers shown in import previews.
const (
	CodeEmpty              = "empty"
	CodeInvalidScheme      = "invalid_scheme"
	CodeInvalidFormat      = "invalid_format"
	CodeAmbiguousFormat    = "ambiguous_format"
	CodeInvalidURL         = "invalid_url"
	CodeInvalidHost        = "invalid_host"
	CodeInvalidPort        = "invalid_port"
	CodeUnexpectedPath     = "unexpected_path"
	CodeUnexpectedQuery    = "unexpected_query"
	CodeUnexpectedFragment = "unexpected_fragment"
	CodeEmptyUsername      = "empty_username"
	CodeInvalidCredentials = "invalid_credentials_encoding"
)

// ParseError reports why a line was rejected. It never contains input text, so
// it cannot leak credentials.
type ParseError struct{ Code string }

func (e *ParseError) Error() string { return "invalid proxy line: " + e.Code }

func fail(code string) error { return &ParseError{Code: code} }

// ParseLine parses scheme://user:password@host:port, scheme://host:port or
// host:port (using defaultProtocol). Credentials are percent-decoded. The host
// is normalized (lowercase DNS names, canonical IP text) while credentials keep
// their case. Formats such as host:port:user:password are rejected rather than
// guessed.
func ParseLine(line string, defaultProtocol domain.Protocol) (domain.Proxy, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return domain.Proxy{}, fail(CodeEmpty)
	}

	var (
		proto domain.Protocol
		rest  string
	)
	if i := strings.Index(line, "://"); i >= 0 {
		proto = domain.Protocol(strings.ToLower(line[:i]))
		if !proto.Valid() {
			return domain.Proxy{}, fail(CodeInvalidScheme)
		}
		rest = line[i+3:]
	} else {
		if !defaultProtocol.Valid() {
			return domain.Proxy{}, fail(CodeInvalidScheme)
		}
		proto, rest = defaultProtocol, line
		if strings.Contains(rest, "@") {
			return domain.Proxy{}, fail(CodeInvalidFormat)
		}
		if !strings.HasPrefix(rest, "[") {
			switch n := strings.Count(rest, ":"); {
			case n == 0:
				return domain.Proxy{}, fail(CodeInvalidPort)
			case n > 1:
				return domain.Proxy{}, fail(CodeAmbiguousFormat)
			}
		}
	}

	switch {
	case rest == "":
		return domain.Proxy{}, fail(CodeInvalidHost)
	case strings.ContainsAny(rest, " \t\\"):
		return domain.Proxy{}, fail(CodeInvalidURL)
	case strings.Contains(rest, "?"):
		return domain.Proxy{}, fail(CodeUnexpectedQuery)
	case strings.Contains(rest, "#"):
		return domain.Proxy{}, fail(CodeUnexpectedFragment)
	}

	u, err := url.Parse("//" + rest)
	if err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "port"):
			return domain.Proxy{}, fail(CodeInvalidPort)
		case strings.Contains(msg, "escape"):
			return domain.Proxy{}, fail(CodeInvalidCredentials)
		}
		return domain.Proxy{}, fail(CodeInvalidURL)
	}
	if u.Path != "" && u.Path != "/" {
		return domain.Proxy{}, fail(CodeUnexpectedPath)
	}

	rawHost := u.Hostname()
	if rawHost == "" {
		return domain.Proxy{}, fail(CodeInvalidHost)
	}
	if u.Port() == "" {
		return domain.Proxy{}, fail(CodeInvalidPort)
	}
	port, err := strconv.ParseUint(u.Port(), 10, 32)
	if err != nil || port < 1 || port > 65535 {
		return domain.Proxy{}, fail(CodeInvalidPort)
	}
	host, err := NormalizeHost(rawHost)
	if err != nil {
		return domain.Proxy{}, fail(CodeInvalidHost)
	}

	p := domain.Proxy{
		Name:     net.JoinHostPort(host, strconv.FormatUint(port, 10)),
		Protocol: proto,
		Host:     host,
		Port:     uint16(port),
	}
	if u.User != nil {
		name := u.User.Username()
		if name == "" {
			return domain.Proxy{}, fail(CodeEmptyUsername)
		}
		password, _ := u.User.Password()
		p.Auth = &domain.Credentials{Username: name, Password: password}
	}
	return p, nil
}

// ErrInvalidHost is returned by NormalizeHost.
var ErrInvalidHost = errors.New("invalid host")

// NormalizeHost validates a hostname or IP literal (optionally bracketed) and
// returns its canonical form: lowercase DNS name without a trailing dot, or
// canonical IP text without brackets.
func NormalizeHost(h string) (string, error) {
	h = strings.TrimSpace(h)
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1]
	}
	if h == "" {
		return "", ErrInvalidHost
	}
	if addr, err := netip.ParseAddr(h); err == nil {
		if addr.Zone() != "" {
			return "", ErrInvalidHost
		}
		return addr.String(), nil
	}
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if h == "" || len(h) > 253 {
		return "", ErrInvalidHost
	}
	numeric := true
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", ErrInvalidHost
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			switch {
			case c >= '0' && c <= '9':
			case c >= 'a' && c <= 'z', c == '-', c == '_':
				numeric = false
			default:
				return "", ErrInvalidHost
			}
		}
	}
	// Digits and dots that failed IP parsing (256.1.1.1, 1.2.3) are not names.
	if numeric {
		return "", ErrInvalidHost
	}
	return h, nil
}

const (
	maxNameRunes = 100
	maxTagRunes  = 32
	maxTags      = 10
)

// ValidateName trims and validates a display name.
func ValidateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	n := utf8.RuneCountInString(name)
	if n == 0 || n > maxNameRunes || hasControl(name) {
		return "", errors.New("name must be 1 to 100 characters without control characters")
	}
	return name, nil
}

// ValidateTags trims tags, drops case-insensitive duplicates and enforces limits.
func ValidateTags(tags []string) ([]string, error) {
	if len(tags) > maxTags {
		return nil, errors.New("at most 10 tags are allowed")
	}
	out := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.TrimSpace(t)
		n := utf8.RuneCountInString(t)
		if n == 0 || n > maxTagRunes || hasControl(t) {
			return nil, errors.New("each tag must be 1 to 32 characters without control characters")
		}
		key := strings.ToLower(t)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	return out, nil
}

func hasControl(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}
