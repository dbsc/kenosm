package agent

import (
	"context"
	"encoding/json"
	"fmt"
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
	Browser       *browser.Browser // optional; enables the screenshot tool
	MaxIterations int
	Log           func(kind, detail string) // optional progress callback
}

// Result is what an agent did during a run.
type Result struct {
	Summary    string    `json:"summary"`
	ToolCalls  []string  `json:"tool_calls"`
	Iterations int       `json:"iterations"`
	Usage      llm.Usage `json:"usage"`
}

// Agent is a tool-using loop: it repeatedly asks the model, executes any tool
// calls (write files, read files, list files, run commands), and feeds results
// back until the model finishes with a summary.
type Agent struct {
	llm     *llm.Gemini
	ws      *workspace.Workspace
	browser *browser.Browser
	maxIt   int
	tools   []llm.Tool
	log     func(kind, detail string)
}

func New(cfg Config) *Agent {
	maxIt := cfg.MaxIterations
	if maxIt <= 0 {
		maxIt = 15
	}
	tools := buildTools()
	if cfg.Browser == nil {
		tools = withoutTool(tools, "screenshot")
	}
	return &Agent{llm: cfg.LLM, ws: cfg.Workspace, browser: cfg.Browser, maxIt: maxIt, tools: tools, log: cfg.Log}
}

func (a *Agent) Run(ctx context.Context, task string, images []llm.Image) (Result, error) {
	system := `You are an autonomous coding agent with access to a shared workspace directory.
You have tools to write files, read files, list files, edit files, search the workspace, capture screenshots, and run shell commands to test your work.
Use tools only when the task actually requires them. For analytical, descriptive, or question-answering tasks (for example describing an attached image or explaining something), answer directly in your response without calling any tools.
When you do create or change files, actually test them with the run_command tool before finishing.
When finished, output a concise summary of what you did and the result.`

	history := []llm.Turn{{Role: "user", Text: task, Images: images}}
	var summary strings.Builder
	var callLog []string
	var usage llm.Usage
	iterations := 0
	lastSig := ""
	repeat := 0

	for iterations < a.maxIt {
		iterations++
		res, err := a.llm.Generate(ctx, system, history, a.tools)
		if err != nil {
			return Result{}, err
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

	return Result{Summary: summary.String(), ToolCalls: callLog, Iterations: iterations, Usage: usage}, nil
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
		path, _ := c.Args["path"].(string)
		content, _ := c.Args["content"].(string)
		if err := a.ws.WriteFile(path, content); err != nil {
			return resp(map[string]any{"error": err.Error()})
		}
		return resp(map[string]any{"ok": true, "path": path})
	case "read_file":
		path, _ := c.Args["path"].(string)
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
	case "screenshot":
		target, _ := c.Args["target"].(string)
		if target == "" {
			target, _ = c.Args["url"].(string)
		}
		if target == "" {
			return resp(map[string]any{"error": "screenshot requires a 'target' (URL or file path)"})
		}
		if a.browser == nil {
			return resp(map[string]any{"error": "no browser available"})
		}
		img, err := a.browser.Screenshot(ctx, target)
		if err != nil {
			return resp(map[string]any{"error": err.Error()})
		}
		rel := fmt.Sprintf("screenshots/shot-%d.png", time.Now().UnixNano())
		if werr := a.ws.WriteFile(rel, string(img)); werr != nil {
			return resp(map[string]any{"error": "save screenshot: " + werr.Error()})
		}
		return llm.FunctionResponse{
			Name:             c.Name,
			Response:         map[string]any{"ok": true, "path": rel},
			ID:               c.ID,
			ThoughtSignature: c.ThoughtSignature,
			ImageMimeType:    "image/png",
			ImageData:        img,
		}
	case "edit_file":
		path, _ := c.Args["path"].(string)
		oldS, _ := c.Args["old_string"].(string)
		newS, _ := c.Args["new_string"].(string)
		if path == "" || oldS == "" {
			return resp(map[string]any{"error": "edit_file requires path and old_string"})
		}
		n, err := a.ws.EditFile(path, oldS, newS)
		if err != nil {
			return resp(map[string]any{"error": err.Error()})
		}
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
	default:
		return resp(map[string]any{"error": "unknown tool: " + c.Name})
	}
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
					"path":    map[string]any{"type": "STRING", "description": "Relative file path inside the workspace"},
					"content": map[string]any{"type": "STRING", "description": "Full file content"},
				},
				"required": []any{"path", "content"},
			},
		},
		{
			Name:        "read_file",
			Description: "Read the content of a file in the workspace.",
			Parameters: map[string]any{
				"type":       "OBJECT",
				"properties": map[string]any{
					"path": map[string]any{"type": "STRING", "description": "Relative file path inside the workspace"},
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
				"type":       "OBJECT",
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
				"type":       "OBJECT",
				"properties": map[string]any{
					"path":       map[string]any{"type": "STRING", "description": "File path inside the workspace"},
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
				"type":       "OBJECT",
				"properties": map[string]any{
					"query": map[string]any{"type": "STRING", "description": "Text to search for"},
				},
				"required": []any{"query"},
			},
		},
		{
			Name:        "screenshot",
			Description: "Open a URL or local HTML file in a headless browser and capture a screenshot so you can visually inspect the interface. Returns the screenshot image for you to see.",
			Parameters: map[string]any{
				"type":       "OBJECT",
				"properties": map[string]any{
					"target": map[string]any{"type": "STRING", "description": "A URL (http/https) or a local file path inside the workspace to screenshot"},
				},
				"required": []any{"target"},
			},
		},
	}
}

// withoutTool returns a copy of tools with the named tool removed.
func withoutTool(tools []llm.Tool, name string) []llm.Tool {
	out := make([]llm.Tool, 0, len(tools))
	for _, t := range tools {
		if t.Name != name {
			out = append(out, t)
		}
	}
	return out
}
