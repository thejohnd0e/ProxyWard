package proxy

import (
	"errors"
	"strings"
	"testing"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
)

func TestParseUnauthenticatedIPv6(t *testing.T) {
	p, err := ParseLine("socks5://[2001:db8::1]:1080", "http")
	if err != nil {
		t.Fatal(err)
	}
	if p.Auth != nil || p.Host != "2001:db8::1" || p.Port != 1080 || p.Protocol != "socks5" {
		t.Fatalf("unexpected endpoint: host=%s port=%d", p.Host, p.Port)
	}
}

func TestParseLineValid(t *testing.T) {
	type want struct {
		proto      domain.Protocol
		host       string
		port       uint16
		user, pass string
		auth       bool
	}
	for name, tc := range map[string]struct {
		line string
		def  domain.Protocol
		want want
	}{
		"http url":            {"http://10.0.0.1:3128", "socks5", want{"http", "10.0.0.1", 3128, "", "", false}},
		"https url":           {"https://proxy.example.com:8443", "http", want{"https", "proxy.example.com", 8443, "", "", false}},
		"socks5 url":          {"socks5://10.0.0.2:1080", "http", want{"socks5", "10.0.0.2", 1080, "", "", false}},
		"upper scheme":        {"SOCKS5://10.0.0.2:1080", "http", want{"socks5", "10.0.0.2", 1080, "", "", false}},
		"bare host port":      {"10.0.0.3:8080", "https", want{"https", "10.0.0.3", 8080, "", "", false}},
		"bare dns":            {"Proxy.Example.COM:80", "http", want{"http", "proxy.example.com", 80, "", "", false}},
		"trailing dot":        {"proxy.example.com.:80", "http", want{"http", "proxy.example.com", 80, "", "", false}},
		"bare ipv6":           {"[2001:DB8:0:0::1]:3128", "http", want{"http", "2001:db8::1", 3128, "", "", false}},
		"auth":                {"http://user:pass@10.0.0.1:3128", "socks5", want{"http", "10.0.0.1", 3128, "user", "pass", true}},
		"auth case preserved": {"socks5://UsEr:PaSs@host.lan:1080", "http", want{"socks5", "host.lan", 1080, "UsEr", "PaSs", true}},
		"empty password":      {"http://user:@10.0.0.1:3128", "http", want{"http", "10.0.0.1", 3128, "user", "", true}},
		"no password colon":   {"http://user@10.0.0.1:3128", "http", want{"http", "10.0.0.1", 3128, "user", "", true}},
		"percent encoded":     {"https://u%40x:p%3A%2F%40%23%25w@10.0.0.1:443", "http", want{"https", "10.0.0.1", 443, "u@x", "p:/@#%w", true}},
		"plus is literal":     {"http://a+b:c+d@10.0.0.1:80", "http", want{"http", "10.0.0.1", 80, "a+b", "c+d", true}},
		"auth ipv6":           {"socks5://u:p@[::1]:1080", "http", want{"socks5", "::1", 1080, "u", "p", true}},
		"trailing slash":      {"http://10.0.0.1:3128/", "http", want{"http", "10.0.0.1", 3128, "", "", false}},
		"surrounding spaces":  {"  http://10.0.0.1:3128\t", "http", want{"http", "10.0.0.1", 3128, "", "", false}},
		"min port":            {"10.0.0.1:1", "http", want{"http", "10.0.0.1", 1, "", "", false}},
		"max port":            {"10.0.0.1:65535", "http", want{"http", "10.0.0.1", 65535, "", "", false}},
	} {
		p, err := ParseLine(tc.line, tc.def)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		w := tc.want
		if p.Protocol != w.proto || p.Host != w.host || p.Port != w.port {
			t.Errorf("%s: got %s %s %d", name, p.Protocol, p.Host, p.Port)
		}
		if (p.Auth != nil) != w.auth {
			t.Errorf("%s: auth presence = %v", name, p.Auth != nil)
		} else if w.auth && (p.Auth.Username != w.user || p.Auth.Password != w.pass) {
			t.Errorf("%s: credentials mismatch", name)
		}
		if p.Name == "" {
			t.Errorf("%s: empty default name", name)
		}
	}
}

