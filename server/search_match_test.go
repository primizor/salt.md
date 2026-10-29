package server

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"
)

// A search for two words found nothing as soon as one of them had a second
// spelling: a stem, or a sharp s swapped for ss (#107). ftsMatch put such a
// word in a group, ("release"* OR "releas"*), FTS5 refuses a group next to
// another term without an explicit AND, and the failed query was reported as
// "no results".

// Every pattern ftsMatch builds has to be one FTS5 accepts, on both indexes.
// The inputs are the shapes a person types: one word, several, words with and
// without a stem, quotes, operators FTS5 would otherwise read as syntax.
func TestFtsMatchIsValidFTS5(t *testing.T) {
	s := testServer(t)
	inputs := []string{
		"release", "release notes", "march release", "attached invoices",
		"draft notes", "error 123", "price display cockpit",
		"strasse map", // i18n-ok: a typed ss that gets an ß variant
		`say "hello"`, "a OR b", "NOT this", "NEAR(x y)", "C++ -", "(x) y*", "col:umn",
	}
	for _, q := range inputs {
		for _, table := range []string{"chunks_fts", "pages_fts"} {
			var n int
			if err := s.db.QueryRow(`SELECT count(*) FROM `+table+` WHERE `+table+` MATCH ?`, ftsMatch(q)).Scan(&n); err != nil {
				t.Errorf("%s: %q became %s, which FTS5 refuses: %v", table, q, ftsMatch(q), err)
			}
		}
	}
}

// And the search itself finds the page for every query whose words are all
// in it, through the route the browser uses.
func TestSearchFindsEveryWordCombination(t *testing.T) {
	s := testServer(t)
	uid, cookie := signedIn(t, s, "finder@example.test")
	ws := s.firstWorkspaceOf(t, uid)
	id := newID()
	body := `[{"type":"paragraph","content":[{"type":"text","text":"Draft of the release notes. The invoices are attached."}]}]`
	if _, err := s.db.Exec(`INSERT INTO pages (id, title, content, position, created_at, updated_at, workspace_id, owner_id, visibility, type)
		VALUES (?, 'Release notes for March', ?, 0, ?, ?, ?, ?, 'workspace', 'doc')`,
		id, body, now(), now(), ws, uid); err != nil {
		t.Fatalf("insert: %v", err)
	}
	s.reindexPage(id)

	for _, q := range []string{"release", "notes", "draft notes", "release notes", "march release", "attached invoices", "invoices release march"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/search?q="+url.QueryEscape(q), nil)
		req.Header.Set("Cookie", cookie)
		s.ServeHTTP(rec, req)
		var hits []searchResult
		if err := json.Unmarshal(rec.Body.Bytes(), &hits); err != nil {
			t.Fatalf("%q: %d %s", q, rec.Code, rec.Body.String())
		}
		if len(hits) != 1 || hits[0].ID != id {
			t.Errorf("%q found %d pages, want the one page", q, len(hits))
		}
	}
	// A word that is not on the page still narrows: AND, not OR.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/search?q="+url.QueryEscape("release elephants"), nil)
	req.Header.Set("Cookie", cookie)
	s.ServeHTTP(rec, req)
	if rec.Body.String() != "[]\n" && rec.Body.String() != "[]" {
		t.Errorf("a word the page does not contain still found it: %s", rec.Body.String())
	}
}
