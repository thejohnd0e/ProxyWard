package proxy

import (
	"net"
	"strconv"
	"strings"
	"unicode"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
)

// MaxImportLines bounds the number of non-blank lines in one pasted list.
const MaxImportLines = 1000

const maxMaskedRunes = 200

// ImportRow is one non-blank line of a pasted list. Masked never contains
// passwords; for rejected lines it contains no credentials at all.
type ImportRow struct {
	Line      int // 1-based line number in the pasted text
	Masked    string
	Valid     bool
	Duplicate bool // repeats an earlier valid row of the same list
	ErrorCode string
}

// ParsedRow is an ImportRow together with the parsed proxy, which is only
// meaningful when Row.Valid is true. It carries credentials: keep it in memory.
type ParsedRow struct {
	Row   ImportRow
	Proxy domain.Proxy
}

// Preview parses every non-blank, non-comment line. Rows repeating an earlier
// valid row (same protocol, host, port and credentials) are marked Duplicate.
func Preview(text string, defaultProtocol domain.Protocol) []ImportRow {
	parsed := ParseList(text, defaultProtocol)
	rows := make([]ImportRow, len(parsed))
	for i, p := range parsed {
		rows[i] = p.Row
	}
	return rows
}

// ParseList is Preview that also returns the parsed proxies.
func ParseList(text string, defaultProtocol domain.Protocol) []ParsedRow {
	text = strings.TrimPrefix(text, string(rune(0xFEFF))) // byte order mark
	var out []ParsedRow
	seen := map[string]bool{}
	for i, raw := range strings.Split(text, string(rune(10))) {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		row := ImportRow{Line: i + 1}
		p, err := ParseLine(line, defaultProtocol)
		if err != nil {
			row.Masked = MaskLine(line)
			row.ErrorCode = CodeInvalidFormat
			if pe, ok := err.(*ParseError); ok {
				row.ErrorCode = pe.Code
			}
			out = append(out, ParsedRow{Row: row})
			continue
		}
		row.Valid = true
		row.Masked = Mask(p)
		key := IdentityKey(p)
		if seen[key] {
			row.Duplicate = true
		}
		seen[key] = true
		out = append(out, ParsedRow{Row: row, Proxy: p})
	}
	return out
}

// IdentityKey is an in-memory key for duplicate detection within one request.
// It contains the password and must never be stored or logged.
func IdentityKey(p domain.Proxy) string {
	user, pass := "", ""
	if p.Auth != nil {
		user, pass = p.Auth.Username, p.Auth.Password
	}
	return strings.Join([]string{string(p.Protocol), p.Host, strconv.Itoa(int(p.Port)), user, pass}, "\x00")
}

// Mask renders a proxy for display with the password hidden.
func Mask(p domain.Proxy) string {
	var b strings.Builder
	b.WriteString(string(p.Protocol))
	b.WriteString("://")
	if p.Auth != nil {
		b.WriteString(sanitize(p.Auth.Username))
		b.WriteString(":***@")
	}
	b.WriteString(net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))))
	return truncate(b.String())
}

// MaskLine redacts everything that could be a credential in a raw line:
// the whole user-info part before the last "@", and trailing fields of
// host:port:user:password style lines.
func MaskLine(line string) string {
	line = strings.TrimSpace(line)
	prefix, rest := "", line
	if i := strings.Index(line, "://"); i >= 0 {
		prefix, rest = line[:i+3], line[i+3:]
	}
	switch at := strings.LastIndex(rest, "@"); {
	case at >= 0:
		rest = "***@" + rest[at+1:]
	case !strings.HasPrefix(rest, "[") && strings.Count(rest, ":") > 1:
		rest = rest[:strings.Index(rest, ":")] + ":***"
	}
	return truncate(sanitize(prefix + rest))
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, s)
}

func truncate(s string) string {
	r := []rune(s)
	if len(r) > maxMaskedRunes {
		return string(r[:maxMaskedRunes-3]) + "..."
	}
	return s
}
