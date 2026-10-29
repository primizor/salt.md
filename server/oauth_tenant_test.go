package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The Microsoft tenant (#110), exercised through the real routes.
//
// Before the setting existed every Microsoft address said "common", and a
// single-tenant app registration refuses that outright (AADSTS50194). An
// address is built in five places: the authorize redirect and the token
// exchange of the sign-in, the same two for connecting a mailbox, and the
// mailbox's token refresh. These tests follow the tenant into all five, because
// the one that still says common is the bug.

const (
	tenantID      = "6f9c2b1e-0d4a-4c3b-9e8f-1a2b3c4d5e6f"
	otherTenantID = "0b1c2d3e-4f50-4617-8829-3a4b5c6d7e8f"
)

// msStub stands in for login.microsoftonline.com. It notes where each request
// went, which is what these tests are about, and answers every token request
// with an id_token whose tid names the directory the account came from.
type msStub struct {
	next  http.RoundTripper
	tid   string
	calls []string
}

func (m *msStub) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "login.microsoftonline.com" {
		return m.next.RoundTrip(r)
	}
	m.calls = append(m.calls, r.URL.Scheme+"://"+r.URL.Host+r.URL.Path)
	claims, _ := json.Marshal(map[string]string{"email": "ada@example.com", "name": "Ada", "tid": m.tid})
	body, _ := json.Marshal(map[string]string{
		"access_token":  "access",
		"refresh_token": "refresh",
		"id_token":      "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".unsigned",
	})
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    r,
	}, nil
}

// last is where the latest request went, and forgets them all: a step that
// never reached Microsoft must not pass on the step before it.
func (m *msStub) last() string {
	defer func() { m.calls = nil }()
	if len(m.calls) == 0 {
		return ""
	}
	return m.calls[len(m.calls)-1]
}

// stubMicrosoft points the server's calls to Microsoft at a stub for the length
// of one test. The OAuth code builds its own http.Client without a transport,
// which means http.DefaultTransport; anything not addressed to Microsoft goes
// through to the real one untouched.
func stubMicrosoft(t *testing.T, tid string) *msStub {
	t.Helper()
	stub := &msStub{next: http.DefaultTransport, tid: tid}
	http.DefaultTransport = stub
	t.Cleanup(func() { http.DefaultTransport = stub.next })
	return stub
}

// microsoftRoundTrip walks one flow the way a browser does: the start sets the
// state cookie and sends the browser to Microsoft, Microsoft (the stub) sends
// it back with a code, and salt.md exchanges the code at the token endpoint.
// It returns where the browser was sent and what the callback answered.
func microsoftRoundTrip(t *testing.T, s *Server, flow, session string) (*url.URL, *httptest.ResponseRecorder) {
	t.Helper()
	get := func(path string, cookies []*http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if session != "" {
			req.Header.Set("Cookie", session)
		}
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec
	}
	start := get(flow+"/start", nil)
	if start.Code != http.StatusFound {
		t.Fatalf("%s/start: got %d, want a redirect to Microsoft", flow, start.Code)
	}
	authorize, err := url.Parse(start.Header().Get("Location"))
	if err != nil {
		t.Fatalf("%s/start: unreadable redirect: %v", flow, err)
	}
	callback := get(flow+"/callback?code=probe&state="+url.QueryEscape(authorize.Query().Get("state")), start.Result().Cookies())
	return authorize, callback
}

func endpointOf(u *url.URL) string { return u.Scheme + "://" + u.Host + u.Path }

func hasSession(rec *httptest.ResponseRecorder) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return true
		}
	}
	return false
}

