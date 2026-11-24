package llm

import "testing"

func TestOpenAISchemaConvertsGeminiTypes(t *testing.T) {
	tools := []Tool{{
		Name:        "write_file",
		Description: "write",
		Parameters: map[string]any{
			"type":       "OBJECT",
			"properties": map[string]any{
				"path":    map[string]any{"type": "STRING"},
				"content": map[string]any{"type": "STRING"},
				"count":   map[string]any{"type": "NUMBER"},
			},
			"required": []any{"path", "content"},
		},
	}}

	out := toOpenAITools(tools)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	params := out[0].Function.Parameters
	if params["type"] != "object" {
		t.Fatalf("top-level type = %v, want object", params["type"])
	}
	props := params["properties"].(map[string]any)
	for _, key := range []string{"path", "content"} {
		if props[key].(map[string]any)["type"] != "string" {
			t.Fatalf("%s.type = %v, want string", key, props[key].(map[string]any)["type"])
		}
	}
	if props["count"].(map[string]any)["type"] != "number" {
		t.Fatalf("count.type = %v, want number", props["count"].(map[string]any)["type"])
	}
	req, _ := params["required"].([]any)
	if len(req) != 2 || req[0] != "path" || req[1] != "content" {
		t.Fatalf("required = %v, want [path content]", req)
	}
}

func TestOpenAISchemaHandlesNested(t *testing.T) {
	in := map[string]any{
		"type": "OBJECT",
		"properties": map[string]any{
			"nested": map[string]any{"type": "ARRAY", "items": map[string]any{"type": "BOOLEAN"}},
		},
	}
	out := openaiSchema(in).(map[string]any)
	nested := out["properties"].(map[string]any)["nested"].(map[string]any)
	if nested["type"] != "array" {
		t.Fatalf("nested type = %v, want array", nested["type"])
	}
	if nested["items"].(map[string]any)["type"] != "boolean" {
		t.Fatal("items type must be lowercase boolean")
	}
}

func TestToOpenAIMessages(t *testing.T) {
	msgs := toOpenAIMessages("sys", []Turn{
		{Role: "user", Text: "hello"},
		{Role: "model", ToolCalls: []FunctionCall{{Name: "write_file", Args: map[string]any{"path": "a.txt"}, ID: "call-1"}}},
		{Role: "user", ToolResponses: []FunctionResponse{{Name: "write_file", Response: map[string]any{"ok": true}, ID: "call-1"}}},
	})
	if len(msgs) != 4 {
		t.Fatalf("want 4 messages (system, user, assistant, tool), got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "system" || msgs[0].Content != "sys" {
		t.Errorf("bad system message: %+v", msgs[0])
	}
	if msgs[1].Role != "user" || msgs[1].Content != "hello" {
		t.Errorf("bad user message: %+v", msgs[1])
	}
	if msgs[2].Role != "assistant" || len(msgs[2].ToolCalls) != 1 {
		t.Errorf("bad assistant message: %+v", msgs[2])
	}
	if msgs[2].ToolCalls[0].Function.Arguments != `{"path":"a.txt"}` {
		t.Errorf("bad tool args: %s", msgs[2].ToolCalls[0].Function.Arguments)
	}
	if msgs[3].Role != "tool" || msgs[3].ToolCallID != "call-1" || msgs[3].Content != `{"ok":true}` {
		t.Errorf("bad tool message: %+v", msgs[3])
	}
}

func TestParseOpenAI(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"run_command","arguments":"{\"command\":\"ls\"}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
	res, err := parseOpenAI(body, 200)
	if err != nil {
		t.Fatalf("parseOpenAI: %v", err)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "run_command" {
		t.Fatalf("bad tool calls: %+v", res.ToolCalls)
	}
	if res.ToolCalls[0].Args["command"] != "ls" {
		t.Fatalf("bad args: %+v", res.ToolCalls[0].Args)
	}
	if res.Usage.TotalTokens != 15 {
		t.Fatalf("bad usage: %+v", res.Usage)
	}
}

func TestParseOpenAITextAndError(t *testing.T) {
	text, err := parseOpenAI([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`), 200)
	if err != nil {
		t.Fatalf("text parse: %v", err)
	}
	if text.Text != "done" {
		t.Fatalf("bad text: %q", text.Text)
	}

	_, err = parseOpenAI([]byte(`{"error":{"message":"invalid api key","type":"invalid_request_error"}}`), 401)
	if err == nil {
		t.Fatal("expected an APIError for a 401")
	}
	if _, ok := err.(*APIError); !ok {
		t.Fatalf("expected *APIError, got %T", err)
	}
}
