package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"kenosm/internal/transport"
)

// OpenAI is a client for any OpenAI-compatible Chat Completions API (DeepSeek
// and others). It routes every call through the configured SOCKS5 proxy and
// retries transient failures, mirroring the Gemini client's behaviour.
type OpenAI struct {
	baseURL         string
	apiKey          string
	client          *http.Client
	maxRetries      int
	maxOutputTokens int
	fallbackModels  []string
	curModel        string
	mu              sync.Mutex // guards curModel
}

// NewOpenAI builds an OpenAI-compatible client. BaseURL is the provider root
// (e.g. https://api.deepseek.com); the client POSTs to <base>/chat/completions.
func NewOpenAI(cfg Config) (*OpenAI, error) {
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		return nil, fmt.Errorf("llm: model is required")
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = "https://api.deepseek.com"
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 180 * time.Second
	}
	retries := cfg.MaxRetries
	if retries <= 0 {
		retries = 3
	}
	maxTokens := cfg.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = 8192
	}

	var client *http.Client
	if cfg.Transport != nil {
		client = &http.Client{Transport: cfg.Transport, Timeout: timeout}
	} else {
		tr, err := transport.New(cfg.Proxy)
		if err != nil {
			return nil, err
		}
		client = &http.Client{Transport: tr, Timeout: timeout}
	}

	fallbacks := make([]string, 0, len(cfg.FallbackModels))
	for _, m := range cfg.FallbackModels {
		m = strings.TrimSpace(m)
		if m != "" && m != model {
			fallbacks = append(fallbacks, m)
		}
	}

	return &OpenAI{
		baseURL:         base,
		apiKey:          cfg.APIKey,
		client:          client,
		maxRetries:      retries,
		maxOutputTokens: maxTokens,
		fallbackModels:  fallbacks,
		curModel:        model,
	}, nil
}

// Model returns the model the client is currently using.
func (o *OpenAI) Model() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.curModel
}

// Generate sends a tool-use session to the Chat Completions endpoint. It
// downgrades to the next fallback model on quota exhaustion, keeping the same
// API key and proxy.
func (o *OpenAI) Generate(ctx context.Context, system string, history []Turn, tools []Tool) (GenerateResult, error) {
	endpoint := o.baseURL + "/chat/completions"
	models := o.modelChain()
	var lastErr error
	for _, m := range models {
		if ctx.Err() != nil {
			return GenerateResult{}, ctx.Err()
		}
		req := openaiRequest{
			Model:       m,
			Messages:    toOpenAIMessages(system, history),
			Tools:       toOpenAITools(tools),
			Temperature: 0.4,
			MaxTokens:   o.maxOutputTokens,
		}
		body, err := json.Marshal(req)
		if err != nil {
			return GenerateResult{}, err
		}
		res, err := o.generateAt(ctx, endpoint, body)
		if err == nil {
			o.stickModel(m)
			return res, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return GenerateResult{}, ctx.Err()
		}
		if !isDowngradeTrigger(err) {
			return GenerateResult{}, err
		}
	}
	return GenerateResult{}, fmt.Errorf("all models exhausted: %w", lastErr)
}

func (o *OpenAI) modelChain() []string {
	o.mu.Lock()
	cur := o.curModel
	o.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, m := range append([]string{cur}, o.fallbackModels...) {
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

func (o *OpenAI) stickModel(m string) {
	o.mu.Lock()
	o.curModel = m
	o.mu.Unlock()
}

func (o *OpenAI) generateAt(ctx context.Context, endpoint string, body []byte) (GenerateResult, error) {
	var lastErr error
	for attempt := 0; attempt < o.maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return GenerateResult{}, ctx.Err()
			case <-time.After(backoff(attempt)):
			}
		}
		res, err := o.doOnce(ctx, endpoint, body)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if !isRetryable(err) {
			return GenerateResult{}, err
		}
	}
	return GenerateResult{}, fmt.Errorf("giving up after %d attempts: %w", o.maxRetries, lastErr)
}

func (o *OpenAI) doOnce(ctx context.Context, endpoint string, body []byte) (GenerateResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return GenerateResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return GenerateResult{}, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return GenerateResult{}, err
	}
	return parseOpenAI(data, resp.StatusCode)
}

// ---- wire types ----

