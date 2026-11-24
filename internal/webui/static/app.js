// kenosm desktop frontend — no auth, no modals, functional only.
// Chat · agents · files · stats · tools · live agent browsers.

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => document.querySelectorAll(sel);

// ---------- helpers ----------
function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c])
  );
}

function fmt(n) {
  return Number(n || 0).toLocaleString("en-US");
}

function fmtCost(n) {
  const v = Number(n || 0);
  if (!v) return "$0";
  return v < 0.01 ? "$" + v.toFixed(5) : "$" + v.toFixed(4);
}

function fmtSize(n) {
  n = Number(n || 0);
  if (n < 1024) return n + " B";
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
  return (n / 1024 / 1024).toFixed(1) + " MB";
}

// The native window pre-seeds the API key into localStorage; every request
// carries it as a query param so the server's bearer auth lets the app through.
function apiKey() {
  try { return localStorage.getItem("kenosm_key") || ""; } catch { return ""; }
}

function authUrl(url) {
  const k = apiKey();
  if (!k) return url;
  return url + (url.includes("?") ? "&" : "?") + "key=" + encodeURIComponent(k);
}

async function api(url, opts = {}) {
  const res = await fetch(authUrl(url), {
    ...opts,
    headers: { "Content-Type": "application/json", ...(opts.headers || {}) },
  });
  return res;
}

// ---------- markdown (escape-first: raw HTML in model output can never run) ----------
function inline(t) {
  return t
    .replace(/`([^`]+)`/g, "<code>$1</code>")
    .replace(/\*\*([^*]+)\*\*/g, "<b>$1</b>")
    .replace(/(^|[\s(])\*([^*\n]+)\*(?=[\s).,!;:]|$)/g, "$1<i>$2</i>");
}

function md(s) {
  const lines = String(s ?? "").split("\n");
  const out = [];
  let code = [];
  let mermaid = [];
  let list = "";
  const escI = (t) => inline(esc(t));
  const closeList = () => { if (list) { out.push("</" + list + ">"); list = ""; } };
  const flushCode = () => {
    if (code.length) {
      out.push('<pre class="md-code"><button class="code-copy" data-code="' + esc(code.join("\n")) + '">copy</button>' + code.join("\n") + "</pre>");
      code = [];
    }
  };
  const flushMermaid = () => {
    if (mermaid.length) {
      const src = mermaid.join("\n");
      // data-src carries JSON so quotes/newlines survive the attribute; the
      // renderer reads it back with JSON.parse.
      out.push('<div class="mermaid-box" data-src="' + esc(JSON.stringify(src)) + '"></div>');
      mermaid = [];
    }
  };
  for (const line of lines) {
    if (line.trim().startsWith("```")) {
      const lang = line.trim().slice(3).trim().toLowerCase();
      if (mermaid.length) { flushMermaid(); continue; }
      if (code.length) { flushCode(); continue; }
      if (lang === "mermaid") { closeList(); flushCode(); mermaid = []; continue; }
      closeList(); flushMermaid(); code = [];
      continue;
    }
    if (mermaid.length) { mermaid.push(line); continue; }
    if (code.length) { code.push(esc(line)); continue; }
    const t = line.trim();
    if (!t) { closeList(); flushCode(); continue; }
    if (/^#{1,4}\s/.test(t)) { closeList(); flushCode(); out.push('<div class="md-h">' + escI(t.replace(/^#{1,4}\s/, "")) + "</div>"); continue; }
    if (/^[-*]\s/.test(t)) { if (list !== "ul") { closeList(); out.push("<ul>"); list = "ul"; } out.push("<li>" + escI(t.replace(/^[-*]\s/, "")) + "</li>"); continue; }
    if (/^\d+\.\s/.test(t)) { if (list !== "ol") { closeList(); out.push("<ol>"); list = "ol"; } out.push("<li>" + escI(t.replace(/^\d+\.\s/, "")) + "</li>"); continue; }
    closeList(); flushCode();
    out.push("<p>" + escI(t) + "</p>");
  }
  flushMermaid(); flushCode(); closeList();
  return out.join("\n");
}

// Renders every pending mermaid box in a container as an inline SVG diagram
// (Claude-style: the model answers with flowcharts and sequence diagrams).
async function renderMermaidBoxes(container) {
  const boxes = container.querySelectorAll(".mermaid-box:not(.done)");
  if (!boxes.length) return;
  if (typeof mermaid === "undefined") {
    boxes.forEach((b) => { b.classList.add("done"); b.innerHTML = '<div class="mermaid-err">diagram renderer not loaded</div>'; });
    return;
  }
  for (const box of boxes) {
    box.classList.add("done");
    let src;
    try { src = JSON.parse(box.dataset.src || ""); } catch { src = box.dataset.src; }
    if (!src) continue;
    try {
      const id = "mmd" + Math.random().toString(36).slice(2);
      const { svg } = await mermaid.render(id, src);
      box.innerHTML = svg;
    } catch (e) {
      box.innerHTML = '<div class="mermaid-err">mermaid: ' + esc(String((e && e.message) || e)) + "</div>";
    }
  }
}

// ---------- tabs ----------
function switchTab(name) {
  $$(".tab").forEach((b) => b.classList.toggle("active", b.dataset.tab === name));
  $$(".tab-panel").forEach((p) => p.classList.remove("active"));
  const panel = $("#tab-" + name);
  if (panel) panel.classList.add("active");
  if (name === "agents") loadAgents();
  if (name === "files") loadFiles();
  if (name === "stats") loadStats();
  if (name === "tools") loadModules();
}

$$(".tab").forEach((btn) => {
  btn.addEventListener("click", () => switchTab(btn.dataset.tab));
});

