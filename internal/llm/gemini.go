package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"kenosm/internal/transport"
)

// Config configures a Gemini client.
type Config struct {
	BaseURL         string
	APIKey          string
	Model           string
	FallbackModels  []string // ordered lighter-model downgrade targets on quota exhaustion
	Proxy           transport.ProxySpec
	Transport       http.RoundTripper // optional; overrides the SOCKS5 proxy transport (tests)
	Timeout         time.Duration
	MaxRetries      int
	MaxOutputTokens int
}

// Gemini is a client for the Gemini generateContent REST API that routes every
// call through a SOCKS5 proxy and retries transient failures. When a model is
// out of quota (429 RESOURCE_EXHAUSTED) it transparently downgrades to the next
// lighter model in FallbackModels, keeping the same API key and proxy.
type Gemini struct {
	baseURL         string
	apiKey          string
	client          *http.Client
	maxRetries      int
	maxOutputTokens int
	fallbackModels  []string
	curModel        string
	mu              sync.Mutex // guards curModel
}

// APIError is a non-2xx error returned by the Gemini API.
type APIError struct {
	StatusCode int
	Code       int
	Status     string
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("gemini error %d (%s): %s", e.Code, e.Status, e.Message)
}

// Tool describes a callable function exposed to the model.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// FunctionCall is a request from the model to invoke a tool.
type FunctionCall struct {
	Name             string
	Args             map[string]any
	ID               string
	ThoughtSignature string
}

// FunctionResponse is the result of a tool invocation. When a tool produces a
// visual result (e.g. a screenshot), ImageMimeType/ImageData carry it so the
// multimodal model can inspect the image.
type FunctionResponse struct {
	Name             string
	Response         map[string]any
	ID               string
	ThoughtSignature string
	ImageMimeType    string
	ImageData        []byte
}

// Turn is one message in a multi-turn tool-use session.
type Turn struct {
	Role          string // "user" or "model"
	Text          string
	ToolCalls     []FunctionCall
	ToolResponses []FunctionResponse
	Images        []Image // inline images for a user turn (multimodal input)
}

// Image is an inline image attached to a user message.
type Image struct {
	MimeType string
	Data     []byte
}

// GenerateResult is the model output for one generation step.
type GenerateResult struct {
	Text      string
	ToolCalls []FunctionCall
	Usage     Usage
}

// Usage is the token accounting for one generation, as reported by Gemini.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	ThoughtTokens    int `json:"thought_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func New(cfg Config) (*Gemini, error) {
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		return nil, fmt.Errorf("llm: model is required")
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = "https://generativelanguage.googleapis.com/v1beta"
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	retries := cfg.MaxRetries
	if retries <= 0 {
		retries = 3
	}
	maxTokens := cfg.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = 32768
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

	return &Gemini{
		baseURL:         base,
		apiKey:          cfg.APIKey,
		client:          client,
		maxRetries:      retries,
		maxOutputTokens: maxTokens,
		fallbackModels:  fallbacks,
		curModel:        model,
	}, nil
}

// Text is a convenience for a single-turn, no-tools generation.
func (g *Gemini) Text(ctx context.Context, system, prompt string) (string, error) {
	res, err := g.Generate(ctx, system, []Turn{{Role: "user", Text: prompt}}, nil)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(res.Text) == "" {
		return "", fmt.Errorf("gemini returned empty text")
	}
	return res.Text, nil
}

// Generate sends a session (history) with optional tools and returns the
// model's text and any tool calls. It uses non-streaming generateContent,
// retries transient errors, and on quota exhaustion (429 RESOURCE_EXHAUSTED)
// downgrades to the next lighter model without dropping the request's API key
// or proxy.
func (g *Gemini) Generate(ctx context.Context, system string, history []Turn, tools []Tool) (GenerateResult, error) {
	req := generateRequest{
		Contents:         toContents(history),
		GenerationConfig: genConfig{Temperature: 0.4, MaxOutputTokens: g.maxOutputTokens},
		Tools:            toTools(tools),
	}
	if system != "" {
		req.SystemInstruction = &content{Parts: []part{{Text: system}}}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return GenerateResult{}, err
	}

	models := g.modelChain()
	var lastErr error
	for _, m := range models {
		endpoint := fmt.Sprintf("%s/models/%s:generateContent", g.baseURL, m)
		res, err := g.generateAt(ctx, endpoint, body)
		if err == nil {
			g.stickModel(m)
			return res, nil
		}
		lastErr = err
		if !isDowngradeTrigger(err) {
			return GenerateResult{}, err
		}
	}
	return GenerateResult{}, fmt.Errorf("all models exhausted: %w", lastErr)
}

