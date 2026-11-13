package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"kenosm/internal/browser"
	"kenosm/internal/llm"
	"kenosm/internal/workspace"
)

// Config configures an autonomous tool-using agent.
type Config struct {
	LLM           *llm.Gemini
	Workspace     *workspace.Workspace
	Browser       *browser.Browser // optional; enables the browser_* tools
	MaxIterations int
	Log           func(kind, detail string) // optional progress callback
	OnUsage       func(llm.Usage)           // optional: fired after each model call with its token usage
	SystemExtra   string                    // optional: appended to the agent's system prompt (e.g. a shared contract)
}

// Result is what an agent did during a run.
type Result struct {
	Summary    string    `json:"summary"`
	ToolCalls  []string  `json:"tool_calls"`
	Files      []string  `json:"files"` // workspace-relative paths written/edited
	Iterations int       `json:"iterations"`
	Usage      llm.Usage `json:"usage"`
}

// Agent is a tool-using loop: it repeatedly asks the model, executes any tool
// calls (write files, read files, list files, run commands, drive a browser),
// and feeds results back until the model finishes with a summary.
type Agent struct {
	llm         *llm.Gemini
	ws          *workspace.Workspace
	browser     *browser.Browser
	session     *browser.Session
	runCtx      context.Context
	maxIt       int
	tools       []llm.Tool
	log         func(kind, detail string)
	onUsage     func(llm.Usage)
	systemExtra string
	files       []string // paths written/edited during the run
}

func New(cfg Config) *Agent {
	maxIt := cfg.MaxIterations
	if maxIt <= 0 {
		maxIt = 15
	}
	tools := buildTools()
	if cfg.Browser == nil {
		tools = withoutPrefix(tools, "browser_")
	}
	return &Agent{llm: cfg.LLM, ws: cfg.Workspace, browser: cfg.Browser, maxIt: maxIt, tools: tools, log: cfg.Log, onUsage: cfg.OnUsage, systemExtra: cfg.SystemExtra}
}

func (a *Agent) Run(ctx context.Context, task string, images []llm.Image) (Result, error) {
	return a.RunWithHistory(ctx, []llm.Turn{{Role: "user", Text: task, Images: images}})
}

// RunWithHistory runs the agent loop over a multi-turn conversation. The passed
// history already includes the current user turn (with any images), so chat
// sessions can replay prior turns as context instead of starting from scratch.
func (a *Agent) RunWithHistory(ctx context.Context, history []llm.Turn) (Result, error) {
	a.runCtx = ctx
	defer func() {
		if a.session != nil {
			a.session.Close()
			a.session = nil
		}
	}()

	system := `You are an autonomous coding agent with access to a shared workspace directory and a real web browser.
You have tools to write files, read files, list files, edit files, search the workspace, run shell commands, and drive the browser: navigate to any site, click buttons, type into fields, scroll, read page text, and capture screenshots.
Use tools only when the task actually requires them. For analytical, descriptive, or question-answering tasks (for example describing an attached image or explaining something), answer directly in your response without calling any tools.
When you create or change files, actually test them with the run_command tool before finishing.
When you build a web page or any UI, verify it in the browser before finishing: open it with browser_navigate, then click every button and link, scroll the page, and take a screenshot of each step (browser_click and browser_scroll save a screenshot automatically, so you can see each state). Confirm from those screenshots that everything works and looks right. If a page shows a CAPTCHA or similar challenge, read it with browser_snapshot or browser_screenshot and type the answer with browser_type.
All file paths are relative to the workspace root. Never prefix a path with "workspace/" or an absolute directory — write "app/index.html", not "workspace/app/index.html".
When finished, output a concise summary of what you did and the result.`

	if a.systemExtra != "" {
		system += "\n\n" + a.systemExtra
	}

	var summary strings.Builder
	var callLog []string
	var usage llm.Usage
	iterations := 0
	lastSig := ""
	repeat := 0

	for iterations < a.maxIt {
		iterations++
		if a.log != nil {
			a.log("model", "generating response")
		}
		res, err := a.llm.Generate(ctx, system, history, a.tools)
		if err != nil {
			return Result{}, err
		}
		if a.onUsage != nil {
			a.onUsage(res.Usage)
		}
		usage.PromptTokens += res.Usage.PromptTokens
		usage.CompletionTokens += res.Usage.CompletionTokens
		usage.ThoughtTokens += res.Usage.ThoughtTokens
		usage.TotalTokens += res.Usage.TotalTokens
		if len(res.ToolCalls) == 0 {
			if res.Text != "" {
				summary.WriteString(res.Text)
			}
			break
		}

		// Guard against the model looping on the same action (e.g. re-fetching
		// its own server) instead of finishing the subtask.
		sig := toolSignature(res.ToolCalls)
		if sig == lastSig {
			repeat++
		} else {
			lastSig = sig
			repeat = 1
		}
		if repeat >= 3 {
			note := "stopped early: repeated the same tool calls, likely stuck in a loop"
			if summary.Len() == 0 {
				summary.WriteString(note)
			} else {
				summary.WriteString("\n\n" + note)
			}
			break
		}

		history = append(history, llm.Turn{Role: "model", Text: res.Text, ToolCalls: res.ToolCalls})

		var responses []llm.FunctionResponse
		for _, c := range res.ToolCalls {
			callLog = append(callLog, c.Name)
			responses = append(responses, a.execute(ctx, c))
			if a.log != nil {
				a.log("tool", describeTool(c))
			}
		}
		history = append(history, llm.Turn{Role: "user", ToolResponses: responses})
	}

	return Result{Summary: summary.String(), ToolCalls: callLog, Files: dedupe(a.files), Iterations: iterations, Usage: usage}, nil
}

