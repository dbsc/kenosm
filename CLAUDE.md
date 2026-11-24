# kenosm

One-binary AI agent system: an HTTP API + proxy gateway + a web UI that drives an
**autonomous multi-agent swarm** with a real headless browser, a shell, file tools
and (optional) desktop control. Agents are user-configured upstream AI services
(Gemini or any OpenAI-compatible API such as DeepSeek), each optionally routed
through its own SOCKS5 proxy.

## Stack

- **Language:** Go 1.27 (strict typing, no `any`-driven hacks — see rules below).
- **HTTP:** `chi/v5` router; Server-Sent Events for live run streams.
- **DB:** PostgreSQL 16 via `pgx/v5` (pgxpool) + `goose` embedded SQL migrations.
- **Browser:** `chromedp` driving headless Chromium.
- **Native app window:** `webview` (WebKitGTK on Linux, Cocoa on macOS, Edge on
  Windows) via cgo — see `internal/webview`.
- **LLM backends:** Gemini `generateContent` (`internal/llm/gemini.go`) and any
  OpenAI-compatible Chat Completions API (`internal/llm/openai.go`).
- **Speech-to-text (optional):** external `STT_CMD` (`stt/transcribe.py`,
  faster-whisper tiny).
- **Frontend:** vanilla JS embedded via `go:embed` (`internal/webui/static`).

## Layout

```
cmd/
  kenosm/     one-binary entrypoint: serve / chat / app / setup / version
  server/     thin alias for `kenosm serve`
  rekey/      re-encrypt stored service secrets under a new SECRET_KEY
  cursortest/ throwaway desktop cursor/click tester (not part of the build)
internal/
  app/        wires config, DB, services, router, graceful shutdown
  config/     env + .env loading, defaults
  agent/      the tool-using agent loop + tool modules (files/shell/browser/desktop)
  swarm/      orchestrator: plan -> parallel workers -> verify/fix -> finalize
  handler/    HTTP handlers (admin, gateway, swarm, chat, workspace, voice, activity, stats)
  server/     chi router + API-key auth middleware
  llm/        Gemini + OpenAI clients, shared types (Turn/Tool/Usage/APIError)
  browser/    chromedp sessions + a local SOCKS5 credential-injecting relay
  desktop/    xdotool/ydotool/uinput desktop automation (observe-only tools today)
  workspace/  sandboxed working dir: file ops, shell exec, process-group cleanup
  transport/  SOCKS5 http.Transport cache
  crypto/     AES-256-GCM encryption of stored secrets
  repository/ pgx repos: services, usage, chats, activity, request logs
  db/         pool + goose migrations
  logging/    async request-log buffer
  pricing/    model price table + cost computation
  webui/      embedded frontend
  webview/    cgo native-window wrapper
stt/          optional Python speech-to-text helper (venv not committed)
assets/       versioned design library (icons, templates, UI/UX KB) seeded into workspace
```

## Run / build / test

```sh
make build      # CGO_ENABLED=1 go build -> bin/kenosm ; go build -> bin/kenosm-server
make run        # go run ./cmd/kenosm  (serves API + gateway + web UI)
make test       # go test ./...
make vet        # go vet ./...
make db-up      # docker compose up -d db   (PostgreSQL 16)
make db-down    # docker compose down
make install    # build + copy bin/kenosm to ~/.local/bin

kenosm serve        # run the server
kenosm chat         # terminal chat with the swarm
kenosm chat --serve # start server, then chat
kenosm app          # open the web UI in a native window
kenosm setup        # first-run wizard (.env + DB)
kenosm version
```

Configuration is environment-driven (see `.env.example`); a local `.env` is
loaded first. Requires Chromium for the browser tools and PostgreSQL for state.

## Hard coding rules

1. **Strict typing.** Prefer concrete structs over `map[string]any` and
   `any`. When decoding external/LLM payloads, marshal into named structs with
   json tags; avoid `v.(string)`-style assertions (see `agent.go` `strVal`/
   `intVal` as the anti-pattern to avoid in new code). No `interface{}` misuse.
2. **No hacks / no dead code.** Do not add conditionals for tools or modules
   that no longer exist. Keep `describeTool`, `DirectionFor`, and tool schemas
   in sync with the tools actually registered. Remove code the moment it becomes
   unreachable; do not leave half-disabled feature paths behind.
3. **Single design-code source.** All frontend color/type/spacing decisions
   derive from `assets/DESIGN.md` + the `--brand-hue` OKLCH tokens; never
   hardcode hex in components. New UI follows the existing design system.
4. **Path safety.** Everything touching the filesystem must resolve through
   `workspace.Resolve` (or an equivalent root-bounded helper) and refuse
   escapes; never build file paths by string concatenation of user/model input.
5. **Secrets.** `SECRET_KEY`, upstream API keys and proxy passwords are
   encrypted at rest (AES-256-GCM) and never serialized to JSON. Never log or
   emit secrets or full shell commands; truncate command echoes.
6. **Concurrency.** Shared mutable state is guarded by `sync.Mutex` (or
   `sync.Once`/`sync.Cond`); never share a `Swarm`/`Agent` across runs. Bounded
   semaphores (MAX_CONCURRENT_RUNS) gate expensive runs.
7. **Resource hygiene.** Every `go func` has a clear exit path (context/ticker
   `Stop`/channel close); every temp file/dir is removed on all paths; body
   reads go through `handler.decodeJSON` (32 MB cap) — never raw
   `json.NewDecoder(r.Body).Decode` for user input.
8. **Tests.** New behaviour ships with a unit test (`go test ./...` must pass);
   `go vet ./...` must stay clean. Keep `go build ./...` green.