func TestParseLineFragmentNames(t *testing.T) {
	for name, tc := range map[string]struct {
		line string
		want string
	}{
		"fragment is display name": {"http://10.0.0.1:3128#proxy_US", "proxy_US"},
		"percent encoded name":     {"http://10.0.0.1:3128#Proxy%20US", "Proxy US"},
		"credentials and name":     {"http://alice:secret@10.0.0.1:3128#proxy_US", "proxy_US"},
		"plus remains literal":     {"http://10.0.0.1:3128#proxy+US", "proxy+US"},
		"no fragment uses address": {"http://10.0.0.1:3128", "10.0.0.1:3128"},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := ParseLine(tc.line, "http")
			if err != nil {
				t.Fatal(err)
			}
			if p.Name != tc.want {
				t.Fatalf("name = %q, want %q", p.Name, tc.want)
			}
		})
	}
}

func TestParseLineInvalidFragmentNames(t *testing.T) {
	for name, line := range map[string]string{
		"empty":              "http://10.0.0.1:80#",
		"malformed encoding": "http://10.0.0.1:80#bad%zz",
		"control character":  "http://10.0.0.1:80#bad%0Aname",
		"overlong":           "http://10.0.0.1:80#" + strings.Repeat("a", 101),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseLine(line, "http")
			var pe *ParseError
			if !errors.As(err, &pe) || pe.Code != "invalid_fragment_name" {
				t.Fatalf("error = %v, want ParseError code %q", err, "invalid_fragment_name")
			}
			if strings.Contains(err.Error(), line) {
				t.Fatal("parse error contains input text")
			}
		})
	}
}

func TestParseLineErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		line string
		code string
	}{
		"empty":                   {"", CodeEmpty},
		"blank":                   {"   ", CodeEmpty},
		"unknown scheme":          {"ftp://10.0.0.1:21", CodeInvalidScheme},
		"socks4":                  {"socks4://10.0.0.1:1080", CodeInvalidScheme},
		"empty scheme":            {"://10.0.0.1:80", CodeInvalidScheme},
		"missing port":            {"http://10.0.0.1", CodeInvalidPort},
		"bare missing port":       {"10.0.0.1", CodeInvalidPort},
		"port zero":               {"10.0.0.1:0", CodeInvalidPort},
		"port too large":          {"http://10.0.0.1:65536", CodeInvalidPort},
		"port not number":         {"http://10.0.0.1:http", CodeInvalidPort},
		"negative port":           {"http://10.0.0.1:-1", CodeInvalidPort},
		"empty port":              {"http://10.0.0.1:", CodeInvalidPort},
		"path":                    {"http://10.0.0.1:80/path", CodeUnexpectedPath},
		"query":                   {"http://10.0.0.1:80?x=1", CodeUnexpectedQuery},
		"query after slash":       {"http://10.0.0.1:80/?x=1", CodeUnexpectedQuery},
		"empty username":          {"http://:secret@10.0.0.1:80", CodeEmptyUsername},
		"empty userinfo":          {"http://@10.0.0.1:80", CodeEmptyUsername},
		"bad escape":              {"http://us%zzer:pw@10.0.0.1:80", CodeInvalidCredentials},
		"empty host":              {"http://:80", CodeInvalidHost},
		"no host at all":          {"http://", CodeInvalidHost},
		"bad ipv4":                {"http://256.1.1.1:80", CodeInvalidHost},
		"short ipv4":              {"http://1.2.3:80", CodeInvalidHost},
		"zone id":                 {"http://[fe80::1%25eth0]:80", CodeInvalidHost},
		"underscore leading dash": {"http://-bad.example:80", CodeInvalidHost},
		"space in host":           {"http://bad host:80", CodeInvalidURL},
		"non ascii host":          {"http://пример.рф:80", CodeInvalidHost},
		"ambiguous four fields":   {"10.0.0.1:8080:user:secret", CodeAmbiguousFormat},
		"bare ipv6 no brackets":   {"2001:db8::1:8080", CodeAmbiguousFormat},
		"bare userinfo":           {"user:pw@10.0.0.1:80", CodeInvalidFormat},
		"label too long":          {"http://" + strings.Repeat("a", 64) + ".example:80", CodeInvalidHost},
		"host too long":           {"http://" + strings.Repeat("abcdefgh.", 30) + "com:80", CodeInvalidHost},
	} {
		_, err := ParseLine(tc.line, "http")
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Errorf("%s: err = %v, want ParseError", name, err)
			continue
		}
		if pe.Code != tc.code {
			t.Errorf("%s: code = %s, want %s", name, pe.Code, tc.code)
		}
	}
}

