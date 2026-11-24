package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"kenosm/internal/browser"
	"kenosm/internal/desktop"
	"kenosm/internal/llm"
	"kenosm/internal/transport"
	"kenosm/internal/workspace"
)

// Config configures an autonomous tool-using agent.
type Config struct {
	LLM           llm.Client
	Workspace     *workspace.Workspace
	Browser       *browser.Browser    // optional; enables the browser_* tools
	Desktop       *desktop.Desktop    // optional; enables the desktop_* tools
	Proxy         transport.ProxySpec // optional; the agent's own SOCKS5 proxy (per-agent browser)
	MaxIterations int
	// DisabledTools lists tool names to exclude from the agent, even when their
	// module is otherwise available. Empty = enable every available tool.
	DisabledTools []string
	Log           func(kind, tool, detail string) // optional progress callback; tool is the tool name for tool events
	OnUsage       func(llm.Usage)                 // optional: fired after each model call with its token usage
	OnSession     func(s *browser.Session)        // optional: fired when the agent opens its browser session (live-view hook)
	SystemExtra   string                          // optional: appended to the agent's system prompt (e.g. a shared contract)
	ContextBytes  int                             // optional: context-window budget for history compaction (0 = 48 KiB)
}

// Result is what an agent did during a run.
type Result struct {
	Summary    string    `json:"summary"`
	ToolCalls  []string  `json:"tool_calls"`
	Files      []string  `json:"files"` // workspace-relative paths written/edited
	Iterations int       `json:"iterations"`
	Usage      llm.Usage `json:"usage"`
}

// Tool is a single callable tool the agent can expose to the model: the schema
// the model sees (def) plus the handler that runs it. Related tools are grouped
// into modules (files, shell, browser, desktop), so each tool is an independent,
// toggleable unit.
type Tool struct {
	def llm.Tool
	run func(a *Agent, ctx context.Context, c llm.FunctionCall) llm.FunctionResponse
}

// tool is a small constructor that keeps the module definitions terse.
func tool(name, description string, params map[string]any, run func(*Agent, context.Context, llm.FunctionCall) llm.FunctionResponse) Tool {
	return Tool{def: llm.Tool{Name: name, Description: description, Parameters: params}, run: run}
}

// Module is a group of related tools that can be toggled on or off as a unit.
// Optional modules need an external runtime (browser or desktop) and are only
// truly enabled when that runtime is available.
type Module struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	Tools    []string `json:"tools"`
	Optional bool     `json:"optional"`
}

func toolNamesOf(ts []Tool) []string {
	names := make([]string, 0, len(ts))
	for _, t := range ts {
		names = append(names, t.def.Name)
	}
	return names
}

// Modules returns the tool modules in display order.
func Modules() []Module {
	return []Module{
		{Name: "files", Label: "Files — read & write code", Tools: toolNamesOf(fileTools()), Optional: false},
		{Name: "shell", Label: "Shell — run commands", Tools: toolNamesOf(shellTools()), Optional: false},
		{Name: "browser", Label: "Browser — browse & click", Tools: toolNamesOf(browserTools()), Optional: true},
		{Name: "desktop", Label: "Desktop — screenshots", Tools: toolNamesOf(desktopTools()), Optional: true},
	}
}

// DisabledToolNames maps disabled module names to the concrete tool names to
// exclude. Unknown module names are ignored.
func DisabledToolNames(disabledModules []string) []string {
	disabled := make(map[string]bool, len(disabledModules))
	for _, m := range disabledModules {
		disabled[strings.TrimSpace(m)] = true
	}
	var out []string
	for _, mod := range Modules() {
		if disabled[mod.Name] {
			out = append(out, mod.Tools...)
		}
	}
	return out
}