func putSettings(t *testing.T, s *Server, cookie, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

// withMicrosoftClient is an instance with a Microsoft app configured, an admin,
// and the account the stub signs in as.
func withMicrosoftClient(t *testing.T) (s *Server, admin string) {
	t.Helper()
	s = testServer(t)
	_, admin = signedIn(t, s, "admin@example.com")
	signedIn(t, s, "ada@example.com")
	s.setSetting("oauth_ms_id", "client")
	s.setSetting("oauth_ms_secret", "secret")
	return s, admin
}

func TestMicrosoftAddressesFollowTheTenant(t *testing.T) {
	s, admin := withMicrosoftClient(t)
	stub := stubMicrosoft(t, tenantID)

	for _, tc := range []struct{ typed, inPath string }{
		// Nothing typed is how every instance starts, and it must keep talking
		// to common exactly as before.
		{"", "common"},
		{tenantID, tenantID},
		{"contoso.onmicrosoft.com", "contoso.onmicrosoft.com"},
	} {
		if rec := putSettings(t, s, admin, `{"msTenant":"`+tc.typed+`"}`); rec.Code != http.StatusOK {
			t.Fatalf("saving tenant %q: got %d %s", tc.typed, rec.Code, rec.Body)
		}
		base := "https://login.microsoftonline.com/" + tc.inPath + "/oauth2/v2.0/"

		authorize, cb := microsoftRoundTrip(t, s, "/api/oauth/microsoft", "")
		if got := endpointOf(authorize); got != base+"authorize" {
			t.Errorf("tenant %q, sign-in sent the browser to %s, want %sauthorize", tc.typed, got, base)
		}
		if got := stub.last(); got != base+"token" {
			t.Errorf("tenant %q, sign-in exchanged its code at %s, want %stoken", tc.typed, got, base)
		}
		if loc := cb.Header().Get("Location"); loc != "/" || !hasSession(cb) {
			t.Errorf("tenant %q, sign-in did not complete: %d %s", tc.typed, cb.Code, loc)
		}

		authorize, cb = microsoftRoundTrip(t, s, "/api/admin/mail-oauth/microsoft", admin)
		if got := endpointOf(authorize); got != base+"authorize" {
			t.Errorf("tenant %q, the mailbox connection sent the browser to %s, want %sauthorize", tc.typed, got, base)
		}
		if got := stub.last(); got != base+"token" {
			t.Errorf("tenant %q, the mailbox connection exchanged its code at %s, want %stoken", tc.typed, got, base)
		}
		if loc := cb.Header().Get("Location"); loc != "/?mailOauth=ok" {
			t.Errorf("tenant %q, the mailbox connection did not complete: %d %s", tc.typed, cb.Code, loc)
		}

		// Sending mail days later goes through the refresh, the one call no
		// browser is watching.
		if _, err := s.refreshedAccessToken("microsoft"); err != nil {
			t.Fatalf("tenant %q, refresh: %v", tc.typed, err)
		}
		if got := stub.last(); got != base+"token" {
			t.Errorf("tenant %q, the mailbox refresh went to %s, want %stoken", tc.typed, got, base)
		}
	}
}

// With a directory ID configured, the id_token's tid has to name that same
// directory. The endpoint already promises it; this is our own check of the
// promise, so an account from somewhere else does not get in just because a
// token endpoint answered.
func TestMicrosoftSignInChecksTheDirectory(t *testing.T) {
	s, _ := withMicrosoftClient(t)
	s.setSetting(settingMSTenant, tenantID)
	stub := stubMicrosoft(t, otherTenantID)

	refused := func(why string) {
		t.Helper()
		_, cb := microsoftRoundTrip(t, s, "/api/oauth/microsoft", "")
		loc, _ := url.Parse(cb.Header().Get("Location"))
		if code := loc.Query().Get("oauthError"); code != "oauth_wrong_tenant" {
			t.Errorf("%s: got %q (%s), want oauth_wrong_tenant", why, code, cb.Header().Get("Location"))
		}
		if hasSession(cb) {
			t.Errorf("%s: refused, and signed in anyway", why)
		}
	}
	refused("an account from another directory")
	stub.tid = ""
	refused("an id_token naming no directory at all")

	// The configured directory gets in, whatever case Microsoft writes it in.
	stub.tid = strings.ToUpper(tenantID)
	if _, cb := microsoftRoundTrip(t, s, "/api/oauth/microsoft", ""); cb.Header().Get("Location") != "/" || !hasSession(cb) {
		t.Errorf("the configured directory was refused: %s", cb.Header().Get("Location"))
	}

	// A domain cannot be compared with tid, which is always an ID. There the
	// endpoint holds the line on its own, and the check stands aside rather than
	// refusing everybody.
	s.setSetting(settingMSTenant, "contoso.onmicrosoft.com")
	stub.tid = otherTenantID
	if _, cb := microsoftRoundTrip(t, s, "/api/oauth/microsoft", ""); cb.Header().Get("Location") != "/" {
		t.Errorf("a domain tenant refused the sign-in: %s", cb.Header().Get("Location"))
	}
}

func TestMicrosoftTenantSettingIsChecked(t *testing.T) {
	s := testServer(t)
	_, admin := signedIn(t, s, "admin@example.com")

	// Stored trimmed and lower-cased, and shown back the way it was stored.
	for typed, stored := range map[string]string{
		"  6F9C2B1E-0D4A-4C3B-9E8F-1A2B3C4D5E6F ": tenantID,
		"Contoso.OnMicrosoft.com":                 "contoso.onmicrosoft.com",
		"contoso.com":                             "contoso.com",
		"xn--mller-kva.example":                   "xn--mller-kva.example",
		"":                                        "",
	} {
		body, _ := json.Marshal(map[string]string{"msTenant": typed})
		rec := putSettings(t, s, admin, string(body))
		if rec.Code != http.StatusOK {
			t.Errorf("%q: got %d %s, want it accepted", typed, rec.Code, rec.Body)
			continue
		}
		var back appSettings
		json.Unmarshal(rec.Body.Bytes(), &back)
		if got := s.setting(settingMSTenant, "?"); got != stored || back.MSTenant != stored {
			t.Errorf("%q: stored %q, shown %q, want %q", typed, got, back.MSTenant, stored)
		}
	}

	// Everything else is refused, and refused whole: the instance name sent in
	// the same request is not saved either, and the tenant stays as it was.
	s.setSetting(settingMSTenant, "contoso.com")
	for _, bad := range []string{
		"contoso.com/../common",
		"contoso.com?prompt=none",
		"contoso.com#x",
		"https://login.microsoftonline.com/contoso.com",
		"{" + tenantID + "}",
		tenantID[:35],
		"con toso.com",
		"-contoso.com",
		"contoso-.com",
		"contoso..com",
		"contoso.com.",
		"192.0.2.10",
		"müller.example", // i18n-ok: a non-ASCII domain is the input under test; punycode is the form accepted
		strings.Repeat("a.", 127) + "com",
		// No dot: not a domain. That includes Microsoft's own words; an empty
		// field already means common, and the field is for ONE directory.
		"contoso",
		"common",
		"organizations",
	} {
		body, _ := json.Marshal(map[string]string{"msTenant": bad, "instanceName": "Changed"})
		rec := putSettings(t, s, admin, string(body))
		var e struct {
			Code string `json:"code"`
		}
		json.Unmarshal(rec.Body.Bytes(), &e)
		if rec.Code != http.StatusBadRequest || e.Code != "oauth_tenant_invalid" {
			t.Errorf("%q: got %d %q, want 400 oauth_tenant_invalid", bad, rec.Code, e.Code)
		}
	}
	if got := s.setting(settingMSTenant, ""); got != "contoso.com" {
		t.Errorf("a refused value replaced the tenant: %q", got)
	}
	if got := s.setting("instance_name", ""); got == "Changed" {
		t.Error("a refused tenant left the rest of the dialog saved")
	}
}
