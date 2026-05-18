package server

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tgruben-circuit/percy/a2a"
)

const testToken = "test-token"

func newA2ATestServer(t *testing.T) (*httptest.Server, *TestHarness) {
	t.Helper()
	h := NewTestHarness(t)
	t.Setenv("PERCY_A2A_TOKEN", testToken)
	t.Setenv("PERCY_A2A_URL", "http://test/a2a")
	mux := http.NewServeMux()
	if !h.server.MountA2A(mux) {
		t.Fatal("MountA2A returned false")
	}
	ts := httptest.NewServer(mux)
	t.Cleanup(func() {
		ts.Close()
		h.Close()
	})
	return ts, h
}

func rpcCall(t *testing.T, ts *httptest.Server, method string, params any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	req, _ := http.NewRequest("POST", ts.URL+"/a2a", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("rpc %s: %v", method, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("rpc %s decode: %v", method, err)
	}
	return out
}

func TestA2AAgentCard(t *testing.T) {
	ts, _ := newA2ATestServer(t)
	resp, err := ts.Client().Get(ts.URL + "/.well-known/agent-card.json")
	if err != nil {
		t.Fatalf("get card: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var card a2a.AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if card.Name != "Percy" {
		t.Errorf("name = %q, want Percy", card.Name)
	}
	if !card.Capabilities.Streaming {
		t.Error("streaming capability should be true")
	}
	if len(card.Skills) == 0 {
		t.Error("no skills advertised")
	}
}

func TestA2AUnauthorized(t *testing.T) {
	ts, _ := newA2ATestServer(t)
	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tasks/get","params":{"id":"x"}}`)
	req, _ := http.NewRequest("POST", ts.URL+"/a2a", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestA2AMessageSend(t *testing.T) {
	ts, _ := newA2ATestServer(t)
	params := map[string]any{
		"message": map[string]any{
			"kind":      "message",
			"messageId": "m1",
			"role":      "user",
			"parts":     []map[string]any{{"kind": "text", "text": "echo: hello from a2a"}},
		},
	}
	resp := rpcCall(t, ts, "message/send", params)
	if e, ok := resp["error"]; ok && e != nil {
		t.Fatalf("rpc error: %v", e)
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result: %v", resp)
	}
	if result["kind"] != "task" {
		t.Errorf("kind = %v, want task", result["kind"])
	}
	status := result["status"].(map[string]any)
	if status["state"] != "completed" {
		t.Errorf("state = %v, want completed", status["state"])
	}
	msg := status["message"].(map[string]any)
	parts := msg["parts"].([]any)
	text := parts[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "hello from a2a") {
		t.Errorf("reply = %q, want it to contain echoed text", text)
	}
	if result["contextId"] == "" {
		t.Error("contextId should be set")
	}
}

func TestA2AContextContinuity(t *testing.T) {
	ts, _ := newA2ATestServer(t)
	params := map[string]any{
		"message": map[string]any{
			"kind":      "message",
			"messageId": "m1",
			"role":      "user",
			"parts":     []map[string]any{{"kind": "text", "text": "echo: first"}},
		},
	}
	resp := rpcCall(t, ts, "message/send", params)
	ctxID := resp["result"].(map[string]any)["contextId"].(string)

	// Second message with same contextId should continue the conversation.
	params["message"].(map[string]any)["messageId"] = "m2"
	params["message"].(map[string]any)["contextId"] = ctxID
	params["message"].(map[string]any)["parts"] = []map[string]any{{"kind": "text", "text": "echo: second"}}
	resp2 := rpcCall(t, ts, "message/send", params)
	if resp2["result"].(map[string]any)["contextId"].(string) != ctxID {
		t.Error("contextId not preserved across calls")
	}
}

func TestA2AMessageStream(t *testing.T) {
	ts, _ := newA2ATestServer(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"message/stream","params":{"message":{"kind":"message","messageId":"m1","role":"user","parts":[{"kind":"text","text":"echo: streamed"}]}}}`
	req, _ := http.NewRequest("POST", ts.URL+"/a2a", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	var events []map[string]any
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatalf("parse event: %v", err)
		}
		events = append(events, ev)
		result, ok := ev["result"].(map[string]any)
		if ok && result["final"] == true {
			break
		}
	}
	if len(events) < 2 {
		t.Fatalf("got %d events, want ≥2", len(events))
	}
	last := events[len(events)-1]["result"].(map[string]any)
	if last["final"] != true {
		t.Errorf("last event not final: %v", last)
	}
	status := last["status"].(map[string]any)
	if status["state"] != "completed" {
		t.Errorf("final state = %v, want completed", status["state"])
	}
}

func TestA2ATaskNotFound(t *testing.T) {
	ts, _ := newA2ATestServer(t)
	resp := rpcCall(t, ts, "tasks/get", map[string]any{"id": "does-not-exist"})
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error, got %v", resp)
	}
	if int(errObj["code"].(float64)) != -32001 {
		t.Errorf("code = %v, want -32001", errObj["code"])
	}
}