func TestParseLineInvalidDefaultProtocol(t *testing.T) {
	var pe *ParseError
	if _, err := ParseLine("10.0.0.1:80", "ftp"); !errors.As(err, &pe) || pe.Code != CodeInvalidScheme {
		t.Fatalf("err = %v", err)
	}
	// An explicit scheme does not need a valid default.
	if _, err := ParseLine("http://10.0.0.1:80", ""); err != nil {
		t.Fatal(err)
	}
}

func TestParseErrorsNeverContainInput(t *testing.T) {
	for _, line := range []string{
		"http://user:SuperSecret1@bad host:80",
		"http://user:SuperSecret1@10.0.0.1:99999",
		"http://us%zzer:SuperSecret1@10.0.0.1:80",
		"10.0.0.1:8080:user:SuperSecret1",
		"http://user:SuperSecret1@10.0.0.1:80/x?y",
	} {
		_, err := ParseLine(line, "http")
		if err == nil {
			t.Fatalf("%q parsed", line)
		}
		if strings.Contains(err.Error(), "SuperSecret1") || strings.Contains(err.Error(), "user") {
			t.Errorf("error leaks input: %v", err)
		}
	}
}

func TestParseKeepsIdentityStableAcrossSpellings(t *testing.T) {
	a, _ := ParseLine("HTTP://Proxy.EXAMPLE.com.:3128", "socks5")
	b, _ := ParseLine("proxy.example.com:3128", "http")
	if a.Protocol != b.Protocol || a.Host != b.Host || a.Port != b.Port {
		t.Fatalf("%+v vs %+v", a, b)
	}
	c, _ := ParseLine("[0:0:0:0:0:0:0:1]:80", "http")
	d, _ := ParseLine("[::1]:80", "http")
	if c.Host != d.Host {
		t.Fatalf("ipv6 spellings differ: %s vs %s", c.Host, d.Host)
	}
}

func TestPreviewRedactsSecretsAndMarksRows(t *testing.T) {
	text := "\ufeff# comment\r\n" +
		"http://alice:TopSecret9@10.0.0.1:3128\r\n" +
		"\r\n" +
		"10.0.0.2:8080\n" +
		"10.0.0.2:8080\n" +
		"HTTP://alice:TopSecret9@10.0.0.1:3128\n" +
		"http://bob:Hunter2Hunter@bad host:80\n" +
		"10.0.0.9:80:carol:Hidden123\n" +
		"ftp://10.0.0.5:21\n"
	rows := Preview(text, domain.ProtocolHTTP)

	want := []struct {
		line  int
		valid bool
		dup   bool
		code  string
	}{
		{2, true, false, ""},
		{4, true, false, ""},
		{5, true, true, ""},
		{6, true, true, ""},
		{7, false, false, CodeInvalidURL},
		{8, false, false, CodeAmbiguousFormat},
		{9, false, false, CodeInvalidScheme},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %d, want %d: %+v", len(rows), len(want), rows)
	}
	for i, w := range want {
		r := rows[i]
		if r.Line != w.line || r.Valid != w.valid || r.Duplicate != w.dup || r.ErrorCode != w.code {
			t.Errorf("row %d: %+v, want %+v", i, r, w)
		}
	}
	for _, r := range rows {
		for _, secret := range []string{"TopSecret9", "Hunter2Hunter", "Hidden123", "bob", "carol"} {
			if strings.Contains(r.Masked, secret) {
				t.Errorf("line %d masked text leaks %q: %s", r.Line, secret, r.Masked)
			}
		}
		if r.Masked == "" {
			t.Errorf("line %d has no masked text", r.Line)
		}
	}
	if !strings.Contains(rows[0].Masked, "10.0.0.1:3128") || !strings.Contains(rows[0].Masked, "alice") {
		t.Errorf("valid row should show scheme, user and endpoint: %s", rows[0].Masked)
	}
}