// Agent is a tool-using loop: it repeatedly asks the model, executes any tool
// calls (write files, read files, list files, run commands, drive a browser,
// control the desktop), and feeds results back until the model finishes.
type Agent struct {
	llm         llm.Client
	ws          *workspace.Workspace
	browser     *browser.Browser
	desktop     *desktop.Desktop
	proxy       transport.ProxySpec
	session     *browser.Session
	runCtx      context.Context
	maxIt       int
	tools       []Tool
	log         func(kind, tool, detail string)
	onUsage     func(llm.Usage)
	onSession   func(*browser.Session)
	systemExtra string
	ctxBytes    int    // context-window budget for history compaction (0 = default)
	files       []string // paths written/edited during the run
}

func New(cfg Config) *Agent {
	maxIt := cfg.MaxIterations
	if maxIt <= 0 {
		maxIt = 15
	}

	disabled := make(map[string]bool, len(cfg.DisabledTools))
	for _, n := range cfg.DisabledTools {
		disabled[strings.TrimSpace(n)] = true
	}

	var tools []Tool
	addModule := func(enabled bool, defs []Tool) {
		if !enabled {
			return
		}
		for _, t := range defs {
			if disabled[t.def.Name] {
				continue
			}
			tools = append(tools, t)
		}
	}

	// File and shell tools are always available; browser/desktop modules enable
	// only when the respective runtime is present. Individual tools can still be
	// turned off via Config.DisabledTools.
	addModule(true, fileTools())
	addModule(true, shellTools())
	addModule(cfg.Browser != nil, browserTools())
	addModule(cfg.Desktop != nil, desktopTools())

	return &Agent{llm: cfg.LLM, ws: cfg.Workspace, browser: cfg.Browser, desktop: cfg.Desktop, proxy: cfg.Proxy, maxIt: maxIt, tools: tools, log: cfg.Log, onUsage: cfg.OnUsage, onSession: cfg.OnSession, systemExtra: cfg.SystemExtra, ctxBytes: cfg.ContextBytes}
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

	system := a.systemPrompt()

	defs := make([]llm.Tool, 0, len(a.tools))
	for _, t := range a.tools {
		defs = append(defs, t.def)
	}

	var summary strings.Builder
	var callLog []string
	var usage llm.Usage
	iterations := 0
	lastSig := ""
	repeat := 0

	for iterations < a.maxIt {
		iterations++
		// Long tool loops grow the conversation until the provider's context
		// window rejects the request (DeepSeek: 64K). Compact the history
		// before each call: keep the original task turn and the most recent
		// exchanges, dropping whole (model, tool-responses) pairs so the
		// role alternation stays valid. The budget comes from the run's
		// context-window setting (a.ctxBytes), defaulting to 48 KiB.
		history = a.compactHistory(history)
		if a.log != nil {
			a.log("model", "", "generating response")
		}
		res, err := a.llm.Generate(ctx, system, history, defs)
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
				a.log("tool", c.Name, describeTool(c))
			}
		}
		history = append(history, llm.Turn{Role: "user", ToolResponses: responses})
	}

	return Result{Summary: summary.String(), ToolCalls: callLog, Files: dedupe(a.files), Iterations: iterations, Usage: usage}, nil
}

