package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHermesACPAdapterInjectsVisibleOnlyContract(t *testing.T) {
	adapter := newHermesACPAdapter()

	out, ok, err := adapter.rewriteParentLine([]byte(`{"jsonrpc":"2.0","id":7,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"text","text":"回个1"}]}}`))
	if err != nil {
		t.Fatalf("rewriteParentLine returned error: %v", err)
	}
	if !ok {
		t.Fatal("rewriteParentLine dropped parent request")
	}

	var env struct {
		Method string `json:"method"`
		Params struct {
			Prompt []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"prompt"`
		} `json:"params"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("rewritten request is not JSON: %v\n%s", err, string(out))
	}
	if env.Method != "session/prompt" {
		t.Fatalf("method = %q, want session/prompt", env.Method)
	}
	if len(env.Params.Prompt) != 2 {
		t.Fatalf("prompt blocks = %d, want 2: %#v", len(env.Params.Prompt), env.Params.Prompt)
	}
	if got := env.Params.Prompt[0].Text; got != "回个1" {
		t.Fatalf("original prompt text = %q, want unchanged", got)
	}
	contract := env.Params.Prompt[1].Text
	if !strings.Contains(contract, "DIREXIO ACP OUTPUT CONTRACT") || !strings.Contains(contract, "最终答案") {
		t.Fatalf("contract block missing required wording: %q", contract)
	}
}

func TestHermesACPAdapterBuffersMessageChunksUntilPromptResponse(t *testing.T) {
	adapter := newHermesACPAdapter()
	_, _, err := adapter.rewriteParentLine([]byte(`{"jsonrpc":"2.0","id":"turn-1","method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"text","text":"回个1"}]}}`))
	if err != nil {
		t.Fatalf("rewriteParentLine returned error: %v", err)
	}

	chunk := []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"用户让我回复一个\"1\"。这是一个简单请求。1"}}}}`)
	out, err := adapter.rewriteChildLine(chunk)
	if err != nil {
		t.Fatalf("rewriteChildLine returned error for chunk: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("message chunk should be buffered, got %d outbound lines", len(out))
	}

	response := []byte(`{"jsonrpc":"2.0","id":"turn-1","result":{"stopReason":"end_turn"}}`)
	out, err = adapter.rewriteChildLine(response)
	if err != nil {
		t.Fatalf("rewriteChildLine returned error for response: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("prompt response should flush text then response, got %d lines: %#v", len(out), out)
	}
	if string(out[1]) != string(response) {
		t.Fatalf("second outbound line = %s, want original response", out[1])
	}

	var flushed struct {
		Method string `json:"method"`
		Params struct {
			SessionID string `json:"sessionId"`
			Update    struct {
				SessionUpdate string `json:"sessionUpdate"`
				Content       struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"update"`
		} `json:"params"`
	}
	if err := json.Unmarshal(out[0], &flushed); err != nil {
		t.Fatalf("flushed line is not JSON: %v\n%s", err, string(out[0]))
	}
	if flushed.Method != "session/update" || flushed.Params.SessionID != "s1" || flushed.Params.Update.SessionUpdate != "agent_message_chunk" {
		t.Fatalf("unexpected flushed update: %#v", flushed)
	}
	if got := flushed.Params.Update.Content.Text; got != "1" {
		t.Fatalf("flushed text = %q, want cleaned final answer", got)
	}
}

func TestHermesACPAdapterForwardsToolUpdates(t *testing.T) {
	adapter := newHermesACPAdapter()
	line := []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"t1","title":"Read file"}}}`)

	out, err := adapter.rewriteChildLine(line)
	if err != nil {
		t.Fatalf("rewriteChildLine returned error: %v", err)
	}
	if len(out) != 1 || string(out[0]) != string(line) {
		t.Fatalf("tool update should pass through unchanged, got %#v", out)
	}
}

func TestSanitizeHermesVisibleText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "think tags",
			in:   "<think>internal reasoning</think>1",
			want: "1",
		},
		{
			name: "explicit final marker",
			in:   "用户让我先分析。\n最终答案：可以，已经开始。",
			want: "可以，已经开始。",
		},
		{
			name: "plain hermes meta narration",
			in:   `The user asked me to reply with "1". This is simple. 1`,
			want: "1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeHermesVisibleText(tt.in); got != tt.want {
				t.Fatalf("sanitizeHermesVisibleText() = %q, want %q", got, tt.want)
			}
		})
	}
}