// Gear menu: Agents / Files / Stats / Tools live behind it.
$("#btn-gear").addEventListener("click", (ev) => {
  ev.stopPropagation();
  $("#gear-menu").classList.toggle("hidden");
});
document.addEventListener("click", (ev) => {
  if (!ev.target.closest("#gear-menu") && !ev.target.closest("#btn-gear")) {
    $("#gear-menu").classList.add("hidden");
  }
});
$$("#gear-menu .menu-item").forEach((mi) => {
  mi.addEventListener("click", () => {
    $("#gear-menu").classList.add("hidden");
    switchTab(mi.dataset.tab);
  });
});

// ---------- chats ----------
let currentChatId = null;
let currentMode = "single";
let currentRunId = null;
let currentES = null;
let running = false;

async function loadChats() {
  const res = await api("/api/v1/chats");
  if (!res.ok) return;
  const data = await res.json();
  const chats = data.chats || [];
  const el = $("#chat-tabs");
  el.innerHTML = "";
  for (const c of chats || []) {
    const row = document.createElement("div");
    row.className = "chat-tab" + (c.id === currentChatId ? " active" : "");
    row.innerHTML =
      '<span class="chat-tab-title">' + esc(c.title || "New chat") + "</span>" +
      '<button class="chat-tab-x" title="Delete"><svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg></button>';
    row.querySelector(".chat-tab-title").onclick = () => selectChat(c.id);
    row.querySelector(".chat-tab-x").onclick = (ev) => {
      ev.stopPropagation();
      deleteChat(c.id);
    };
    el.appendChild(row);
  }
  if (!chats || !chats.length) {
    el.innerHTML = '<p class="hint" style="padding:6px">No sessions yet.</p>';
  }
}

async function newChat() {
  const res = await api("/api/v1/chats", { method: "POST" });
  if (!res.ok) return null;
  const c = await res.json();
  currentChatId = c.id;
  renderMessages([]);
  await loadChats();
  return c.id;
}

async function selectChat(id) {
  const res = await api("/api/v1/chats/" + encodeURIComponent(id));
  if (!res.ok) return;
  const c = await res.json();
  currentChatId = c.id;
  currentMode = c.mode || "single";
  setMode(currentMode);
  renderMessages(c.messages || []);
  loadChats();
}

async function deleteChat(id) {
  if (!confirm("Delete this session?")) return;
  await api("/api/v1/chats/" + encodeURIComponent(id), { method: "DELETE" });
  if (currentChatId === id) {
    currentChatId = null;
    renderMessages([]);
  }
  loadChats();
}

// ---------- auto-follow (pin the view to the newest output) ----------
let autoFollow = true;

function scrollConsoleToBottom() {
  if (!autoFollow) return;
  const c = $("#console-scroll");
  if (c) c.scrollTop = c.scrollHeight;
}

function updateFollowButton() {
  const c = $("#console-scroll");
  const btn = $("#btn-follow");
  if (!c || !btn) return;
  const nearBottom = c.scrollHeight - c.scrollTop - c.clientHeight < 48;
  autoFollow = nearBottom;
  btn.classList.toggle("visible", !nearBottom);
}

$("#console-scroll").addEventListener("scroll", updateFollowButton);
$("#btn-follow").addEventListener("click", () => {
  autoFollow = true;
  const c = $("#console-scroll");
  if (c) c.scrollTop = c.scrollHeight;
  updateFollowButton();
});

// ---------- messages ----------
function renderMessages(messages) {
  const c = $("#console-scroll");
  c.innerHTML = "";
  if (!messages.length) {
    c.innerHTML = '<div class="msg system" id="console-empty">New chat. Write a message below.</div>';
    return;
  }
  for (const m of messages) addBubble(m.role, m.text);
}

function addBubble(role, text) {
  const c = $("#console-scroll");
  const empty = $("#console-empty");
  if (empty) empty.remove();
  const div = document.createElement("div");
  let cls = "bubble " + (role === "user" ? "bubble-user" : "bubble-assistant");
  if (role !== "user" && /^Error:/.test(String(text).trim())) cls += " bubble-error";
  div.className = cls;
  if (role === "user") div.textContent = text;
  else {
    div.innerHTML = md(text);
    renderMermaidBoxes(div);
  }
  c.appendChild(div);
  scrollConsoleToBottom();
  return div;
}

function addWorkBlock() {
  const c = $("#console-scroll");
  const empty = $("#console-empty");
  if (empty) empty.remove();
  const block = document.createElement("div");
  block.className = "work-block";
  block.innerHTML = '<div class="work-lines"></div>';
  c.appendChild(block);
  scrollConsoleToBottom();
  return block;
}

// ---------- attachments (images go to the model; any other file is uploaded
// to the workspace so the agents can read it) ----------
let attached = []; // {mime_type, data} — images for the model input
let attachedFiles = []; // {name, path} — other files saved in the workspace

const X_SVG =
  '<svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>';
const FILE_SVG =
  '<svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z"/><path d="M14 2v4a2 2 0 0 0 2 2h4"/></svg>';

function renderAttachments() {
  const chips = attached
    .map(
      (a, i) =>
        '<span class="chip"><img src="' + esc(a.data) + '" /><button class="chip-x" data-kind="img" data-i="' + i + '">' + X_SVG + "</button></span>"
    )
    .concat(
      attachedFiles.map(
        (f, i) =>
          '<span class="chip chip-file">' + FILE_SVG + '<span class="chip-name">' + esc(f.name) + '</span><button class="chip-x" data-kind="file" data-i="' + i + '">' + X_SVG + "</button></span>"
      )
    );
  $("#attachments").innerHTML = chips.join("");
}
$("#btn-attach").addEventListener("click", () => $("#file-input").click());
$("#file-input").addEventListener("change", async () => {
  for (const f of Array.from($("#file-input").files)) {
    const dataUrl = await new Promise((res) => {
      const r = new FileReader();
      r.onload = () => res(r.result);
      r.readAsDataURL(f);
    });
    if ((f.type || "").startsWith("image/")) {
      attached.push({ mime_type: f.type, data: dataUrl });
    } else {
      const r = await api("/api/v1/workspace/upload", {
        method: "POST",
        body: JSON.stringify({ name: f.name, data: dataUrl }),
      });
      const d = await r.json();
      if (!r.ok) {
        alert("Upload failed: " + (d.error || r.status));
        continue;
      }
      attachedFiles.push({ name: f.name, path: d.path });
    }
  }
  $("#file-input").value = "";
  renderAttachments();
});
$("#attachments").addEventListener("click", (ev) => {
  const btn = ev.target.closest(".chip-x");
  if (!btn) return;
  if (btn.dataset.kind === "file") attachedFiles.splice(Number(btn.dataset.i), 1);
  else attached.splice(Number(btn.dataset.i), 1);
  renderAttachments();
});