func TestPreviewDifferentCredentialsAreNotDuplicates(t *testing.T) {
	rows := Preview("http://a:1@10.0.0.1:80\nhttp://a:2@10.0.0.1:80\nhttp://b:1@10.0.0.1:80\n", "http")
	for _, r := range rows {
		if r.Duplicate || !r.Valid {
			t.Errorf("line %d: %+v", r.Line, r)
		}
	}
}

func TestMaskLineRedactsUserinfoAndTruncates(t *testing.T) {
	for line, forbidden := range map[string][]string{
		"http://u:pw@h:1":                      {"pw", "u:"},
		"u:pw@h:1":                             {"pw"},
		"h:1:user:pw":                          {"user", "pw"},
		"http://a@b@h:1":                       {"a@"},
		"socks5://" + strings.Repeat("x", 500): {},
	} {
		m := MaskLine(line)
		for _, f := range forbidden {
			if strings.Contains(m, f) {
				t.Errorf("MaskLine(%q) = %q contains %q", line, m, f)
			}
		}
		if len([]rune(m)) > 200 {
			t.Errorf("MaskLine output not truncated: %d", len(m))
		}
	}
}

func TestNormalizeHost(t *testing.T) {
	for in, want := range map[string]string{
		"Example.COM":   "example.com",
		"[::1]":         "::1",
		"2001:DB8::1":   "2001:db8::1",
		"10.1.2.3":      "10.1.2.3",
		"host_name.lan": "host_name.lan",
		"a.b.c.":        "a.b.c",
	} {
		got, err := NormalizeHost(in)
		if err != nil || got != want {
			t.Errorf("NormalizeHost(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", " ", "a b", "http://x", "a/b", "1.2.3", "300.1.1.1", "fe80::1%eth0", "-x", "x-", "a..b", "host:80"} {
		if _, err := NormalizeHost(bad); err == nil {
			t.Errorf("NormalizeHost(%q) accepted", bad)
		}
	}
}

func TestValidateNameAndTags(t *testing.T) {
	if n, err := ValidateName("  Office proxy  "); err != nil || n != "Office proxy" {
		t.Fatalf("name: %q %v", n, err)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("a", 101), "bad\x00name", "line\nbreak"} {
		if _, err := ValidateName(bad); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
	tags, err := ValidateTags([]string{" eu ", "EU", "прокси", "work"})
	if err != nil || len(tags) != 3 || tags[0] != "eu" || tags[1] != "прокси" {
		t.Fatalf("tags: %v %v", tags, err)
	}
	if tags, err := ValidateTags(nil); err != nil || tags == nil || len(tags) != 0 {
		t.Fatalf("nil tags: %#v %v", tags, err)
	}
	many := make([]string, 11)
	for i := range many {
		many[i] = strings.Repeat("t", i+1)
	}
	for name, bad := range map[string][]string{
		"too many": many,
		"empty":    {"ok", " "},
		"long":     {strings.Repeat("a", 33)},
		"control":  {"a\tb"},
	} {
		if _, err := ValidateTags(bad); err == nil {
			t.Errorf("%s tags accepted", name)
		}
	}
}
