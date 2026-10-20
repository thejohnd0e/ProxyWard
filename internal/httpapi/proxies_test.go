package httpapi

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"proxy-checker/internal/auth"
)

type session struct {
	e      *env
	cookie string
	csrf   string
}

func newSession(t *testing.T) *session {
	t.Helper()
	e := newEnv(t, true)
	cookie, csrf, r := e.login(adminPassword)
	if r.status != 200 {
		t.Fatalf("login: %d %s", r.status, r.body)
	}
	return &session{e: e, cookie: cookie, csrf: csrf}
}

// call sends a JSON request (body may be a string for raw JSON, or any value).
func (s *session) call(method, path string, body any) resp {
	s.e.t.Helper()
	var raw string
	switch b := body.(type) {
	case nil:
	case string:
		raw = b
	default:
		out, err := json.Marshal(b)
		if err != nil {
			s.e.t.Fatal(err)
		}
		raw = string(out)
	}
	return s.e.do(method, path, raw, map[string]string{auth.CSRFHeader: s.csrf}, s.cookie)
}

type m = map[string]any

func decode(t *testing.T, r resp) m {
	t.Helper()
	var out m
	if err := json.Unmarshal([]byte(r.body), &out); err != nil {
		t.Fatalf("bad JSON %q: %v", r.body, err)
	}
	return out
}

func (s *session) create(body any) m {
	s.e.t.Helper()
	r := s.call("POST", "/api/proxies", body)
	if r.status != 201 {
		s.e.t.Fatalf("create: %d %s", r.status, r.body)
	}
	return decode(s.e.t, r)
}

func idPath(p m) string { return fmt.Sprintf("/api/proxies/%d", int64(p["id"].(float64))) }

func fieldError(t *testing.T, r resp, status int, code, field string) {
	t.Helper()
	if r.status != status || !strings.Contains(r.body, `"code":"`+code+`"`) {
		t.Fatalf("want %d %s, got %d %s", status, code, r.status, r.body)
	}
	if field != "" {
		fields, _ := decode(t, r)["error"].(m)["fields"].(m)
		if _, ok := fields[field]; !ok {
			t.Fatalf("no error for field %q: %s", field, r.body)
		}
	}
}

func TestProxyRoutesRequireAuthenticationAndCSRF(t *testing.T) {
	s := newSession(t)
	for _, tc := range [][2]string{
		{"GET", "/api/proxies"}, {"POST", "/api/proxies"}, {"GET", "/api/proxies/1"},
		{"PUT", "/api/proxies/1"}, {"DELETE", "/api/proxies/1"},
		{"POST", "/api/proxies/import/preview"}, {"POST", "/api/proxies/import"},
	} {
		if r := s.e.do(tc[0], tc[1], "{}", nil, ""); r.status != 401 {
			t.Errorf("%s %s without session: %d", tc[0], tc[1], r.status)
		}
		if tc[0] == "GET" {
			continue
		}
		if r := s.e.do(tc[0], tc[1], "{}", nil, s.cookie); r.status != 403 {
			t.Errorf("%s %s without CSRF: %d", tc[0], tc[1], r.status)
		}
	}
}

func TestCreateAndReadProxiesNeverExposePasswords(t *testing.T) {
	s := newSession(t)
	plain := s.create(m{"name": "Plain", "protocol": "http", "host": "Proxy.LAN", "port": 3128, "tags": []string{"lab", "eu"}})
	if plain["host"] != "proxy.lan" || plain["auth_enabled"] != false || plain["has_password"] != false ||
		plain["availability_schedule"] != nil || plain["speed_schedule"] != nil {
		t.Fatalf("plain DTO: %v", plain)
	}
	authd := s.create(m{"name": "Auth", "protocol": "socks5", "host": "10.0.0.5", "port": 1080,
		"auth_enabled": true, "username": "alice", "password": "Sup3rSecret!"})
	if authd["auth_enabled"] != true || authd["username"] != "alice" || authd["has_password"] != true {
		t.Fatalf("auth DTO: %v", authd)
	}
	for _, path := range []string{"/api/proxies", idPath(authd)} {
		r := s.call("GET", path, nil)
		if r.status != 200 {
			t.Fatalf("GET %s: %d", path, r.status)
		}
	}
	list := decode(t, s.call("GET", "/api/proxies", nil))
	if got := len(list["proxies"].([]any)); got != 2 || list["limit"] != float64(100) {
		t.Fatalf("list: %v", list)
	}
	for _, b := range s.e.bodies {
		if strings.Contains(b, "Sup3rSecret!") {
			t.Fatalf("a response contains the password: %s", b)
		}
	}
	if strings.Contains(s.e.logs.String(), "Sup3rSecret!") {
		t.Fatal("logs contain the password")
	}
}

