package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// responsesServer answers /responses with the given outputs in order and keeps
// the request bodies.
func responsesServer(t *testing.T, replies []string) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path = %s", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		bodies = append(bodies, m)
		w.Write([]byte(replies[len(bodies)-1]))
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

const toolTurn = `{"status":"completed","output":[
 {"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"ENC"},
 {"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}","status":"completed"}],
 "usage":{"input_tokens":10,"output_tokens":5}}`

const finalTurn = `{"status":"completed","output":[
 {"type":"reasoning","id":"rs_2","summary":[],"encrypted_content":"ENC2"},
 {"type":"message","role":"assistant","content":[{"type":"output_text","text":"{\"answer\":\"42\"}","annotations":[]}]}],
 "usage":{"input_tokens":20,"output_tokens":7}}`

type answer struct {
	Answer string `json:"answer"`
}

func TestRunToolsOverResponsesAPI(t *testing.T) {
	srv, bodies := responsesServer(t, []string{toolTurn, finalTurn})
	c := &Client{
		Provider: &OpenAI{APIKey: "k", BaseURL: srv.URL + "/v1", HTTP: srv.Client()},
		Models:   map[Purpose]string{Investigator: "gpt-5.6-terra"},
		Efforts:  map[Purpose]string{Investigator: "medium"},
		APIs:     map[Purpose]string{Investigator: APIResponses},
		Timeout:  5 * time.Second, MaxIter: 4,
	}
	var seen string
	tools := []Tool{{Name: "lookup", Description: "d", InputSchema: map[string]any{"type": "object"},
		Handler: func(_ context.Context, in json.RawMessage) (any, error) {
			seen = string(in)
			return map[string]int{"n": 1}, nil
		}}}

	got, err := RunTools(context.Background(), c, Request{Purpose: Investigator, System: "sys", User: "usr"}, tools, func(a answer) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if got.Answer != "42" || seen != `{"q":"x"}` {
		t.Fatalf("answer=%+v tool input=%s", got, seen)
	}

	first, second := (*bodies)[0], (*bodies)[1]
	if first["store"] != false || first["reasoning"].(map[string]any)["effort"] != "medium" {
		t.Errorf("first request = %v", first)
	}
	if inc := first["include"].([]any); len(inc) != 1 || inc[0] != "reasoning.encrypted_content" {
		t.Errorf("include = %v", inc)
	}
	if first["text"].(map[string]any)["format"].(map[string]any)["type"] != "json_schema" {
		t.Errorf("text.format = %v", first["text"])
	}
	tool := first["tools"].([]any)[0].(map[string]any)
	if tool["type"] != "function" || tool["name"] != "lookup" {
		t.Errorf("tool = %v (responses tools are flat, not nested under function)", tool)
	}
	in1 := first["input"].([]any)
	if in1[0].(map[string]any)["role"] != "developer" || in1[1].(map[string]any)["role"] != "user" {
		t.Errorf("first input = %v", in1)
	}

	// The second turn replays the reasoning and function_call items, then the tool output.
	in2 := second["input"].([]any)
	types := []string{}
	for _, it := range in2 {
		m := it.(map[string]any)
		if ty, ok := m["type"].(string); ok {
			types = append(types, ty)
		} else {
			types = append(types, "role:"+m["role"].(string))
		}
	}
	want := "role:developer,role:user,reasoning,function_call,function_call_output"
	if strings.Join(types, ",") != want {
		t.Fatalf("second input = %v, want %s", types, want)
	}
	out := in2[4].(map[string]any)
	if out["call_id"] != "call_1" || out["output"] != `{"n":1}` {
		t.Errorf("function_call_output = %v", out)
	}
	if in2[2].(map[string]any)["encrypted_content"] != "ENC" {
		t.Errorf("reasoning item was not replayed intact: %v", in2[2])
	}
}

func TestResponsesAPIErrorsAndRefusals(t *testing.T) {
	for name, tc := range map[string]struct{ reply, want string }{
		"refusal":    {`{"status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"no"}]}]}`, "refused"},
		"incomplete": {`{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}`, "incomplete (max_output_tokens)"},
		"error":      {`{"status":"failed","error":{"message":"boom"},"output":[]}`, "boom"},
	} {
		srv, _ := responsesServer(t, []string{tc.reply})
		o := &OpenAI{APIKey: "k", BaseURL: srv.URL + "/v1", HTTP: srv.Client()}
		_, err := o.Chat(context.Background(), ChatRequest{Model: "m", API: APIResponses, Messages: []Message{{Role: "user", Content: "x"}}})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

func TestPerPurposeTimeout(t *testing.T) {
	slow := &slowProvider{d: 60 * time.Millisecond}
	c := &Client{Provider: slow, Timeout: 10 * time.Millisecond, Timeouts: map[Purpose]time.Duration{Investigator: time.Second}}
	if _, err := c.call(context.Background(), Investigator, 1, ChatRequest{Model: "m"}); err != nil {
		t.Fatalf("investigator should get its longer timeout: %v", err)
	}
	if _, err := c.call(context.Background(), Author, 1, ChatRequest{Model: "m"}); err == nil {
		t.Fatal("author should have hit the default timeout")
	}
}

type slowProvider struct{ d time.Duration }

func (s *slowProvider) Chat(ctx context.Context, _ ChatRequest) (ChatResponse, error) {
	select {
	case <-time.After(s.d):
		return ChatResponse{}, nil
	case <-ctx.Done():
		return ChatResponse{}, ctx.Err()
	}
}