// ---------- modes ----------
function setMode(m) {
  currentMode = m;
  $$("#tab-chat .mode-pills .pill").forEach((p) => p.classList.toggle("active", p.id === "btn-mode-" + m));
}
$("#btn-mode-single").addEventListener("click", () => setMode("single"));
$("#btn-mode-swarm").addEventListener("click", () => setMode("swarm"));
$("#btn-mode-polish").addEventListener("click", () => setMode("polish"));

// ---------- model / agent picker ----------
// Lists the enabled agents so a turn can be pinned to a specific model
// (DeepSeek 1/2/3, Gemini…). "Auto" leaves the choice to the swarm/orchestrator.
function renderAgentPicker() {
  const sel = $("#sel-agent");
  if (!sel) return;
  const prev = sel.value;
  const enabled = agents.filter((a) => a.enabled);
  sel.innerHTML =
    '<option value="">Auto</option>' +
    enabled
      .map((a) => {
        const provider = a.provider === "openai" ? "DeepSeek" : "Gemini";
        return `<option value="${esc(a.slug)}">${esc(a.name || a.slug)} · ${provider}</option>`;
      })
      .join("");
  if ([...sel.options].some((o) => o.value === prev)) sel.value = prev;
}

$("#sel-agent").addEventListener("change", () => {
  // Pinning a specific agent is always a single-agent turn.
  if ($("#sel-agent").value) setMode("single");
});

// ---------- refine idea toggle ----------
$("#btn-refine").addEventListener("click", () => {
  $("#btn-refine").classList.toggle("active");
});

// ---------- send + stream ----------
function setRunning(v) {
  running = v;
  $("#btn-run").disabled = v;
  $("#btn-stop").classList.toggle("hidden", !v);
  $("#live-dot").classList.toggle("running", v);
  const cons = document.querySelector(".console");
  if (cons) cons.classList.toggle("running", v);
  $("#run-status").classList.toggle("running", v);
}

async function sendMessage() {
  const text = $("#task").value.trim();
  if (!text || running) return;
  $("#task").value = "";
  if (!currentChatId) await newChat();
  if (!currentChatId) return;

  autoFollow = true;
  addBubble("user", text);
  const images = attached.map((a) => ({ mime_type: a.mime_type, data: a.data }));
  let fullText = text;
  if (attachedFiles.length) {
    fullText +=
      "\n\nAttached files (saved in the workspace, read them with the file tools): " +
      attachedFiles.map((f) => f.path).join(", ");
  }
  attached = [];
  attachedFiles = [];
  renderAttachments();

  setRunning(true);
  $("#run-status").textContent = "Thinking…";
  const block = addWorkBlock();

  try {
    const res = await api("/api/v1/chats/" + encodeURIComponent(currentChatId) + "/messages", {
      method: "POST",
      body: JSON.stringify({
        text: fullText,
        mode: currentMode,
        images,
        reasoning: reasoningLevel(),
        refine: $("#btn-refine").classList.contains("active"),
        agent: $("#sel-agent").value || "",
      }),
    });
    const data = await res.json();
    if (!res.ok) {
      block.remove();
      addBubble("assistant", "Error: " + (data.error || res.status));
      $("#run-status").textContent = "Error: " + (data.error || res.status);
      return;
    }
    currentRunId = data.run_id;
    await streamRun(data.run_id, block);
  } catch (e) {
    block.remove();
    addBubble("assistant", "Error: " + e.message);
    $("#run-status").textContent = "Error: " + e.message;
  } finally {
    setRunning(false);
    loadChats();
  }
}

async function stopRun() {
  if (!currentRunId) return;
  $("#run-status").textContent = "Stopping…";
  try {
    await api("/api/v1/swarm/run/" + currentRunId + "/stop", { method: "POST" });
  } catch (e) {}
}