func TestCreateDefaultsNameAndEmptyPassword(t *testing.T) {
	s := newSession(t)
	p := s.create(m{"protocol": "https", "host": "[2001:DB8::1]", "port": 8443, "auth_enabled": true, "username": "u"})
	if p["name"] != "[2001:db8::1]:8443" || p["host"] != "2001:db8::1" || p["has_password"] != false {
		t.Fatalf("defaults: %v", p)
	}
}

func TestCreateValidation(t *testing.T) {
	s := newSession(t)
	base := func(over m) m {
		b := m{"name": "n", "protocol": "http", "host": "h.lan", "port": 80}
		for k, v := range over {
			if v == "__delete__" {
				delete(b, k)
			} else {
				b[k] = v
			}
		}
		return b
	}
	long := strings.Repeat("n", 101)
	for name, tc := range map[string]struct {
		body  m
		field string
	}{
		"missing protocol":   {base(m{"protocol": "__delete__"}), "protocol"},
		"bad protocol":       {base(m{"protocol": "ftp"}), "protocol"},
		"missing host":       {base(m{"host": "__delete__"}), "host"},
		"bad host":           {base(m{"host": "bad host"}), "host"},
		"host with scheme":   {base(m{"host": "http://h.lan"}), "host"},
		"missing port":       {base(m{"port": "__delete__"}), "port"},
		"port zero":          {base(m{"port": 0}), "port"},
		"port too large":     {base(m{"port": 65536}), "port"},
		"long name":          {base(m{"name": long}), "name"},
		"bad tag":            {base(m{"tags": []string{" "}}), "tags"},
		"auth without user":  {base(m{"auth_enabled": true}), "username"},
		"auth empty user":    {base(m{"auth_enabled": true, "username": ""}), "username"},
		"null password":      {base(m{"auth_enabled": true, "username": "u", "password": nil}), "password"},
		"creds without auth": {base(m{"username": "u"}), "username"},
		"schedule no every":  {base(m{"speed_schedule": m{"enabled": true}}), "speed_schedule"},
		"schedule too short": {base(m{"availability_schedule": m{"enabled": true, "interval_minutes": 0}}), "availability_schedule"},
		"schedule too long":  {base(m{"availability_schedule": m{"enabled": true, "interval_minutes": 10081}}), "availability_schedule"},
	} {
		r := s.call("POST", "/api/proxies", tc.body)
		if r.status != 422 {
			t.Errorf("%s: status %d %s", name, r.status, r.body)
			continue
		}
		fieldError(t, r, 422, "validation_failed", tc.field)
	}
	for name, body := range map[string]string{
		"not json":      "nope",
		"unknown field": `{"protocol":"http","host":"h","port":1,"bogus":1}`,
		"port string":   `{"protocol":"http","host":"h","port":"80"}`,
	} {
		if r := s.call("POST", "/api/proxies", body); r.status != 400 {
			t.Errorf("%s: status %d", name, r.status)
		}
	}
	if n := len(decode(t, s.call("GET", "/api/proxies", nil))["proxies"].([]any)); n != 0 {
		t.Fatalf("rejected requests created %d proxies", n)
	}
}