// systemPrompt builds the agent instructions, describing only the capabilities
// whose tools are actually enabled. Browser/desktop tools are removed when the
// corresponding module is turned off, even though the runtime is still present,
// so the prompt must look at the enabled tool set rather than a.browser/a.desktop.
func (a *Agent) systemPrompt() string {
	hasFiles := a.hasTool("write_file") || a.hasTool("read_file") || a.hasTool("list_files") || a.hasTool("edit_file") || a.hasTool("search_files")
	hasShell := a.hasTool("run_command")
	hasBrowser := a.hasToolPrefix("browser_")
	hasDesktop := a.hasToolPrefix("desktop_")

	var b strings.Builder
	b.WriteString("You are an autonomous coding agent with access to a shared workspace directory.")

	var caps []string
	if hasFiles {
		caps = append(caps, "read, write, list, edit and search files in the workspace")
	}
	if hasShell {
		caps = append(caps, "run shell commands to test your work")
	}
	if hasBrowser {
		caps = append(caps, "drive a real web browser (navigate, click, type, scroll, read page text, and screenshot)")
	}
	if hasDesktop {
		caps = append(caps, "take screenshots of the desktop")
	}
	if len(caps) > 0 {
		b.WriteString("\nYou have tools to " + strings.Join(caps, "; ") + ".")
	}

	b.WriteString(`
Use tools only when the task actually requires them. For analytical, descriptive, or question-answering tasks, answer directly in your response without calling any tools.`)
	if hasShell {
		b.WriteString(`
When you create or change files, actually test them with the run_command tool before finishing.`)
	}
	if hasBrowser {
		b.WriteString(`
When you build a web page or any UI, do NOT produce generic AI output (Inter font + purple/blue gradient + rounded white cards). Read assets/DESIGN.md first, then pick exactly ONE named aesthetic (Swiss Minimalism, Brutalism, Glassmorphism, Claymorphism, Aurora Mesh, Cyberpunk, Dark OLED Luxury, Art Deco, Editorial, Organic, 3D Hyperrealism, etc.), state which one you chose, and make every color/font/spacing/shadow decision serve that direction. Use real typography pairing (a display face + body face from Google Fonts), and define colors as OKLCH CSS variables on :root derived from a single --brand-hue (never hardcode hex in components), with a dark-mode variant. A design asset library lives in assets/ — read assets/README.md too. It has ~7500 SVG icons (assets/icons/lucide/<name>.svg and assets/icons/tabler/<name>.svg), animated spinners (assets/spinners/), and reference pages under assets/templates/. Inline an icon's SVG directly (stroke="currentColor") instead of leaving buttons/rows without icons. For real 3D use Three.js from cdnjs: https://cdnjs.cloudflare.com/ajax/libs/three.js/r128/three.min.js.
You also have a searchable UI/UX knowledge base at assets/uiux/. Query it with run_command to get concrete style, palette, font-pairing and UX guidance for the product you're building, e.g.: run_command "python3 assets/uiux/scripts/search.py 'fintech dashboard' --domain style", "--domain color", "--domain typography", "--domain product", "--domain ux", "--domain landing", "--domain chart", "--domain icons", "--domain google-fonts", "--domain react" or "--domain web". To generate a whole coherent design system: run_command "python3 assets/uiux/scripts/search.py '<product>' --design-system --variance 7 --motion 5 --density 6".
When you build a web page or any UI, verify it in the browser before finishing: open it with browser_navigate, then click every button and link, scroll the page, and take a screenshot of each step (browser_click and browser_scroll save a screenshot automatically). Confirm from those screenshots that everything works and looks right. If a page shows a CAPTCHA or similar challenge, read it with browser_snapshot or browser_screenshot and type the answer with browser_type.
When the task asks for a screenshot, page text, or anything that lives on a live website, you MUST use the browser tools (browser_navigate, browser_screenshot, browser_text, browser_snapshot). NEVER fake this work: do not fetch pages with run_command (curl/wget/urllib/requests), do not generate or draw images with Python/PIL, and do not copy existing or old screenshot files to pretend they are new. A screenshot must come from browser_screenshot of the real page, and page content must come from browser_text/browser_snapshot. If a page will not load or blocks the browser, report it as failed rather than inventing content.`)
	}
	if hasDesktop {
		b.WriteString(`
You can observe the desktop via screenshots: use desktop_windows to list visible windows and desktop_screenshot to capture the whole screen or a specific window.`)
	}
	if hasFiles {
		b.WriteString(`
All file paths are relative to the workspace root. Never prefix a path with "workspace/" or an absolute directory — write "app/index.html", not "workspace/app/index.html".`)
	}
	b.WriteString(`
When finished, output a concise summary of what you did and the result.
If your task involved a plan, architecture, data flow or process, add a mermaid diagram in a fenced code block with the language tag "mermaid" (flowchart or sequenceDiagram) to your summary — the user's UI renders it directly.`)

	if a.systemExtra != "" {
		b.WriteString("\n\n" + a.systemExtra)
	}
	return b.String()
}