function streamRun(runId, block) {
  return new Promise((resolve) => {
    const es = new EventSource(authUrl("/api/v1/swarm/run/" + runId + "/events"));
    currentES = es;
    const lines = block.querySelector(".work-lines");
    let toolCount = 0;
    let toolSummary = null;
    let toolList = null;

    const addLine = (agent, txt, cls) => {
      const d = document.createElement("div");
      d.className = "work-line" + (cls ? " " + cls : "");
      if (agent) d.innerHTML = '<span class="w-agent">' + esc(agent) + "</span>";
      const t = document.createElement("span");
      t.className = cls === "refined" ? "w-text w-text-full" : "w-text";
      t.textContent = txt;
      d.appendChild(t);
      lines.appendChild(d);
      scrollConsoleToBottom();
    };

    const ensureTools = () => {
      if (!toolSummary) {
        toolSummary = document.createElement("div");
        toolSummary.className = "work-tools";
        toolList = document.createElement("div");
        toolList.className = "work-tool-list";
        block.appendChild(toolSummary);
        block.appendChild(toolList);
        toolSummary.onclick = () => {
          toolList.style.display = toolList.style.display === "block" ? "none" : "block";
        };
      }
    };

    const finish = () => {
      es.close();
      if (currentES === es) currentES = null;
      resolve();
    };

    es.onmessage = (ev) => {
      let e;
      try { e = JSON.parse(ev.data); } catch { return; }

      if (e.type === "usage" && (e.usage?.total_tokens || e.cost)) {
        const u = e.usage || {};
        $("#live-total").textContent = fmt(u.total_tokens);
        $("#live-split").textContent = "in " + fmt(u.prompt_tokens) + " · out " + fmt(u.completion_tokens);
        if (e.cost) $("#live-cost").textContent = fmtCost(e.cost);
        $("#run-status").textContent = (e.agent || "") + " " + fmt(u.total_tokens) + " tok · " + fmtCost(e.cost);
        updateCtxLeft(e.agent, u.prompt_tokens);
      }

      switch (e.type) {
        case "start":
          lastPrompt = {};
          updateCtxLeft();
          break;
        case "planning":
          addLine("orchestrator", e.detail || "planning…");
          break;
        case "plan":
          addLine("orchestrator", e.detail || "plan ready");
          break;
        case "subtask":
          addLine(e.agent || "agent", e.detail || "");
          break;
        case "subtask_done":
          addLine(e.agent || "agent", "done");
          break;
        case "verify":
          addLine("verify", e.detail || "", "verify");
          break;
        case "prompt_refined":
          addLine("prompt", e.detail || "", "refined");
          break;
        case "fix_round":
          addLine("fix round", e.detail || "", "fix");
          break;
        case "warning":
          addLine("warning", e.detail || "", "verify");
          break;
        case "tool": {
          toolCount++;
          ensureTools();
          toolSummary.textContent = toolCount + " tool" + (toolCount === 1 ? "" : "s") + " called";
          const d = document.createElement("div");
          d.className = e.direction === "write" ? "write" : "";
          d.textContent = (e.direction === "write" ? "+ " : "") + (e.tool || "") + (e.detail ? " — " + e.detail : "");
          toolList.appendChild(d);
          break;
        }
        case "browser_frame":
          updateBrowserFrame(e);
          break;
        case "done":
          block.remove();
          addBubble("assistant", e.result?.final || "Done.");
          $("#run-status").textContent = "Done.";
          finish();
          break;
        case "error":
          block.remove();
          addBubble("assistant", "Error: " + (e.error || "unknown"));
          $("#run-status").textContent = "Error: " + (e.error || "unknown");
          finish();
          break;
        case "stopped":
          block.remove();
          addBubble("assistant", "Stopped.");
          $("#run-status").textContent = "Stopped.";
          finish();
          break;
      }
    };

    es.onerror = () => {
      $("#run-status").textContent = "Disconnected.";
      finish();
    };
  });
}

// ---------- reasoning-level slider (Faster <-> Smarter) ----------
const REASONING_LEVELS = ["min", "low", "normal", "high", "max"];
const REASONING_LABELS = ["Fast", "Low", "Normal", "High", "Max"];
function reasoningLevel() {
  return REASONING_LEVELS[Number($("#sel-reasoning").value)] || "normal";
}
function updateReasoningUI() {
  const v = Number($("#sel-reasoning").value);
  $("#reasoning-value").textContent = REASONING_LABELS[v] || "Normal";
  $("#sel-reasoning").style.setProperty("--fill", (v / 4) * 100 + "%");
}
$("#sel-reasoning").addEventListener("input", updateReasoningUI);
updateReasoningUI();

// ---------- voice input (server-side capture; works in WebKitGTK) ----------
// The native app's WebKitGTK webview denies getUserMedia, so audio is captured
// by the server (ffmpeg from the host microphone) and transcribed via STT_CMD.
let voiceCapturing = false;
let voiceCaptureID = null;

$("#btn-mic").addEventListener("click", async () => {
  const btn = $("#btn-mic");
  if (voiceCapturing) {
    voiceCapturing = false;
    btn.classList.remove("recording");
    $("#run-status").textContent = "Transcribing…";
    try {
      const r = await api("/api/v1/voice/stop", {
        method: "POST",
        body: JSON.stringify({ capture_id: voiceCaptureID }),
      });
      const d = await r.json();
      if (!r.ok) {
        $("#run-status").textContent = "Voice: " + (d.error || r.status);
        return;
      }
      $("#task").value = ($("#task").value.trim() + " " + (d.text || "")).trim();
      $("#run-status").textContent = "Voice input ready.";
    } catch (e) {
      $("#run-status").textContent = "Voice: " + e.message;
    }
    return;
  }
  try {
    const r = await api("/api/v1/voice/start", { method: "POST", body: "{}" });
    const d = await r.json();
    if (!r.ok) {
      $("#run-status").textContent = "Voice: " + (d.error || r.status);
      return;
    }
    voiceCaptureID = d.capture_id;
    voiceCapturing = true;
    btn.classList.add("recording");
    $("#run-status").textContent = "Recording… click the mic again to stop.";
  } catch (e) {
    $("#run-status").textContent = "Voice: " + e.message;
  }
});

// ---------- context-left indicator ----------
// Tracks, per agent, the last prompt size seen in usage events and shows the
// average free headroom against the model's context window (deepseek-chat:
// 64K tokens). Nothing is limited or chosen by the user — this only displays
// how much context the agents have left.
const CTX_WINDOW = 65536;
let lastPrompt = {};

function updateCtxLeft(agent, promptTokens) {
  if (agent) lastPrompt[agent] = promptTokens || 0;
  const agents = Object.keys(lastPrompt);
  if (!agents.length) {
    $("#ctx-left").textContent = "—";
    $("#ctx-detail").textContent = "starts during a run";
    $("#ctx-fill").style.width = "0%";
    return;
  }
  let sum = 0;
  for (const a of agents) sum += Math.max(0, CTX_WINDOW - lastPrompt[a]);
  const avgFree = sum / agents.length;
  const pct = Math.round((avgFree / CTX_WINDOW) * 100);
  $("#ctx-left").textContent = fmt(avgFree) + " tok";
  $("#ctx-detail").textContent = agents.length + " agent" + (agents.length === 1 ? "" : "s") + " · " + pct + "% free";
  $("#ctx-fill").style.width = pct + "%";
}

// ---------- live agent browsers ----------
const framesByAgent = {};
let activeBrowserAgent = null;