func TestDuplicatesReturnConflict(t *testing.T) {
	s := newSession(t)
	body := m{"protocol": "http", "host": "dup.lan", "port": 8080, "auth_enabled": true, "username": "u", "password": "one"}
	s.create(body)
	r := s.call("POST", "/api/proxies", body)
	fieldError(t, r, 409, "duplicate", "")
	if strings.Contains(r.body, "one") && strings.Contains(r.body, "password") {
		t.Fatalf("conflict response leaks input: %s", r.body)
	}
	body["password"] = "two"
	s.create(body)
	s.create(m{"protocol": "http", "host": "DUP.lan", "port": 8080})
	if r := s.call("POST", "/api/proxies", m{"protocol": "http", "host": "dup.lan", "port": 8080}); r.status != 409 {
		t.Fatalf("unauthenticated duplicate: %d", r.status)
	}
}

func TestUpdateKeepsReplacesAndRemovesCredentials(t *testing.T) {
	s := newSession(t)
	p := s.create(m{"name": "A", "protocol": "http", "host": "u.lan", "port": 1, "tags": []string{"x"},
		"auth_enabled": true, "username": "alice", "password": "pw-one-1"})
	path := idPath(p)
	put := func(body m) resp {
		b := m{"protocol": "http", "host": "u.lan", "port": 1}
		for k, v := range body {
			b[k] = v
		}
		return s.call("PUT", path, b)
	}
	get := func() m { return decode(t, s.call("GET", path, nil)) }

	if r := put(m{"name": "Renamed"}); r.status != 200 {
		t.Fatalf("rename: %d %s", r.status, r.body)
	}
	got := get()
	if got["name"] != "Renamed" || got["username"] != "alice" || got["has_password"] != true || got["auth_enabled"] != true {
		t.Fatalf("omitted credentials/tags must be kept: %v", got)
	}
	if tags := got["tags"].([]any); len(tags) != 1 || tags[0] != "x" {
		t.Fatalf("tags: %v", got["tags"])
	}

	// A kept password still works: updating the username keeps the old password
	// (observable through duplicate detection against another proxy).
	other := s.create(m{"protocol": "http", "host": "u.lan", "port": 1, "auth_enabled": true, "username": "bob", "password": "pw-one-1"})
	_ = other
	if r := put(m{"username": "bob"}); r.status != 409 {
		t.Fatalf("username change colliding with bob/pw-one-1: %d %s", r.status, r.body)
	}
	if r := put(m{"password": "pw-two-2"}); r.status != 200 {
		t.Fatalf("password replace: %d %s", r.status, r.body)
	}
	if r := put(m{"username": "bob"}); r.status != 200 {
		t.Fatalf("bob/pw-two-2 should be free now: %d %s", r.status, r.body)
	}
	if r := put(m{"password": ""}); r.status != 200 || get()["has_password"] != false {
		t.Fatalf("empty password must replace: %d %s", r.status, r.body)
	}
	fieldError(t, put(m{"password": nil}), 422, "validation_failed", "password")
	fieldError(t, put(m{"username": ""}), 422, "validation_failed", "username")
	fieldError(t, put(m{"auth_enabled": false, "username": "x"}), 422, "validation_failed", "username")

	if r := put(m{"auth_enabled": false}); r.status != 200 {
		t.Fatalf("remove auth: %d %s", r.status, r.body)
	}
	if g := get(); g["auth_enabled"] != false || g["username"] != "" || g["has_password"] != false {
		t.Fatalf("auth not removed: %v", g)
	}
	fieldError(t, put(m{"password": "orphan"}), 422, "validation_failed", "username")
	fieldError(t, put(m{"auth_enabled": true}), 422, "validation_failed", "username")
	if r := put(m{"auth_enabled": true, "username": "carol", "password": "pw-three-3"}); r.status != 200 {
		t.Fatalf("enable auth: %d %s", r.status, r.body)
	}
	if g := get(); g["auth_enabled"] != true || g["username"] != "carol" || g["has_password"] != true {
		t.Fatalf("auth not enabled: %v", g)
	}
	for _, b := range s.e.bodies {
		for _, secret := range []string{"pw-one-1", "pw-two-2", "pw-three-3", "orphan"} {
			if strings.Contains(b, secret) {
				t.Fatalf("response leaks %s: %s", secret, b)
			}
		}
	}
}