type openaiRequest struct {
	Model       string          `json:"model"`
	Messages    []openaiMessage `json:"messages"`
	Tools       []openaiTool    `json:"tools,omitempty"`
	Temperature float64         `json:"temperature,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
}

type openaiMessage struct {
	Role       string          `json:"role"`
	Content    any             `json:"content,omitempty"`
	ToolCalls  []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type openaiToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openaiFunctionCall `json:"function"`
}

type openaiFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openaiContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *openaiImageURL `json:"image_url,omitempty"`
}

type openaiImageURL struct {
	URL string `json:"url"`
}

type openaiTool struct {
	Type     string         `json:"type"`
	Function openaiFunction `json:"function"`
}

type openaiFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type openaiResponse struct {
	Choices []struct {
		Message struct {
			Role      string          `json:"role"`
			Content   string          `json:"content"`
			ToolCalls []openaiToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
}

func toOpenAIMessages(system string, history []Turn) []openaiMessage {
	var msgs []openaiMessage
	if system != "" {
		msgs = append(msgs, openaiMessage{Role: "system", Content: system})
	}
	for _, t := range history {
		if t.Role == "model" {
			m := openaiMessage{Role: "assistant"}
			if t.Text != "" {
				m.Content = t.Text
			}
			for _, c := range t.ToolCalls {
				args, _ := json.Marshal(c.Args)
				if args == nil {
					args = []byte("{}")
				}
				m.ToolCalls = append(m.ToolCalls, openaiToolCall{ID: c.ID, Type: "function", Function: openaiFunctionCall{Name: c.Name, Arguments: string(args)}})
			}
			msgs = append(msgs, m)
			continue
		}
		if len(t.ToolResponses) > 0 {
			for _, r := range t.ToolResponses {
				content, _ := json.Marshal(r.Response)
				if content == nil {
					content = []byte("{}")
				}
				msgs = append(msgs, openaiMessage{Role: "tool", ToolCallID: r.ID, Content: string(content)})
			}
			continue
		}
		if len(t.Images) > 0 {
			var parts []openaiContentPart
			if t.Text != "" {
				parts = append(parts, openaiContentPart{Type: "text", Text: t.Text})
			}
			for _, img := range t.Images {
				parts = append(parts, openaiContentPart{Type: "image_url", ImageURL: &openaiImageURL{URL: "data:" + img.MimeType + ";base64," + base64.StdEncoding.EncodeToString(img.Data)}})
			}
			msgs = append(msgs, openaiMessage{Role: "user", Content: parts})
			continue
		}
		msgs = append(msgs, openaiMessage{Role: "user", Content: t.Text})
	}
	return msgs
}

func toOpenAITools(tools []Tool) []openaiTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]openaiTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, openaiTool{Type: "function", Function: openaiFunction{Name: t.Name, Description: t.Description, Parameters: t.Parameters}})
	}
	return out
}

func parseOpenAI(data []byte, statusCode int) (GenerateResult, error) {
	var or openaiResponse
	if err := json.Unmarshal(data, &or); err != nil {
		if statusCode >= 400 {
			msg := strings.Join(strings.Fields(string(data)), " ")
			if len(msg) > 300 {
				msg = msg[:300] + "..."
			}
			if msg == "" {
				msg = http.StatusText(statusCode)
			}
			return GenerateResult{}, &APIError{StatusCode: statusCode, Code: statusCode, Status: http.StatusText(statusCode), Message: msg}
		}
		return GenerateResult{}, fmt.Errorf("decode openai response (status %d): %w", statusCode, err)
	}
	if or.Error != nil {
		return GenerateResult{}, &APIError{StatusCode: statusCode, Code: statusCode, Status: or.Error.Type, Message: or.Error.Message}
	}
	if len(or.Choices) == 0 {
		if statusCode >= 400 {
			return GenerateResult{}, &APIError{StatusCode: statusCode, Code: statusCode, Status: http.StatusText(statusCode), Message: "no choices in error response"}
		}
		return GenerateResult{}, fmt.Errorf("openai returned no choices (status %d)", statusCode)
	}

	msg := or.Choices[0].Message
	var res GenerateResult
	res.Text = msg.Content
	for _, tc := range msg.ToolCalls {
		var args map[string]any
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			args = map[string]any{"_raw": tc.Function.Arguments}
		}
		res.ToolCalls = append(res.ToolCalls, FunctionCall{Name: tc.Function.Name, Args: args, ID: tc.ID})
	}
	if or.Usage != nil {
		res.Usage = Usage{
			PromptTokens:     or.Usage.PromptTokens,
			CompletionTokens: or.Usage.CompletionTokens,
			TotalTokens:      or.Usage.TotalTokens,
		}
	}
	if strings.TrimSpace(res.Text) == "" && len(res.ToolCalls) == 0 {
		return GenerateResult{}, fmt.Errorf("openai returned empty response (status %d)", statusCode)
	}
	return res, nil
}