// dedupe returns a copy of s with duplicates removed, preserving first-seen order.
func dedupe(s []string) []string {
	seen := make(map[string]bool, len(s))
	out := make([]string, 0, len(s))
	for _, v := range s {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// toolSignature produces a stable string for a set of tool calls so the loop
// can detect when the model repeats itself.
func toolSignature(calls []llm.FunctionCall) string {
	var b strings.Builder
	for _, c := range calls {
		b.WriteString(c.Name)
		b.WriteByte('(')
		if j, err := json.Marshal(c.Args); err == nil {
			b.Write(j)
		}
		b.WriteByte(')')
	}
	return b.String()
}

func (a *Agent) execute(ctx context.Context, c llm.FunctionCall) llm.FunctionResponse {
	resp := func(m map[string]any) llm.FunctionResponse {
		return llm.FunctionResponse{Name: c.Name, Response: m, ID: c.ID, ThoughtSignature: c.ThoughtSignature}
	}
	switch c.Name {
	case "write_file":
		path := cleanRelPath(c.Args["path"])
		content, _ := c.Args["content"].(string)
		if err := a.ws.WriteFile(path, content); err != nil {
			return resp(map[string]any{"error": err.Error()})
		}
		a.noteFile(path)
		return resp(map[string]any{"ok": true, "path": path})
	case "read_file":
		path := cleanRelPath(c.Args["path"])
		data, err := a.ws.ReadFile(path)
		if err != nil {
			return resp(map[string]any{"error": err.Error()})
		}
		return resp(map[string]any{"content": data})
	case "list_files":
		files, err := a.ws.ListFiles()
		if err != nil {
			return resp(map[string]any{"error": err.Error()})
		}
		names := make([]string, 0, len(files))
		for _, f := range files {
			names = append(names, f.Path)
		}
		return resp(map[string]any{"files": names})
	case "run_command":
		command, _ := c.Args["command"].(string)
		out, code, err := a.ws.RunCommand(ctx, command)
		m := map[string]any{"output": out, "exit_code": code}
		if err != nil {
			m["error"] = err.Error()
		}
		return resp(m)
	case "edit_file":
		path := cleanRelPath(c.Args["path"])
		oldS, _ := c.Args["old_string"].(string)
		newS, _ := c.Args["new_string"].(string)
		if path == "" || oldS == "" {
			return resp(map[string]any{"error": "edit_file requires path and old_string"})
		}
		n, err := a.ws.EditFile(path, oldS, newS)
		if err != nil {
			return resp(map[string]any{"error": err.Error()})
		}
		a.noteFile(path)
		return resp(map[string]any{"ok": true, "path": path, "replaced": n})
	case "search_files":
		query, _ := c.Args["query"].(string)
		if query == "" {
			return resp(map[string]any{"error": "search_files requires a query"})
		}
		matches, err := a.ws.Search(query)
		if err != nil {
			return resp(map[string]any{"error": err.Error()})
		}
		return resp(map[string]any{"matches": matches, "count": len(matches)})

	// ---- browser tools ----
	case "browser_navigate":
		target := strVal(c.Args["url"])
		if target == "" {
			return resp(map[string]any{"error": "browser_navigate requires a url"})
		}
		s, err := a.bsession()
		if err != nil {
			return resp(map[string]any{"error": err.Error()})
		}
		if err := s.Navigate(a.resolveTarget(target)); err != nil {
			return resp(map[string]any{"error": err.Error()})
		}
		return a.shotResp(c, s, map[string]any{"ok": true, "url": target})
	case "browser_snapshot":
		s, err := a.bsession()
		if err != nil {
			return resp(map[string]any{"error": err.Error()})
		}
		els, err := s.Snapshot()
		if err != nil {
			return resp(map[string]any{"error": err.Error()})
		}
		return resp(map[string]any{"elements": els, "count": len(els)})
	case "browser_click":
		target := browser.SelectorFor(strVal(c.Args["target"]))
		if target == "" {
			return resp(map[string]any{"error": "browser_click requires a target (index from browser_snapshot, or a CSS selector)"})
		}
		s, err := a.bsession()
		if err != nil {
			return resp(map[string]any{"error": err.Error()})
		}
		if err := s.Click(target); err != nil {
			return resp(map[string]any{"error": err.Error()})
		}
		return a.shotResp(c, s, map[string]any{"ok": true, "clicked": target})
	case "browser_type":
		target := browser.SelectorFor(strVal(c.Args["target"]))
		text := strVal(c.Args["text"])
		if target == "" {
			return resp(map[string]any{"error": "browser_type requires a target"})
		}
		s, err := a.bsession()
		if err != nil {
			return resp(map[string]any{"error": err.Error()})
		}
		if err := s.Type(target, text); err != nil {
			return resp(map[string]any{"error": err.Error()})
		}
		return a.shotResp(c, s, map[string]any{"ok": true, "typed_into": target})
	case "browser_scroll":
		amount := intVal(c.Args["amount"], 600)
		if strings.EqualFold(strVal(c.Args["direction"]), "up") {
			amount = -amount
		}
		s, err := a.bsession()
		if err != nil {
			return resp(map[string]any{"error": err.Error()})
		}
		if err := s.Scroll(amount); err != nil {
			return resp(map[string]any{"error": err.Error()})
		}
		return a.shotResp(c, s, map[string]any{"ok": true, "scrolled_by": amount})
	case "browser_screenshot":
		s, err := a.bsession()
		if err != nil {
			return resp(map[string]any{"error": err.Error()})
		}
		return a.shotResp(c, s, map[string]any{"ok": true})
	case "browser_text":
		s, err := a.bsession()
		if err != nil {
			return resp(map[string]any{"error": err.Error()})
		}
		text, err := s.Text()
		if err != nil {
			return resp(map[string]any{"error": err.Error()})
		}
		return resp(map[string]any{"text": text})
	default:
		return resp(map[string]any{"error": "unknown tool: " + c.Name})
	}
}

// bsession returns the run's shared browser session, creating it on first use.
func (a *Agent) bsession() (*browser.Session, error) {
	if a.session != nil {
		return a.session, nil
	}
	if a.browser == nil {
		return nil, fmt.Errorf("no browser available")
	}
	s, err := a.browser.NewSession(a.runCtx)
	if err != nil {
		return nil, err
	}
	a.session = s
	return s, nil
}

// shotResp captures the current viewport, saves it to the workspace and returns
// a tool response carrying both the saved path and the image so the model can
// visually inspect the result of the action it just performed.
func (a *Agent) shotResp(c llm.FunctionCall, s *browser.Session, meta map[string]any) llm.FunctionResponse {
	img, err := s.Screenshot()
	if err != nil {
		return llm.FunctionResponse{Name: c.Name, Response: map[string]any{"error": err.Error()}, ID: c.ID, ThoughtSignature: c.ThoughtSignature}
	}
	path := fmt.Sprintf("screenshots/shot-%d.png", time.Now().UnixNano())
	if werr := a.ws.WriteFile(path, string(img)); werr != nil {
		return llm.FunctionResponse{Name: c.Name, Response: map[string]any{"error": "save screenshot: " + werr.Error()}, ID: c.ID, ThoughtSignature: c.ThoughtSignature}
	}
	a.noteFile(path)
	meta["screenshot"] = path
	return llm.FunctionResponse{Name: c.Name, Response: meta, ID: c.ID, ThoughtSignature: c.ThoughtSignature, ImageMimeType: "image/png", ImageData: img}
}

// resolveTarget turns a URL or workspace-relative file path into something the
// browser can load (http(s)/file URL).
func (a *Agent) resolveTarget(t string) string {
	t = strings.TrimSpace(t)
	if strings.HasPrefix(t, "http://") || strings.HasPrefix(t, "https://") || strings.HasPrefix(t, "file://") {
		return t
	}
	abs := filepath.Join(a.ws.Root(), filepath.FromSlash(strings.TrimPrefix(t, "./")))
	return "file://" + filepath.ToSlash(abs)
}

// noteFile records a workspace-relative path that this agent wrote or edited.
func (a *Agent) noteFile(path string) {
	if path != "" {
		a.files = append(a.files, path)
	}
}

// cleanRelPath trims common model mistakes from a workspace-relative path: a
// leading "workspace/" prefix, "./", surrounding whitespace, backslashes and a
// leading "/". The workspace re-validates the result and rejects escapes.
func cleanRelPath(v any) string {
	p, _ := v.(string)
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	p = strings.TrimPrefix(p, "./")
	for strings.HasPrefix(p, "/") {
		p = strings.TrimPrefix(p, "/")
	}
	for strings.HasPrefix(p, "workspace/") {
		p = strings.TrimPrefix(p, "workspace/")
	}
	if p == "workspace" {
		return ""
	}
	return p
}

func strVal(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func intVal(v any, def int) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return def
}

func describeTool(c llm.FunctionCall) string {
	switch c.Name {
	case "write_file":
		if p, ok := c.Args["path"].(string); ok {
			return "write_file " + p
		}
	case "read_file":
		if p, ok := c.Args["path"].(string); ok {
			return "read_file " + p
		}
	case "run_command":
		if cmd, ok := c.Args["command"].(string); ok {
			return "run_command " + cmd
		}
	case "edit_file":
		if p, ok := c.Args["path"].(string); ok {
			return "edit_file " + p
		}
	case "search_files":
		if q, ok := c.Args["query"].(string); ok {
			return "search_files " + q
		}
	case "browser_navigate":
		return "browser_navigate " + strVal(c.Args["url"])
	case "browser_click":
		return "browser_click " + strVal(c.Args["target"])
	case "browser_type":
		return "browser_type " + strVal(c.Args["target"])
	case "browser_scroll":
		return "browser_scroll " + strVal(c.Args["direction"])
	case "browser_screenshot":
		return "browser_screenshot"
	case "browser_snapshot":
		return "browser_snapshot"
	case "browser_text":
		return "browser_text"
	}
	return c.Name
}

func buildTools() []llm.Tool {
	return []llm.Tool{
		{
			Name:        "write_file",
			Description: "Create or overwrite a file in the workspace.",
			Parameters: map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"path":    map[string]any{"type": "STRING", "description": "Relative file path inside the workspace (do NOT prefix with 'workspace/')"},
					"content": map[string]any{"type": "STRING", "description": "Full file content"},
				},
				"required": []any{"path", "content"},
			},
		},
		{
			Name:        "read_file",
			Description: "Read the content of a file in the workspace.",
			Parameters: map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"path": map[string]any{"type": "STRING", "description": "Relative file path inside the workspace (do NOT prefix with 'workspace/')"},
				},
				"required": []any{"path"},
			},
		},
		{
			Name:        "list_files",
			Description: "List all files in the workspace.",
			Parameters: map[string]any{
				"type":       "OBJECT",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "run_command",
			Description: "Run a shell command inside the workspace directory and return stdout/stderr and exit code. Use it to test your work.",
			Parameters: map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"command": map[string]any{"type": "STRING", "description": "Shell command to run"},
				},
				"required": []any{"command"},
			},
		},
		{
			Name:        "edit_file",
			Description: "Replace a substring inside a file with new text, without rewriting the whole file. Use it for targeted fixes. Returns how many replacements were made.",
			Parameters: map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"path":       map[string]any{"type": "STRING", "description": "File path inside the workspace (do NOT prefix with 'workspace/')"},
					"old_string": map[string]any{"type": "STRING", "description": "Exact text to find (must exist in the file)"},
					"new_string": map[string]any{"type": "STRING", "description": "Replacement text"},
				},
				"required": []any{"path", "old_string", "new_string"},
			},
		},
		{
			Name:        "search_files",
			Description: "Search all files in the workspace for a text query (case-insensitive) and return matching lines as 'path:line: text'.",
			Parameters: map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"query": map[string]any{"type": "STRING", "description": "Text to search for"},
				},
				"required": []any{"query"},
			},
		},
		{
			Name:        "browser_navigate",
			Description: "Open a URL (or a workspace-relative file path) in the browser. Returns a screenshot of the loaded page.",
			Parameters: map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"url": map[string]any{"type": "STRING", "description": "http(s) URL to open, or a local file path inside the workspace"},
				},
				"required": []any{"url"},
			},
		},
		{
			Name:        "browser_snapshot",
			Description: "List the visible interactive elements on the current page (buttons, links, inputs) as numbered entries. Use the numbers as targets for browser_click / browser_type.",
			Parameters: map[string]any{
				"type":       "OBJECT",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "browser_click",
			Description: "Click an element on the page: pass its index from browser_snapshot (e.g. \"3\") or a CSS selector (e.g. \"#submit\"). Returns a screenshot taken right after the click.",
			Parameters: map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"target": map[string]any{"type": "STRING", "description": "Element index from browser_snapshot, or a CSS selector"},
				},
				"required": []any{"target"},
			},
		},
		{
			Name:        "browser_type",
			Description: "Type text into an input/textarea/field: pass its index from browser_snapshot or a CSS selector. Returns a screenshot after typing.",
			Parameters: map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"target": map[string]any{"type": "STRING", "description": "Element index from browser_snapshot, or a CSS selector"},
					"text":   map[string]any{"type": "STRING", "description": "Text to type"},
				},
				"required": []any{"target", "text"},
			},
		},
		{
			Name:        "browser_scroll",
			Description: "Scroll the page up or down. Returns a screenshot of the new viewport so you can verify scrolled content.",
			Parameters: map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"direction": map[string]any{"type": "STRING", "description": "\"down\" or \"up\""},
					"amount":    map[string]any{"type": "NUMBER", "description": "Pixels to scroll (default 600)"},
				},
				"required": []any{"direction"},
			},
		},
		{
			Name:        "browser_screenshot",
			Description: "Capture a screenshot of the current browser viewport.",
			Parameters: map[string]any{
				"type":       "OBJECT",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "browser_text",
			Description: "Return the visible text of the current page.",
			Parameters: map[string]any{
				"type":       "OBJECT",
				"properties": map[string]any{},
			},
		},
	}
}

// withoutPrefix returns a copy of tools with any tool whose name has the given
// prefix removed.
func withoutPrefix(tools []llm.Tool, prefix string) []llm.Tool {
	out := make([]llm.Tool, 0, len(tools))
	for _, t := range tools {
		if strings.HasPrefix(t.Name, prefix) {
			continue
		}
		out = append(out, t)
	}
	return out
}