// modelChain returns the ordered models to try for one generation: the
// currently-sticky model first, then the lighter fallbacks (deduped).
func (g *Gemini) modelChain() []string {
	g.mu.Lock()
	cur := g.curModel
	g.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, m := range append([]string{cur}, g.fallbackModels...) {
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// stickModel remembers the model that last succeeded, so subsequent generations
// in the same run skip straight to it instead of re-hitting the saturated model.
func (g *Gemini) stickModel(m string) {
	g.mu.Lock()
	g.curModel = m
	g.mu.Unlock()
}

// Model returns the model the client is currently using.
func (g *Gemini) Model() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.curModel
}

// generateAt retries a single model's endpoint for transient failures. Quota
// exhaustion (429) is NOT retried here — it bubbles up so Generate can downgrade
// to a lighter model.
func (g *Gemini) generateAt(ctx context.Context, endpoint string, body []byte) (GenerateResult, error) {
	var lastErr error
	for attempt := 0; attempt < g.maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return GenerateResult{}, ctx.Err()
			case <-time.After(backoff(attempt)):
			}
		}
		res, err := g.doOnce(ctx, endpoint, body)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if !isRetryable(err) {
			return GenerateResult{}, err
		}
	}
	return GenerateResult{}, fmt.Errorf("giving up after %d attempts: %w", g.maxRetries, lastErr)
}

func (g *Gemini) doOnce(ctx context.Context, endpoint string, body []byte) (GenerateResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return GenerateResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", g.apiKey)

	resp, err := g.client.Do(httpReq)
	if err != nil {
		return GenerateResult{}, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return GenerateResult{}, err
	}
	return parseGenerate(data, resp.StatusCode)
}

// ---- wire types ----

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

type part struct {
	Text             string                `json:"text,omitempty"`
	FunctionCall     *functionCallPart     `json:"functionCall,omitempty"`
	FunctionResponse *functionResponsePart `json:"functionResponse,omitempty"`
	InlineData       *inlineDataPart       `json:"inlineData,omitempty"`
	// ThoughtSignature is emitted by Gemini 3.x thinking models as a sibling of
	// functionCall/functionResponse (camelCase, at part level, NOT nested inside
	// functionCall). It must be echoed back verbatim in subsequent turns or the
	// API rejects the request with 400 "missing a thought_signature".
	ThoughtSignature string `json:"thoughtSignature,omitempty"`
}

type inlineDataPart struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"` // base64-encoded bytes
}

type functionCallPart struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
	ID   string         `json:"id,omitempty"`
}

type functionResponsePart struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
	ID       string         `json:"id,omitempty"`
}

type functionDeclaration struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type toolDecl struct {
	FunctionDeclarations []functionDeclaration `json:"functionDeclarations"`
}

type generateRequest struct {
	Contents          []content  `json:"contents"`
	SystemInstruction *content   `json:"systemInstruction,omitempty"`
	GenerationConfig  genConfig  `json:"generationConfig,omitempty"`
	Tools             []toolDecl `json:"tools,omitempty"`
}

type genConfig struct {
	Temperature     float64 `json:"temperature,omitempty"`
	MaxOutputTokens int     `json:"maxOutputTokens,omitempty"`
}