func (a *Agent) hasTool(name string) bool {
	for _, t := range a.tools {
		if t.def.Name == name {
			return true
		}
	}
	return false
}

func (a *Agent) hasToolPrefix(prefix string) bool {
	for _, t := range a.tools {
		if strings.HasPrefix(t.def.Name, prefix) {
			return true
		}
	}
	return false
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

// compactHistory drops whole (model, tool-response) exchange pairs from the
// middle of the conversation so a long tool loop stays inside the provider's
// context window. The first turn (the task) and the most recent exchanges are
// kept; the role alternation (user → model → user → …) is preserved.
const (
	historyMaxTurns        = 24
	defaultHistoryMaxBytes = 48 * 1024
)

// compactHistory is the agent's history budget applied per-run: the context
// window selected in the UI (a.ctxBytes) replaces the default 48 KiB.
func (a *Agent) compactHistory(history []llm.Turn) []llm.Turn {
	budget := a.ctxBytes
	if budget <= 0 {
		budget = defaultHistoryMaxBytes
	}
	return compactHistoryBudget(history, budget)
}

// compactHistoryBudget drops whole exchange pairs from the middle of the
// conversation so a long tool loop stays inside the provider's context window.
// It prefers genuine tool-exchange pairs (a model turn carrying tool calls
// followed by its matching user tool-response turn): those are self-contained
// once executed and carry the largest payloads (screenshots come back as base64
// tool responses). When no tool pair exists (a plain text chat), the oldest
// conversational exchange after the task turn is dropped instead. The first turn
// (the task) and the most recent exchanges are always kept; the role alternation
// (user → model → user → …) is preserved. The budget bounds the serialized size.
func compactHistoryBudget(history []llm.Turn, budget int) []llm.Turn {
	for len(history) > historyMaxTurns || historyBytes(history) > budget {
		if len(history) < 3 {
			return history // nothing safe left to drop
		}
		// Drop the oldest tool-exchange pair when one exists.
		if idx := firstToolPairIndex(history); idx >= 1 {
			history = append(append([]llm.Turn{}, history[:idx]...), history[idx+2:]...)
			continue
		}
		// No tool pair: drop the oldest exchange after the task (index 1 and 2).
		if history[1].Role != "model" {
			return history // unexpected shape: refuse to mangle it
		}
		history = append([]llm.Turn{history[0]}, history[3:]...)
	}
	return history
}

// firstToolPairIndex returns the index of the first model turn that carries tool
// calls and is immediately followed by its matching user tool-response turn, or
// -1 when the history contains no such pair.
func firstToolPairIndex(history []llm.Turn) int {
	for i := 1; i+1 < len(history); i++ {
		if history[i].Role == "model" && len(history[i].ToolCalls) > 0 &&
			history[i+1].Role == "user" && len(history[i+1].ToolResponses) > 0 {
			return i
		}
	}
	return -1
}

// historyBytes estimates the serialized size of the conversation, used to
// bound total context growth (a few turns can carry huge file contents).
func historyBytes(history []llm.Turn) int {
	data, err := json.Marshal(history)
	if err != nil {
		return 0
	}
	return len(data)
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

// execute dispatches a tool call to the matching tool's handler.
func (a *Agent) execute(ctx context.Context, c llm.FunctionCall) llm.FunctionResponse {
	for _, t := range a.tools {
		if t.def.Name == c.Name {
			return t.run(a, ctx, c)
		}
	}
	return a.resp(c, map[string]any{"error": "unknown tool: " + c.Name})
}

// resp builds a function response carrying the tool call's id and thought
// signature so the model can correlate it.
func (a *Agent) resp(c llm.FunctionCall, m map[string]any) llm.FunctionResponse {
	return llm.FunctionResponse{Name: c.Name, Response: m, ID: c.ID, ThoughtSignature: c.ThoughtSignature}
}

// bsession returns the run's shared browser session, creating it on first use.
func (a *Agent) bsession() (*browser.Session, error) {
	if a.session != nil {
		return a.session, nil
	}
	if a.browser == nil {
		return nil, fmt.Errorf("no browser available")
	}
	s, err := a.browser.NewSession(a.runCtx, a.proxy)
	if err != nil {
		return nil, err
	}
	a.session = s
	if a.onSession != nil {
		a.onSession(s)
	}
	return s, nil
}

// shotResp captures the current browser viewport, saves it to the workspace and
// returns a tool response carrying both the saved path and the image.
func (a *Agent) shotResp(c llm.FunctionCall, s *browser.Session, meta map[string]any) llm.FunctionResponse {
	img, err := s.Screenshot()
	if err != nil {
		return a.resp(c, map[string]any{"error": err.Error()})
	}
	return a.imageResp(c, img, meta)
}

// deskShotResp screenshots the desktop and returns it as the tool's image result.
func (a *Agent) deskShotResp(c llm.FunctionCall, meta map[string]any) llm.FunctionResponse {
	img, err := a.desktop.Screenshot()
	if err != nil {
		return a.resp(c, map[string]any{"error": err.Error()})
	}
	return a.imageResp(c, img, meta)
}

// imageResp saves a PNG screenshot to the workspace and returns it to the model.
func (a *Agent) imageResp(c llm.FunctionCall, img []byte, meta map[string]any) llm.FunctionResponse {
	path := fmt.Sprintf("screenshots/shot-%d.png", time.Now().UnixNano())
	if werr := a.ws.WriteFile(path, string(img)); werr != nil {
		return a.resp(c, map[string]any{"error": "save screenshot: " + werr.Error()})
	}
	a.noteFile(path)
	meta["screenshot"] = path
	return llm.FunctionResponse{Name: c.Name, Response: meta, ID: c.ID, ThoughtSignature: c.ThoughtSignature, ImageMimeType: "image/png", ImageData: img}
}

// resolveTarget turns a URL or workspace-relative file path into something the
// browser can load (http(s)/file URL). Local file targets must stay inside the
// workspace: the browser must never be able to read arbitrary host files.
func (a *Agent) resolveTarget(t string) (string, error) {
	t = strings.TrimSpace(t)
	if strings.HasPrefix(t, "http://") || strings.HasPrefix(t, "https://") {
		return t, nil
	}
	if strings.HasPrefix(t, "file://") {
		u, err := url.Parse(t)
		if err != nil {
			return "", fmt.Errorf("invalid file URL: %w", err)
		}
		if u.Host != "" && u.Host != "localhost" {
			return "", fmt.Errorf("file URLs must reference the local workspace")
		}
		abs, err := a.ws.Resolve(u.Path)
		if err != nil {
			return "", err
		}
		return "file://" + filepath.ToSlash(abs), nil
	}
	abs, err := a.ws.Resolve(strings.TrimPrefix(t, "./"))
	if err != nil {
		return "", err
	}
	return "file://" + filepath.ToSlash(abs), nil
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
	return s
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

func strSlice(v any) []string {
	switch a := v.(type) {
	case []any:
		out := make([]string, 0, len(a))
		for _, e := range a {
			out = append(out, strVal(e))
		}
		return out
	case []string:
		return a
	}
	return nil
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
			// Truncate: full commands may contain secrets and would land in the
			// activity log and the frontend feed verbatim.
			return "run_command " + truncate(cmd, 120)
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
	}
	return c.Name
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// fileTools returns the workspace file tools (always available).
func fileTools() []Tool {
	return []Tool{
		tool("write_file", "Create or overwrite a file in the workspace.",
			map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"path":    map[string]any{"type": "STRING", "description": "Relative file path inside the workspace (do NOT prefix with 'workspace/')"},
					"content": map[string]any{"type": "STRING", "description": "Full file content"},
				},
				"required": []any{"path", "content"},
			},
			func(a *Agent, _ context.Context, c llm.FunctionCall) llm.FunctionResponse {
				path := cleanRelPath(c.Args["path"])
				content, _ := c.Args["content"].(string)
				if err := a.ws.WriteFile(path, content); err != nil {
					return a.resp(c, map[string]any{"error": err.Error()})
				}
				a.noteFile(path)
				return a.resp(c, map[string]any{"ok": true, "path": path})
			}),
		tool("read_file", "Read the content of a file in the workspace.",
			map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"path": map[string]any{"type": "STRING", "description": "Relative file path inside the workspace (do NOT prefix with 'workspace/')"},
				},
				"required": []any{"path"},
			},
			func(a *Agent, _ context.Context, c llm.FunctionCall) llm.FunctionResponse {
				path := cleanRelPath(c.Args["path"])
				data, err := a.ws.ReadFile(path)
				if err != nil {
					return a.resp(c, map[string]any{"error": err.Error()})
				}
				return a.resp(c, map[string]any{"content": data})
			}),
		tool("list_files", "List all files in the workspace.",
			map[string]any{"type": "OBJECT", "properties": map[string]any{}},
			func(a *Agent, _ context.Context, c llm.FunctionCall) llm.FunctionResponse {
				files, err := a.ws.ListFiles()
				if err != nil {
					return a.resp(c, map[string]any{"error": err.Error()})
				}
				names := make([]string, 0, len(files))
				for _, f := range files {
					names = append(names, f.Path)
				}
				return a.resp(c, map[string]any{"files": names})
			}),
		tool("edit_file", "Replace a substring inside a file with new text, without rewriting the whole file. Use it for targeted fixes. Returns how many replacements were made.",
			map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"path":       map[string]any{"type": "STRING", "description": "File path inside the workspace (do NOT prefix with 'workspace/')"},
					"old_string": map[string]any{"type": "STRING", "description": "Exact text to find (must exist in the file)"},
					"new_string": map[string]any{"type": "STRING", "description": "Replacement text"},
				},
				"required": []any{"path", "old_string", "new_string"},
			},
			func(a *Agent, _ context.Context, c llm.FunctionCall) llm.FunctionResponse {
				path := cleanRelPath(c.Args["path"])
				oldS, _ := c.Args["old_string"].(string)
				newS, _ := c.Args["new_string"].(string)
				if path == "" || oldS == "" {
					return a.resp(c, map[string]any{"error": "edit_file requires path and old_string"})
				}
				n, err := a.ws.EditFile(path, oldS, newS)
				if err != nil {
					return a.resp(c, map[string]any{"error": err.Error()})
				}
				a.noteFile(path)
				return a.resp(c, map[string]any{"ok": true, "path": path, "replaced": n})
			}),
		tool("search_files", "Search all files in the workspace for a text query (case-insensitive) and return matching lines as 'path:line: text'.",
			map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"query": map[string]any{"type": "STRING", "description": "Text to search for"},
				},
				"required": []any{"query"},
			},
			func(a *Agent, _ context.Context, c llm.FunctionCall) llm.FunctionResponse {
				query, _ := c.Args["query"].(string)
				if query == "" {
					return a.resp(c, map[string]any{"error": "search_files requires a query"})
				}
				matches, err := a.ws.Search(query)
				if err != nil {
					return a.resp(c, map[string]any{"error": err.Error()})
				}
				return a.resp(c, map[string]any{"matches": matches, "count": len(matches)})
			}),
	}
}