function updateBrowserFrame(e) {
  if (!e.agent || !e.image) return;
  const prev = framesByAgent[e.agent];
  if (prev && prev.image === e.image && prev.url === (e.url || "")) return; // no new frame
  framesByAgent[e.agent] = { url: e.url || "", title: e.title || "", image: e.image };
  const empty = $("#browser-empty");
  if (empty) empty.remove();
  if (!activeBrowserAgent || !framesByAgent[activeBrowserAgent]) activeBrowserAgent = e.agent;
  syncBrowserPanel();
}

function syncBrowserPanel() {
  const tabsEl = $("#browser-tabs");
  tabsEl.innerHTML = "";
  for (const [agent] of Object.entries(framesByAgent)) {
    const t = document.createElement("button");
    t.className = "browser-tab" + (agent === activeBrowserAgent ? " active" : "");
    t.textContent = agent;
    t.title = agent;
    t.onclick = () => {
      activeBrowserAgent = agent;
      syncBrowserPanel();
    };
    tabsEl.appendChild(t);
  }
  const f = framesByAgent[activeBrowserAgent];
  if (!f) return;
  $("#browser-url").textContent = f.url || "—";
  $("#browser-url").title = f.url || "";
  const img = $("#browser-img");
  img.src = "data:image/png;base64," + f.image;
  img.classList.remove("hidden");
  const ph = $("#browser-placeholder");
  if (ph) ph.classList.add("hidden");
}

// ---------- file tree ----------
function treeRow(n, depth) {
  const row = document.createElement("div");
  row.className = "ft-row";
  row.style.paddingLeft = 8 + depth * 14 + "px";
  row.dataset.depth = depth;
  if (n.dir) {
    row.innerHTML = '<span class="ft-caret">▸</span><span class="ft-name ft-dir">' + esc(n.name) + "/</span>";
    row.onclick = () => toggleDir(row, n.path);
    return row;
  }
  row.innerHTML =
    '<span class="ft-caret"></span><span class="ft-name">' + esc(n.name) + "</span>" +
    '<span class="ft-size">' + fmtSize(n.size) + "</span>";
  row.onclick = () => viewFile(n.path);
  return row;
}

function toggleDir(row, path) {
  const kids = row.nextSibling;
  if (kids && kids.classList && kids.classList.contains("ft-kids")) {
    const open = row.dataset.open === "1";
    if (open) kids.remove();
    row.dataset.open = open ? "" : "1";
    row.querySelector(".ft-caret").textContent = open ? "▸" : "▾";
    return;
  }
  const wrap = document.createElement("div");
  wrap.className = "ft-kids";
  wrap.innerHTML = '<span class="ft-loading">…</span>';
  row.parentNode.insertBefore(wrap, row.nextSibling);
  row.dataset.open = "1";
  row.querySelector(".ft-caret").textContent = "▾";
  const depth = Number(row.dataset.depth || 0) + 1;
  api("/api/v1/workspace/tree?path=" + encodeURIComponent(path))
    .then((r) => r.json())
    .then((d) => {
      wrap.innerHTML = "";
      for (const c of d.entries || []) wrap.appendChild(treeRow(c, depth));
      if (!wrap.children.length) wrap.innerHTML = '<span class="ft-loading">empty</span>';
    })
    .catch(() => { wrap.innerHTML = '<span class="ft-loading">error</span>'; });
}

function renderTree(container, entries) {
  container.innerHTML = "";
  if (!entries || !entries.length) {
    container.innerHTML = '<p class="hint">No files yet. Run a task.</p>';
    return;
  }
  for (const n of entries) container.appendChild(treeRow(n, 0));
}

async function loadFiles() {
  const res = await api("/api/v1/workspace/tree");
  if (!res.ok) return;
  const data = await res.json();
  renderTree($("#files-list"), data.entries || []);
}

async function loadSidebarFiles() {
  const el = $("#sidebar-files");
  const res = await api("/api/v1/workspace/tree");
  if (!res.ok) return;
  const data = await res.json();
  renderTree(el, data.entries || []);
}

async function viewFile(path) {
  const res = await api("/api/v1/workspace/files/" + encodeURIComponent(path));
  if (!res.ok) return;
  const data = await res.json();
  const filesTab = document.querySelector('.tab[data-tab="files"]');
  if (filesTab) filesTab.click();
  $("#file-viewer-title").textContent = data.path;
  const box = $("#file-viewer-content");
  box.textContent = "";
  if (data.mime_type && data.base64) {
    const img = document.createElement("img");
    img.src = "data:" + data.mime_type + ";base64," + data.base64;
    img.style.maxWidth = "100%";
    img.style.background = "#fff";
    box.appendChild(img);
  } else {
    box.textContent = data.content || "";
  }
  $("#file-viewer").classList.remove("hidden");
}

$("#btn-clear-ws").addEventListener("click", async () => {
  if (!confirm("Clear the whole workspace?")) return;
  await api("/api/v1/workspace/clear", { method: "POST" });
  loadFiles();
  loadSidebarFiles();
});

// ---------- working folder ----------
function setWsBar(root) {
  const el = $("#ws-bar-root");
  if (!el) return;
  const r = root || "";
  el.textContent = r || "…";
  el.title = r;
}

async function loadWsRoot() {
  const r = await api("/api/v1/workspace/root");
  if (!r.ok) return;
  const d = await r.json();
  $("#ws-root").value = d.root || "";
  setWsBar(d.root);
}

$("#btn-set-root").addEventListener("click", async () => {
  const path = $("#ws-root").value.trim();
  if (!path) return;
  await applyWsRoot(path);
});

// ---------- folder picker (browse any host directory) ----------
let dirPath = "/";

async function applyWsRoot(path) {
  const r = await api("/api/v1/workspace/root", {
    method: "POST",
    body: JSON.stringify({ path }),
  });
  const d = await r.json();
  if (!r.ok) {
    alert("Cannot set folder: " + (d.error || r.status));
    return false;
  }
  $("#ws-root").value = d.root;
  setWsBar(d.root);
  loadFiles();
  loadSidebarFiles();
  return true;
}