type generateResponse struct {
	Candidates []struct {
		Content struct {
			Parts []part `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
	UsageMetadata *struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
		ThoughtsTokenCount   int `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
}

func toContents(history []Turn) []content {
	var cs []content
	for _, t := range history {
		if t.Role == "model" {
			var parts []part
			if t.Text != "" {
				parts = append(parts, part{Text: t.Text})
			}
			for _, c := range t.ToolCalls {
				parts = append(parts, part{FunctionCall: &functionCallPart{Name: c.Name, Args: c.Args, ID: c.ID}, ThoughtSignature: c.ThoughtSignature})
			}
			cs = append(cs, content{Role: "model", Parts: parts})
			continue
		}
		if len(t.ToolResponses) > 0 {
			var parts []part
			for _, r := range t.ToolResponses {
				parts = append(parts, part{FunctionResponse: &functionResponsePart{Name: r.Name, Response: r.Response, ID: r.ID}, ThoughtSignature: r.ThoughtSignature})
				if len(r.ImageData) > 0 {
					parts = append(parts, part{InlineData: &inlineDataPart{MimeType: r.ImageMimeType, Data: base64.StdEncoding.EncodeToString(r.ImageData)}})
				}
			}
			cs = append(cs, content{Role: "user", Parts: parts})
			continue
		}
		if len(t.Images) > 0 {
			var parts []part
			if t.Text != "" {
				parts = append(parts, part{Text: t.Text})
			}
			for _, img := range t.Images {
				parts = append(parts, part{InlineData: &inlineDataPart{MimeType: img.MimeType, Data: base64.StdEncoding.EncodeToString(img.Data)}})
			}
			cs = append(cs, content{Role: "user", Parts: parts})
			continue
		}
		cs = append(cs, content{Role: "user", Parts: []part{{Text: t.Text}}})
	}
	return cs
}

func toTools(tools []Tool) []toolDecl {
	if len(tools) == 0 {
		return nil
	}
	var ds []functionDeclaration
	for _, t := range tools {
		ds = append(ds, functionDeclaration{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	}
	return []toolDecl{{FunctionDeclarations: ds}}
}

func parseGenerate(data []byte, statusCode int) (GenerateResult, error) {
	var gr generateResponse
	if err := json.Unmarshal(data, &gr); err != nil {
		return GenerateResult{}, fmt.Errorf("decode gemini response (status %d): %w", statusCode, err)
	}
	if gr.Error != nil {
		return GenerateResult{}, &APIError{StatusCode: statusCode, Code: gr.Error.Code, Status: gr.Error.Status, Message: gr.Error.Message}
	}
	if len(gr.Candidates) == 0 {
		return GenerateResult{}, fmt.Errorf("gemini returned no candidates (status %d)", statusCode)
	}

	var res GenerateResult
	var sb strings.Builder
	for _, p := range gr.Candidates[0].Content.Parts {
		if p.FunctionCall != nil {
			res.ToolCalls = append(res.ToolCalls, FunctionCall{Name: p.FunctionCall.Name, Args: p.FunctionCall.Args, ID: p.FunctionCall.ID, ThoughtSignature: p.ThoughtSignature})
		} else if p.Text != "" {
			sb.WriteString(p.Text)
		}
	}
	if gr.UsageMetadata != nil {
		res.Usage = Usage{
			PromptTokens:     gr.UsageMetadata.PromptTokenCount,
			CompletionTokens: gr.UsageMetadata.CandidatesTokenCount,
			ThoughtTokens:    gr.UsageMetadata.ThoughtsTokenCount,
			TotalTokens:      gr.UsageMetadata.TotalTokenCount,
		}
	}
	res.Text = sb.String()
	if strings.TrimSpace(res.Text) == "" && len(res.ToolCalls) == 0 {
		return GenerateResult{}, fmt.Errorf("gemini returned empty response (status %d)", statusCode)
	}
	return res, nil
}

func isRetryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var ae *APIError
	if errors.As(err, &ae) {
		switch ae.Code {
		case 429:
			// Quota exhaustion: handled by model downgrade, not same-model retries.
			return false
		case 500, 502, 503, 504:
			return true
		}
		return false
	}
	return true
}

// isDowngradeTrigger reports whether an error should cause a switch to a
// lighter model — Gemini 429 RESOURCE_EXHAUSTED (quota) errors.
func isDowngradeTrigger(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.StatusCode == 429 || ae.Code == 429 || ae.Status == "RESOURCE_EXHAUSTED"
	}
	return false
}

func backoff(attempt int) time.Duration {
	d := time.Duration(1<<uint(attempt-1)) * time.Second
	if d > 8*time.Second {
		d = 8 * time.Second
	}
	return d
}