// shellTools returns the command-running tool (always available).
func shellTools() []Tool {
	return []Tool{
		tool("run_command", "Run a shell command inside the workspace directory and return stdout/stderr and exit code. Use it to test your work.",
			map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"command": map[string]any{"type": "STRING", "description": "Shell command to run"},
				},
				"required": []any{"command"},
			},
			func(a *Agent, ctx context.Context, c llm.FunctionCall) llm.FunctionResponse {
				command, _ := c.Args["command"].(string)
				out, code, err := a.ws.RunCommand(ctx, command)
				m := map[string]any{"output": out, "exit_code": code}
				if err != nil {
					m["error"] = err.Error()
				}
				return a.resp(c, m)
			}),
	}
}

// browserTools returns the browser tools (enabled when a Browser is available).
func browserTools() []Tool {
	return []Tool{
		tool("browser_navigate", "Open a URL (or a workspace-relative file path) in the browser. Returns a screenshot of the loaded page.",
			map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"url": map[string]any{"type": "STRING", "description": "http(s) URL to open, or a local file path inside the workspace"},
				},
				"required": []any{"url"},
			},
			func(a *Agent, _ context.Context, c llm.FunctionCall) llm.FunctionResponse {
				target := strVal(c.Args["url"])
				if target == "" {
					return a.resp(c, map[string]any{"error": "browser_navigate requires a url"})
				}
				s, err := a.bsession()
				if err != nil {
					return a.resp(c, map[string]any{"error": err.Error()})
				}
				targetURL, err := a.resolveTarget(target)
				if err != nil {
					return a.resp(c, map[string]any{"error": err.Error()})
				}
				if err := s.Navigate(targetURL); err != nil {
					return a.resp(c, map[string]any{"error": err.Error()})
				}
				return a.shotResp(c, s, map[string]any{"ok": true, "url": target})
			}),
		tool("browser_snapshot", "List the visible interactive elements on the current page (buttons, links, inputs) as numbered entries. Use the numbers as targets for browser_click / browser_type.",
			map[string]any{"type": "OBJECT", "properties": map[string]any{}},
			func(a *Agent, _ context.Context, c llm.FunctionCall) llm.FunctionResponse {
				s, err := a.bsession()
				if err != nil {
					return a.resp(c, map[string]any{"error": err.Error()})
				}
				els, err := s.Snapshot()
				if err != nil {
					return a.resp(c, map[string]any{"error": err.Error()})
				}
				return a.resp(c, map[string]any{"elements": els, "count": len(els)})
			}),
		tool("browser_click", "Click an element on the page: pass its index from browser_snapshot (e.g. \"3\") or a CSS selector (e.g. \"#submit\"). Returns a screenshot taken right after the click.",
			map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"target": map[string]any{"type": "STRING", "description": "Element index from browser_snapshot, or a CSS selector"},
				},
				"required": []any{"target"},
			},
			func(a *Agent, _ context.Context, c llm.FunctionCall) llm.FunctionResponse {
				target := browser.SelectorFor(strVal(c.Args["target"]))
				if target == "" {
					return a.resp(c, map[string]any{"error": "browser_click requires a target (index from browser_snapshot, or a CSS selector)"})
				}
				s, err := a.bsession()
				if err != nil {
					return a.resp(c, map[string]any{"error": err.Error()})
				}
				if err := s.Click(target); err != nil {
					return a.resp(c, map[string]any{"error": err.Error()})
				}
				return a.shotResp(c, s, map[string]any{"ok": true, "clicked": target})
			}),
		tool("browser_type", "Type text into an input/textarea/field: pass its index from browser_snapshot or a CSS selector. Returns a screenshot after typing.",
			map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"target": map[string]any{"type": "STRING", "description": "Element index from browser_snapshot, or a CSS selector"},
					"text":   map[string]any{"type": "STRING", "description": "Text to type"},
				},
				"required": []any{"target", "text"},
			},
			func(a *Agent, _ context.Context, c llm.FunctionCall) llm.FunctionResponse {
				target := browser.SelectorFor(strVal(c.Args["target"]))
				text := strVal(c.Args["text"])
				if target == "" {
					return a.resp(c, map[string]any{"error": "browser_type requires a target"})
				}
				s, err := a.bsession()
				if err != nil {
					return a.resp(c, map[string]any{"error": err.Error()})
				}
				if err := s.Type(target, text); err != nil {
					return a.resp(c, map[string]any{"error": err.Error()})
				}
				return a.shotResp(c, s, map[string]any{"ok": true, "typed_into": target})
			}),
		tool("browser_scroll", "Scroll the page up or down. Returns a screenshot of the new viewport so you can verify scrolled content.",
			map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"direction": map[string]any{"type": "STRING", "description": "\"down\" or \"up\""},
					"amount":    map[string]any{"type": "NUMBER", "description": "Pixels to scroll (default 600)"},
				},
				"required": []any{"direction"},
			},
			func(a *Agent, _ context.Context, c llm.FunctionCall) llm.FunctionResponse {
				amount := intVal(c.Args["amount"], 600)
				if strings.EqualFold(strVal(c.Args["direction"]), "up") {
					amount = -amount
				}
				s, err := a.bsession()
				if err != nil {
					return a.resp(c, map[string]any{"error": err.Error()})
				}
				if err := s.Scroll(amount); err != nil {
					return a.resp(c, map[string]any{"error": err.Error()})
				}
				return a.shotResp(c, s, map[string]any{"ok": true, "scrolled_by": amount})
			}),
		tool("browser_screenshot", "Capture a screenshot of the current browser viewport.",
			map[string]any{"type": "OBJECT", "properties": map[string]any{}},
			func(a *Agent, _ context.Context, c llm.FunctionCall) llm.FunctionResponse {
				s, err := a.bsession()
				if err != nil {
					return a.resp(c, map[string]any{"error": err.Error()})
				}
				return a.shotResp(c, s, map[string]any{"ok": true})
			}),
		tool("browser_text", "Return the visible text of the current page.",
			map[string]any{"type": "OBJECT", "properties": map[string]any{}},
			func(a *Agent, _ context.Context, c llm.FunctionCall) llm.FunctionResponse {
				s, err := a.bsession()
				if err != nil {
					return a.resp(c, map[string]any{"error": err.Error()})
				}
				text, err := s.Text()
				if err != nil {
					return a.resp(c, map[string]any{"error": err.Error()})
				}
				return a.resp(c, map[string]any{"text": text})
			}),
	}
}