async function loadDir(p) {
  const r = await api("/api/v1/fs/dirs?path=" + encodeURIComponent(p));
  const d = await r.json();
  if (!r.ok) {
    alert("Cannot read directory: " + (d.error || r.status));
    return;
  }
  dirPath = d.path;
  $("#dir-path").textContent = d.path;
  if ($("#dir-input")) $("#dir-input").value = d.path;
  const list = $("#dir-list");
  list.innerHTML = "";
  const folderSvg = '<svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"/></svg>';
  for (const dir of d.dirs || []) {
    const row = document.createElement("div");
    row.className = "dir-row";
    row.innerHTML = folderSvg + '<span>' + esc(dir.name) + "</span>";
    row.onclick = () => loadDir(dir.path);
    list.appendChild(row);
  }
  if (!(d.dirs || []).length) {
    const empty = document.createElement("div");
    empty.className = "hint";
    empty.textContent = "no subdirectories";
    list.appendChild(empty);
  }
}

function openDirModal() {
  $("#dir-modal").classList.remove("hidden");
  loadDir($("#ws-root").value.trim() || dirPath);
}
function closeDirModal() {
  $("#dir-modal").classList.add("hidden");
}

$("#btn-browse-dir").addEventListener("click", openDirModal);
$("#btn-change-folder").addEventListener("click", openDirModal);
$("#dir-close").addEventListener("click", closeDirModal);
$("#dir-cancel").addEventListener("click", closeDirModal);
$("#dir-modal").addEventListener("click", (ev) => {
  if (ev.target === $("#dir-modal")) closeDirModal();
});
$("#dir-up").addEventListener("click", () => loadDir($("#dir-path").textContent + "/.."));
$("#dir-home").addEventListener("click", async () => {
  const r = await api("/api/v1/fs/home");
  if (r.ok) {
    const d = await r.json();
    loadDir(d.home || "/");
  } else {
    loadDir("/");
  }
});
$("#dir-go").addEventListener("click", () => {
  const p = $("#dir-input").value.trim();
  if (p) loadDir(p);
});
$("#dir-input").addEventListener("keydown", (e) => {
  if (e.key === "Enter") {
    e.preventDefault();
    const p = $("#dir-input").value.trim();
    if (p) loadDir(p);
  }
});
$("#dir-select").addEventListener("click", async () => {
  if (await applyWsRoot(dirPath)) closeDirModal();
});

// ---------- agents ----------
let agents = [];
let editingId = null;

async function loadAgents() {
  const res = await api("/api/v1/services");
  if (!res.ok) return;
  agents = await res.json();
  const el = $("#agents-list");
  if (!agents.length) {
    el.innerHTML = '<p class="hint">No agents yet. Click "Add agent".</p>';
    return;
  }
  el.innerHTML = agents
    .map(
      (a) => `
    <div class="card">
      <div class="meta">
        <div class="name">${esc(a.name || a.slug)} <span class="badge">${esc(a.slug)}</span>
          <span class="badge">${a.provider === "openai" ? "deepseek" : "gemini"}</span>
          ${a.enabled ? "" : '<span class="badge">disabled</span>'}</div>
        <div class="sub">model: ${esc(a.model || "auto")} | proxy: ${esc(a.proxy_host || "—")}:${a.proxy_port || 0} | key: ${a.has_api_key ? "set" : "not set"}</div>
      </div>
      <div class="actions">
        <button class="btn ghost small" data-act="edit" data-id="${esc(a.id)}">Edit</button>
        <button class="btn ghost small" data-act="toggle" data-id="${esc(a.id)}">${a.enabled ? "Disable" : "Enable"}</button>
        <button class="btn ghost small" data-act="del" data-id="${esc(a.id)}">Delete</button>
      </div>
    </div>`
    )
    .join("");
  renderAgentPicker();
}

$("#agents-list").addEventListener("click", (ev) => {
  const b = ev.target.closest("[data-act]");
  if (!b) return;
  const id = b.dataset.id;
  if (b.dataset.act === "edit") editAgent(id);
  if (b.dataset.act === "toggle") toggleAgent(id);
  if (b.dataset.act === "del") deleteAgent(id);
});

function openNewAgent() {
  editingId = null;
  $("#agent-form-title").textContent = "Add agent";
  ["f-name", "f-slug", "f-baseurl", "f-model", "f-key", "f-phost", "f-pport", "f-puser", "f-ppass"].forEach((id) => ($("#" + id).value = ""));
  $("#f-provider").value = "gemini";
  $("#f-baseurl").value = "https://generativelanguage.googleapis.com/v1beta";
  $("#f-enabled").checked = true;
  $("#agent-form").classList.remove("hidden");
}

function editAgent(id) {
  const a = agents.find((x) => x.id === id);
  if (!a) return;
  editingId = id;
  $("#agent-form-title").textContent = "Edit agent";
  $("#f-name").value = a.name || "";
  $("#f-slug").value = a.slug || "";
  $("#f-baseurl").value = a.upstream_url || "";
  $("#f-provider").value = a.provider === "openai" ? "openai" : "gemini";
  $("#f-model").value = a.model || "";
  $("#f-key").value = "";
  $("#f-phost").value = a.proxy_host || "";
  $("#f-pport").value = a.proxy_port || "";
  $("#f-puser").value = a.proxy_username || "";
  $("#f-ppass").value = "";
  $("#f-enabled").checked = !!a.enabled;
  $("#agent-form").classList.remove("hidden");
}