func TestUpdateEndpointConflictAndNotFound(t *testing.T) {
	s := newSession(t)
	a := s.create(m{"protocol": "http", "host": "a.lan", "port": 1})
	b := s.create(m{"protocol": "http", "host": "b.lan", "port": 1})
	if r := s.call("PUT", idPath(b), m{"protocol": "http", "host": "A.LAN", "port": 1}); r.status != 409 {
		t.Fatalf("duplicate endpoint: %d", r.status)
	}
	r := s.call("PUT", idPath(b), m{"protocol": "socks5", "host": "c.lan", "port": 1080})
	if r.status != 200 || decode(t, r)["protocol"] != "socks5" {
		t.Fatalf("endpoint change: %d %s", r.status, r.body)
	}
	_ = a
	for _, path := range []string{"/api/proxies/9999", "/api/proxies/abc", "/api/proxies/0", "/api/proxies/-1"} {
		for _, method := range []string{"GET", "PUT", "DELETE"} {
			var body any
			if method == "PUT" {
				body = m{"protocol": "http", "host": "x.lan", "port": 1}
			}
			if r := s.call(method, path, body); r.status != 404 {
				t.Errorf("%s %s: %d", method, path, r.status)
			}
		}
	}
	fieldError(t, s.call("PUT", idPath(b), m{"host": "x.lan", "port": 1}), 422, "validation_failed", "protocol")
}

