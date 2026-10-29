package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// A client decides from a tool's annotations whether it may call it without
// asking: plan mode and background agents refuse anything that might write.
// readOnlyHint has to say what the read-only gate says, and a writing tool has
// to state destructiveHint, because a client that finds none assumes true.
func TestToolAnnotationsMatchTheGate(t *testing.T) {
	for _, tool := range mcpTools {
		name := tool["name"].(string)
		a, ok := tool["annotations"].(map[string]any)
		if !ok {
			t.Errorf("%s has no annotations", name)
			continue
		}
		if ro, ok := a["readOnlyHint"].(bool); !ok || ro != readTools[name] {
			t.Errorf("%s: readOnlyHint %v, readTools says %v", name, a["readOnlyHint"], readTools[name])
		}
		if !readTools[name] {
			for _, k := range []string{"destructiveHint", "idempotentHint", "openWorldHint"} {
				if _, ok := a[k].(bool); !ok {
					t.Errorf("%s writes and leaves %s to the default", name, k)
				}
			}
		}
		if open, _ := a["openWorldHint"].(bool); open != (name == "import_url") {
			t.Errorf("%s: openWorldHint %v", name, open)
		}
	}
}

// The annotations reach a client through tools/list, not just the catalogue.
func TestToolsListCarriesAnnotations(t *testing.T) {
	s := testServer(t)
	uid, _ := signedIn(t, s, "lister@example.test")
	secret := "annotations-token"
	if _, err := s.db.Exec(`INSERT INTO api_tokens (id, user_id, name, token_hash, scope, created_at)
		VALUES (?, ?, 'agent', ?, 'read', ?)`, newID(), uid, tokenHash(secret), now()); err != nil {
		t.Fatalf("insert token: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	s.ServeHTTP(rec, req)
	var resp struct {
		Result struct {
			Tools []struct {
				Name        string          `json:"name"`
				Annotations map[string]bool `json:"annotations"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || len(resp.Result.Tools) != len(mcpTools) {
		t.Fatalf("tools/list: %d %s", rec.Code, rec.Body.String())
	}
	for _, tool := range resp.Result.Tools {
		ro, ok := tool.Annotations["readOnlyHint"]
		if !ok || ro != readTools[tool.Name] {
			t.Errorf("%s: readOnlyHint %v over the wire, readTools says %v", tool.Name, ro, readTools[tool.Name])
		}
	}
}

// Every writing tool honours idempotency_key, so every writing tool declares
// it; a client that sends only declared arguments could not use it otherwise
// (#119). A reading tool has nothing to repeat and does not offer it.
func TestWritingToolsDeclareTheIdempotencyKey(t *testing.T) {
	for _, tool := range mcpTools {
		name := tool["name"].(string)
		schema, _ := tool["inputSchema"].(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		_, declared := props["idempotency_key"]
		if declared == readTools[name] {
			t.Errorf("%s: declares idempotency_key %v, but writes %v", name, declared, !readTools[name])
		}
	}
}