async function saveAgent() {
  const name = $("#f-name").value.trim();
  const slug = $("#f-slug").value.trim() || name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
  const provider = $("#f-provider").value === "openai" ? "openai" : "gemini";
  const body = {
    name,
    slug,
    upstream_url: $("#f-baseurl").value.trim(),
    provider,
    auth_header: provider === "openai" ? "Authorization" : "x-goog-api-key",
    model: $("#f-model").value.trim() || (provider === "openai" ? "deepseek-chat" : ""),
    proxy_host: $("#f-phost").value.trim(),
    proxy_port: parseInt($("#f-pport").value, 10) || 0,
    proxy_username: $("#f-puser").value.trim(),
    enabled: $("#f-enabled").checked,
  };
  if (provider === "openai") body.auth_scheme = "Bearer";
  if ($("#f-key").value) body.upstream_api_key = $("#f-key").value;
  if ($("#f-ppass").value) body.proxy_password = $("#f-ppass").value;

  const url = editingId ? "/api/v1/services/" + editingId : "/api/v1/services";
  const res = await api(url, { method: editingId ? "PUT" : "POST", body: JSON.stringify(body) });
  if (!res.ok) {
    const e = await res.json().catch(() => ({}));
    alert("Error: " + (e.error || res.status));
    return;
  }
  $("#agent-form").classList.add("hidden");
  loadAgents();
}

async function toggleAgent(id) {
  const a = agents.find((x) => x.id === id);
  if (!a) return;
  await api("/api/v1/services/" + id, { method: "PUT", body: JSON.stringify({ enabled: !a.enabled }) });
  loadAgents();
}

async function deleteAgent(id) {
  if (!confirm("Delete this agent?")) return;
  await api("/api/v1/services/" + id, { method: "DELETE" });
  loadAgents();
}

$("#btn-new-agent").addEventListener("click", openNewAgent);
$("#btn-save-agent").addEventListener("click", saveAgent);
$("#btn-cancel-agent").addEventListener("click", () => $("#agent-form").classList.add("hidden"));
$("#f-provider").addEventListener("change", () => {
  const openai = $("#f-provider").value === "openai";
  const cur = $("#f-baseurl").value;
  if (openai && (!cur || cur.includes("generativelanguage"))) $("#f-baseurl").value = "https://api.deepseek.com";
  if (!openai && (!cur || cur.includes("deepseek"))) $("#f-baseurl").value = "https://generativelanguage.googleapis.com/v1beta";
});

// ---------- stats ----------
async function fetchStats() {
  const res = await api("/api/v1/stats");
  if (!res.ok) return null;
  return res.json();
}

function renderBars(rows) {
  if (!rows || !rows.length) return '<div class="hint">no data</div>';
  const max = Math.max.apply(null, rows.map((r) => r.total_tokens || 0)) || 1;
  return rows
    .map(
      (r) => `
    <div class="bar-row">
      <span class="bar-label" title="${esc(r.key)}">${esc(r.key)}</span>
      <div class="bar-track"><div class="bar-fill" style="width:${((r.total_tokens / max) * 100).toFixed(1)}%"></div></div>
      <span class="bar-val">${fmt(r.total_tokens)}</span>
    </div>`
    )
    .join("");
}

function renderGroup(rows) {
  if (!rows || !rows.length) return '<p class="hint">no data</p>';
  return rows
    .map(
      (r) => `
    <div class="card">
      <div class="meta"><div class="name">${esc(r.key)}</div><div class="sub">${fmt(r.total_tokens)} tokens · ${fmt(r.calls)} calls</div></div>
      <div class="actions"><span class="hint">${fmtCost(r.cost)}</span></div>
    </div>`
    )
    .join("");
}

async function loadStats() {
  const d = await fetchStats();
  if (!d) return;
  const t = d.totals || {};
  $("#stat-totals").innerHTML = `
    <div class="stat-card"><div class="stat-num">${fmtCost(t.cost)}</div><div class="stat-label">spend (USD)</div></div>
    <div class="stat-card"><div class="stat-num">${fmt(t.total_tokens)}</div><div class="stat-label">total tokens</div></div>
    <div class="stat-card"><div class="stat-num">${fmt(t.prompt_tokens)}</div><div class="stat-label">input</div></div>
    <div class="stat-card"><div class="stat-num">${fmt(t.completion_tokens)}</div><div class="stat-label">output</div></div>
    <div class="stat-card"><div class="stat-num">${fmt(t.calls)}</div><div class="stat-label">LLM calls</div></div>
    <div class="stat-card"><div class="stat-num">${fmt(t.runs)}</div><div class="stat-label">runs</div></div>`;
  $("#stat-by-model").innerHTML = renderGroup(d.by_model);
  $("#stat-by-agent").innerHTML = renderGroup(d.by_agent);
  $("#stat-by-run").innerHTML = (d.by_run || []).length
    ? d.by_run
        .map(
          (r) => `
    <div class="card">
      <div class="meta"><div class="name">${esc(r.run_id)}</div><div class="sub">${esc(r.task || "")}</div></div>
      <div class="actions"><span class="hint">${fmt(r.total_tokens)} tok · ${fmt(r.calls)} calls · ${fmtCost(r.cost)}</span></div>
    </div>`
        )
        .join("")
    : '<p class="hint">No runs yet.</p>';
  renderTimelineSelector(d.by_run || []);
  // live panel
  $("#live-total").textContent = fmt(t.total_tokens);
  $("#live-split").textContent = "in " + fmt(t.prompt_tokens) + " · out " + fmt(t.completion_tokens);
  $("#live-cost").textContent = fmtCost(t.cost);
  $("#live-by-model").innerHTML = renderBars(d.by_model);
  $("#live-by-agent").innerHTML = renderBars(d.by_agent);
}

function renderTimelineSelector(runs) {
  const sel = $("#timeline-run");
  sel.innerHTML = '<option value="">select a run…</option>' +
    runs.map((r) => `<option value="${esc(r.run_id)}">${esc(r.run_id)}</option>`).join("");
}
$("#timeline-run").addEventListener("change", () => {
  const id = $("#timeline-run").value;
  if (id) loadTimeline(id);
});