func TestScheduleOverrides(t *testing.T) {
	s := newSession(t)
	p := s.create(m{"protocol": "http", "host": "s.lan", "port": 1,
		"availability_schedule": m{"enabled": true, "interval_minutes": 5},
		"speed_schedule":        m{"enabled": false, "interval_minutes": 360}})
	if p["availability_schedule"].(m)["interval_minutes"] != float64(5) || p["speed_schedule"].(m)["enabled"] != false {
		t.Fatalf("overrides: %v", p)
	}
	path := idPath(p)
	base := func(extra m) m {
		b := m{"protocol": "http", "host": "s.lan", "port": 1}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	// Absent keeps both.
	if g := decode(t, s.call("PUT", path, base(nil))); g["availability_schedule"] == nil || g["speed_schedule"] == nil {
		t.Fatalf("absent overrides were cleared: %v", g)
	}
	// Null clears one (inherit), keeps the other.
	g := decode(t, s.call("PUT", path, base(m{"availability_schedule": nil})))
	if g["availability_schedule"] != nil || g["speed_schedule"] == nil {
		t.Fatalf("null override: %v", g)
	}
	// Explicit disabled override is stored as disabled.
	g = decode(t, s.call("PUT", path, base(m{"availability_schedule": m{"enabled": false, "interval_minutes": 10080}})))
	if sch := g["availability_schedule"].(m); sch["enabled"] != false || sch["interval_minutes"] != float64(10080) {
		t.Fatalf("disabled override: %v", g)
	}
	fieldError(t, s.call("PUT", path, base(m{"speed_schedule": m{"enabled": true, "interval_minutes": 0}})), 422, "validation_failed", "speed_schedule")
}

func TestDeleteProxy(t *testing.T) {
	s := newSession(t)
	p := s.create(m{"protocol": "http", "host": "del.lan", "port": 1})
	if r := s.call("DELETE", idPath(p), nil); r.status != 204 {
		t.Fatalf("delete: %d %s", r.status, r.body)
	}
	if r := s.call("GET", idPath(p), nil); r.status != 404 {
		t.Fatalf("get after delete: %d", r.status)
	}
	if r := s.call("DELETE", idPath(p), nil); r.status != 404 {
		t.Fatalf("second delete: %d", r.status)
	}
}

func rowByLine(t *testing.T, resp m, line int) m {
	t.Helper()
	for _, r := range resp["rows"].([]any) {
		if row := r.(m); row["line"] == float64(line) {
			return row
		}
	}
	t.Fatalf("no row for line %d in %v", line, resp)
	return nil
}

func TestImportPreviewRedactsAndFlagsDuplicates(t *testing.T) {
	s := newSession(t)
	s.create(m{"protocol": "http", "host": "10.0.0.1", "port": 3128, "auth_enabled": true, "username": "alice", "password": "Stored#Pw1"})
	text := strings.Join([]string{
		"http://alice:Stored%23Pw1@10.0.0.1:3128",  // 1: duplicate of a stored proxy
		"http://alice:Other%23Pw2@10.0.0.1:3128",   // 2: different password, importable
		"socks5://bob:Pasted-Secret@10.0.0.2:1080", // 3: valid
		"socks5://bob:Pasted-Secret@10.0.0.2:1080", // 4: duplicate inside the list
		"http://carol:Leaky-Secret@bad host:80",    // 5: invalid
		"10.0.0.3:80:dave:Ambiguous-Secret",        // 6: invalid
		"10.0.0.4:8080",                            // 7: bare, default protocol
	}, "\n")
	r := s.call("POST", "/api/proxies/import/preview", m{"text": text, "default_protocol": "https"})
	if r.status != 200 {
		t.Fatalf("preview: %d %s", r.status, r.body)
	}
	for _, secret := range []string{"Stored#Pw1", "Stored%23Pw1", "Other%23Pw2", "Pasted-Secret", "Leaky-Secret", "Ambiguous-Secret", "carol", "dave"} {
		if strings.Contains(r.body, secret) {
			t.Fatalf("preview leaks %q: %s", secret, r.body)
		}
	}
	out := decode(t, r)
	if out["existing_count"] != float64(1) || out["limit"] != float64(100) {
		t.Fatalf("counts: %v", out)
	}
	check := func(line int, valid, dup bool, code string) {
		row := rowByLine(t, out, line)
		if row["valid"] != valid || row["duplicate"] != dup || (code != "" && row["error_code"] != code) {
			t.Errorf("line %d: %v", line, row)
		}
	}
	check(1, true, true, "")
	check(2, true, false, "")
	check(3, true, false, "")
	check(4, true, true, "")
	check(5, false, false, "invalid_url")
	check(6, false, false, "ambiguous_format")
	check(7, true, false, "")
	if row := rowByLine(t, out, 7); !strings.HasPrefix(row["masked"].(string), "https://10.0.0.4:8080") {
		t.Fatalf("default protocol not applied: %v", row)
	}
	// Preview saves nothing.
	if n := len(decode(t, s.call("GET", "/api/proxies", nil))["proxies"].([]any)); n != 1 {
		t.Fatalf("preview stored proxies: %d", n)
	}
}

func TestImportPreviewValidation(t *testing.T) {
	s := newSession(t)
	fieldError(t, s.call("POST", "/api/proxies/import/preview", m{"text": "1.1.1.1:80", "default_protocol": "ftp"}), 422, "validation_failed", "default_protocol")
	fieldError(t, s.call("POST", "/api/proxies/import/preview", m{"text": "1.1.1.1:80"}), 422, "validation_failed", "default_protocol")
	lines := make([]string, 1001)
	for i := range lines {
		lines[i] = fmt.Sprintf("10.0.%d.%d:80", i/250, i%250)
	}
	fieldError(t, s.call("POST", "/api/proxies/import/preview", m{"text": strings.Join(lines, "\n"), "default_protocol": "http"}), 422, "too_many_lines", "")
	r := s.call("POST", "/api/proxies/import/preview", m{"text": "", "default_protocol": "http"})
	if r.status != 200 || len(decode(t, r)["rows"].([]any)) != 0 {
		t.Fatalf("empty text: %d %s", r.status, r.body)
	}
}

func TestBodySizeCap(t *testing.T) {
	s := newSession(t)
	big := m{"text": strings.Repeat("#", 300*1024), "default_protocol": "http"}
	for _, path := range []string{"/api/proxies/import/preview", "/api/proxies/import"} {
		if r := s.call("POST", path, big); r.status != 413 {
			t.Errorf("%s: %d", path, r.status)
		}
	}
	if r := s.call("POST", "/api/proxies/import/preview", m{"text": strings.Repeat("#", 200*1024), "default_protocol": "http"}); r.status != 200 {
		t.Fatalf("a 200 KiB body must be accepted: %d", r.status)
	}
}

func TestImportSavesSelectedValidRows(t *testing.T) {
	s := newSession(t)
	text := "http://alice:Imp0rt-Pw@10.0.0.1:3128\n# skipped comment\n10.0.0.2:8080\nbad line\n10.0.0.2:8080\n"
	r := s.call("POST", "/api/proxies/import", m{"text": text, "default_protocol": "socks5", "selected_lines": []int{1, 3}})
	if r.status != 201 {
		t.Fatalf("import: %d %s", r.status, r.body)
	}
	out := decode(t, r)
	if out["imported"] != float64(2) || len(out["ids"].([]any)) != 2 {
		t.Fatalf("result: %v", out)
	}
	if strings.Contains(r.body, "Imp0rt-Pw") {
		t.Fatal("import response leaks the password")
	}
	list := decode(t, s.call("GET", "/api/proxies", nil))["proxies"].([]any)
	if len(list) != 2 {
		t.Fatalf("stored %d proxies", len(list))
	}
	first, second := list[0].(m), list[1].(m)
	if first["protocol"] != "http" || first["username"] != "alice" || first["has_password"] != true || first["name"] != "10.0.0.1:3128" ||
		second["protocol"] != "socks5" || second["host"] != "10.0.0.2" || second["auth_enabled"] != false {
		t.Fatalf("stored proxies: %v", list)
	}
}

func TestImportRejectsUnselectableLinesWithoutSaving(t *testing.T) {
	s := newSession(t)
	s.create(m{"protocol": "http", "host": "10.0.0.9", "port": 80})
	text := "10.0.0.1:80\nbad line\n10.0.0.1:80\n10.0.0.9:80\n"
	for name, lines := range map[string][]int{
		"invalid line":      {1, 2},
		"list duplicate":    {1, 3},
		"stored duplicate":  {1, 4},
		"unknown line":      {1, 99},
		"comment or blank":  {1, 5},
		"empty selection":   {},
		"non-positive line": {0},
	} {
		r := s.call("POST", "/api/proxies/import", m{"text": text, "default_protocol": "http", "selected_lines": lines})
		fieldError(t, r, 422, "validation_failed", "selected_lines")
		if n := len(decode(t, s.call("GET", "/api/proxies", nil))["proxies"].([]any)); n != 1 {
			t.Fatalf("%s: rejected import changed the stored count to %d", name, n)
		}
	}
	// Repeating a line number selects it once.
	r := s.call("POST", "/api/proxies/import", m{"text": text, "default_protocol": "http", "selected_lines": []int{1, 1}})
	if r.status != 201 || decode(t, r)["imported"] != float64(1) {
		t.Fatalf("repeated selection: %d %s", r.status, r.body)
	}
}

func TestImportOverTheLimitRollsBack(t *testing.T) {
	s := newSession(t)
	var lines []string
	for i := 0; i < 99; i++ {
		lines = append(lines, fmt.Sprintf("10.1.0.%d:80", i+1))
	}
	sel := make([]int, 99)
	for i := range sel {
		sel[i] = i + 1
	}
	if r := s.call("POST", "/api/proxies/import", m{"text": strings.Join(lines, "\n"), "default_protocol": "http", "selected_lines": sel}); r.status != 201 {
		t.Fatalf("99 proxies: %d %s", r.status, r.body)
	}
	over := "10.2.0.1:80\n10.2.0.2:80\n"
	r := s.call("POST", "/api/proxies/import", m{"text": over, "default_protocol": "http", "selected_lines": []int{1, 2}})
	fieldError(t, r, 422, "record_limit", "")
	list := decode(t, s.call("GET", "/api/proxies", nil))["proxies"].([]any)
	if len(list) != 99 {
		t.Fatalf("99 + 2 stored %d proxies; the import must be all-or-nothing", len(list))
	}
	// Preview reports the headroom so a client can warn before saving.
	pv := decode(t, s.call("POST", "/api/proxies/import/preview", m{"text": over, "default_protocol": "http"}))
	if pv["existing_count"] != float64(99) || pv["limit"] != float64(100) {
		t.Fatalf("preview counts: %v", pv)
	}
	// The single-create route enforces the same limit.
	s.create(m{"protocol": "http", "host": "last.lan", "port": 1})
	fieldError(t, s.call("POST", "/api/proxies", m{"protocol": "http", "host": "over.lan", "port": 1}), 422, "record_limit", "")
}