// desktopTools returns the desktop tools (enabled when a Desktop is available).
// Computer control (launching programs, mouse, keyboard) is intentionally
// removed: agents can only observe the desktop via screenshots and window lists.
func desktopTools() []Tool {
	return []Tool{
		tool("desktop_windows", "List visible desktop windows (id + title) so you can pick one to screenshot.",
			map[string]any{"type": "OBJECT", "properties": map[string]any{}},
			func(a *Agent, _ context.Context, c llm.FunctionCall) llm.FunctionResponse {
				wins, err := a.desktop.Windows()
				if err != nil {
					return a.resp(c, map[string]any{"error": err.Error()})
				}
				return a.resp(c, map[string]any{"windows": wins, "count": len(wins)})
			}),
		tool("desktop_screenshot", "Capture a screenshot of the whole screen, or of a specific window if 'window' (id or title) is given.",
			map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"window": map[string]any{"type": "STRING", "description": "Window id or title substring (optional; default is the whole screen)"},
				},
			},
			func(a *Agent, _ context.Context, c llm.FunctionCall) llm.FunctionResponse {
				var img []byte
				var err error
				if target := strVal(c.Args["window"]); target != "" {
					img, err = a.desktop.ScreenshotWindow(target)
				} else {
					img, err = a.desktop.Screenshot()
				}
				if err != nil {
					return a.resp(c, map[string]any{"error": err.Error()})
				}
				return a.imageResp(c, img, map[string]any{"ok": true})
			}),
	}
}