async function loadTimeline(runId) {
  const el = $("#timeline");
  el.innerHTML = '<p class="hint">Loading…</p>';
  const res = await api("/api/v1/runs/" + encodeURIComponent(runId) + "/timeline");
  if (!res.ok) {
    el.innerHTML = '<p class="hint">Could not load timeline.</p>';
    return;
  }
  const data = await res.json();
  renderTimeline(data.timeline || []);
}

function renderTimeline(bars) {
  const el = $("#timeline");
  if (!bars || !bars.length) {
    el.innerHTML = '<p class="hint">No subtasks recorded for this run.</p>';
    return;
  }
  const times = bars.map((b) => new Date(b.started_at).getTime());
  const ends = bars.filter((b) => b.ended_at).map((b) => new Date(b.ended_at).getTime());
  const t0 = Math.min.apply(null, times);
  const t1 = Math.max.apply(null, times.concat(ends));
  const span = Math.max(1, t1 - t0);
  const agents = [...new Set(bars.map((b) => b.agent))];
  el.innerHTML = agents
    .map((a) => {
      const barsHtml = bars
        .filter((b) => b.agent === a)
        .map((b) => {
          const s = new Date(b.started_at).getTime();
          const e = b.ended_at ? new Date(b.ended_at).getTime() : t1;
          const left = ((s - t0) / span) * 100;
          const width = Math.max(2, ((e - s) / span) * 100);
          const label = "step " + (b.step + 1);
          return `<div class="tl-bar" style="left:${left.toFixed(2)}%;width:${width.toFixed(2)}%" title="${esc(label)}">${esc(label)}</div>`;
        })
        .join("");
      return `<div class="tl-row"><div class="tl-agent">${esc(a)}</div><div class="tl-track">${barsHtml}</div></div>`;
    })
    .join("");
}

// ---------- tool modules ----------
async function loadModules() {
  const res = await api("/api/v1/tools/modules");
  if (!res.ok) return;
  const mods = await res.json();
  $("#modules-list").innerHTML = mods
    .map(
      (m) => `
    <div class="card">
      <div class="meta">
        <div class="name">${esc(m.label)} <span class="badge">${esc(m.name)}</span></div>
        <div class="sub">${(m.tools || []).map(esc).join(" · ")}</div>
      </div>
      ${m.available
        ? `<label class="check">enabled <input type="checkbox" data-module="${esc(m.name)}" ${m.enabled ? "checked" : ""} /></label>`
        : '<span class="hint">runtime unavailable</span>'}
    </div>`
    )
    .join("");
}
$("#modules-list").addEventListener("change", async (ev) => {
  const cb = ev.target.closest("input[data-module]");
  if (!cb) return;
  await api("/api/v1/tools/modules", {
    method: "PUT",
    body: JSON.stringify({ name: cb.dataset.module, enabled: cb.checked }),
  });
});

// ---------- models info ----------
async function loadModels() {
  const res = await api("/api/v1/models");
  if (!res.ok) return;
  const d = await res.json();
  $("#models-info").textContent =
    "orchestrator: " + (d.orchestrator_model || "—") +
    " · default: " + (d.default_model || "—") +
    " · available: " + (d.available_models || []).join(", ");
}

// ---------- server status + splash ----------
let splashHidden = false;
function hideSplash() {
  if (splashHidden) return;
  splashHidden = true;
  const el = $("#splash");
  if (el) el.classList.add("done");
}

async function checkHealth() {
  try {
    const res = await fetch("/healthz");
    $("#server-status").className = "dot" + (res.ok ? " ok" : "");
    if (res.ok) hideSplash();
  } catch (e) {
    $("#server-status").className = "dot";
  }
}

// ---------- bindings + init ----------
$("#btn-run").addEventListener("click", sendMessage);
$("#btn-stop").addEventListener("click", stopRun);
$("#btn-new-chat").addEventListener("click", () => newChat());
$("#task").addEventListener("keydown", (e) => {
  if (e.key === "Enter" && !e.shiftKey) {
    e.preventDefault();
    sendMessage();
  }
});

// copy buttons inside markdown code blocks
$("#console-scroll").addEventListener("click", (ev) => {
  const b = ev.target.closest(".code-copy");
  if (!b) return;
  navigator.clipboard.writeText(b.dataset.code).catch(() => {});
});

// diagrams (mermaid) render with the app theme
if (typeof mermaid !== "undefined") {
  mermaid.initialize({
    startOnLoad: false,
    securityLevel: "strict",
    theme: "dark",
    themeVariables: {
      darkMode: true,
      background: "#111111",
      primaryColor: "#222222",
      primaryTextColor: "#ffffff",
      primaryBorderColor: "#ffffff",
      lineColor: "#ffffff",
      secondaryColor: "#1a1a1a",
      tertiaryColor: "#101010",
      fontFamily: "ui-sans-serif, system-ui, sans-serif",
      fontSize: "13px",
    },
  });
}

loadChats();
loadAgents();
loadModels();
loadStats();
loadSidebarFiles();
loadWsRoot();
checkHealth();
setInterval(checkHealth, 10000);
setInterval(async () => {
  const d = await fetchStats();
  if (!d) return;
  const t = d.totals || {};
  $("#live-total").textContent = fmt(t.total_tokens);
  $("#live-split").textContent = "in " + fmt(t.prompt_tokens) + " · out " + fmt(t.completion_tokens);
  $("#live-cost").textContent = fmtCost(t.cost);
  $("#live-by-model").innerHTML = renderBars(d.by_model);
  $("#live-by-agent").innerHTML = renderBars(d.by_agent);
}, 2000);

// When no run is streaming (page reloaded mid-run, or a run started from the
// CLI), poll the latest browser frames so the panel never stays empty.
setInterval(async () => {
  if (running) return; // SSE owns updates during a run
  try {
    const r = await api("/api/v1/swarm/latest-browsers");
    if (!r.ok) return;
    const d = await r.json();
    for (const f of d.frames || []) updateBrowserFrame(f);
  } catch (e) {}
}, 2500);
