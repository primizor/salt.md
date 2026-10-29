package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// A comment an agent added over MCP looked like one the person typed (#116).
// It carries the agent now, as a note does: the claim it made, or "generic",
// beside the account that vouches for it. The browser cannot claim one.
func TestCommentsSayWhichAgentWroteThem(t *testing.T) {
	s := testServer(t)
	uid, cookie := signedIn(t, s, "commenter@example.test")
	u := &user{ID: uid, Name: "Grace"}
	ws := s.firstWorkspaceOf(t, uid)
	page := s.makePage(t, ws, uid, "", "Task", `{}`)

	if _, err := callTool(t, s, u, "comments", `{"page_id":"`+page+`","action":"add","body":"Named","agent":"claude","label":"Claude Code"}`); err != nil {
		t.Fatalf("add with agent: %v", err)
	}
	if _, err := callTool(t, s, u, "comments", `{"page_id":"`+page+`","action":"add","body":"Unnamed"}`); err != nil {
		t.Fatalf("add without agent: %v", err)
	}
	post := func(hdr, value, body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/pages/"+page+"/comments", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(hdr, value)
		s.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("POST comment: %d %s", rec.Code, rec.Body.String())
		}
	}
	// The browser may not pose as an agent.
	post("Cookie", cookie, `{"body":"Typed","agent":"claude","label":"Claude Code"}`)
	secret := "comment-agent-token"
	if _, err := s.db.Exec(`INSERT INTO api_tokens (id, user_id, name, token_hash, scope, created_at)
		VALUES (?, ?, 'script', ?, 'write', ?)`, newID(), uid, tokenHash(secret), now()); err != nil {
		t.Fatalf("insert token: %v", err)
	}
	post("Authorization", "Bearer "+secret, `{"body":"Scripted"}`)

	list, err := s.pageComments(page)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]string{
		"Named":    {"claude", "Claude Code"},
		"Unnamed":  {"generic", ""},
		"Typed":    {"", ""},
		"Scripted": {"generic", ""},
	}
	if len(list) != len(want) {
		t.Fatalf("%d comments, want %d", len(list), len(want))
	}
	for _, c := range list {
		w := want[c.Body]
		if c.Agent != w[0] || c.AgentLabel != w[1] {
			t.Errorf("%q: agent %q label %q author %q, want agent %q label %q", c.Body, c.Agent, c.AgentLabel, c.AuthorName, w[0], w[1])
		}
	}
}
