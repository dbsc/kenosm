// kenosm frontend — black and white, no emojis.

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => document.querySelectorAll(sel);

// ---------- Tabs ----------
$$(".tab").forEach((btn) => {
  btn.addEventListener("click", () => {
    $$(".tab").forEach((b) => b.classList.remove("active"));
    $$(".tab-panel").forEach((p) => p.classList.remove("active"));
    btn.classList.add("active");
    $("#tab-" + btn.dataset.tab).classList.add("active");
    if (btn.dataset.tab === "files") loadFiles();
    if (btn.dataset.tab === "agents") loadAgents();
    if (btn.dataset.tab === "stats") loadStats();
    if (btn.dataset.tab === "tools") loadModules();
  });
});

// Delegated clicks for the dynamically-rendered action buttons. Values live in
// data-* attributes and are read back through dataset, so agent names, file
// paths and any other content can never break out into executable script (no
// inline onclick string interpolation).
document.addEventListener("click", (ev) => {
  const el = ev.target.closest("[data-action]");
  if (!el) return;
  switch (el.dataset.action) {
    case "editAgent": editAgent(el.dataset.id); break;
    case "toggleAgent": toggleAgent(el.dataset.id); break;
    case "deleteAgent": deleteAgent(el.dataset.id); break;
    case "removeAttachment": removeAttachment(Number(el.dataset.index)); break;
    case "viewFile": viewFile(el.dataset.path); break;
    case "toggleModule": toggleModule(el.dataset.module, el.checked); break;
  }
});

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
  if (v < 0.01) return "$" + v.toFixed(5);
  return "$" + v.toFixed(4);
}

// ---------- API auth ----------
let apiKey = localStorage.getItem("kenosm_key") || "";

// apiFetch injects the bearer key (if any) into every API request and, on a
// 401, prompts for the key once and retries.
async function apiFetch(url, opts = {}) {
  if (!apiKey) apiKey = localStorage.getItem("kenosm_key") || "";
  const headers = new Headers(opts.headers || {});
  if (apiKey) headers.set("Authorization", "Bearer " + apiKey);
  let res = await window.fetch(url, { ...opts, headers });
  if (res.status === 401) {
    apiKey = await askForKey();
    if (apiKey) {
      headers.set("Authorization", "Bearer " + apiKey);
      res = await window.fetch(url, { ...opts, headers });
    }
  }
  return res;
}

function askForKey() {
  return new Promise((resolve) => {
    const k = prompt("Enter the API key:");
    if (k) {
      localStorage.setItem("kenosm_key", k);
      apiKey = k;
    }
    resolve(k);
  });
}

// EventSource cannot set request headers, so the key travels as a query param.
function eventsUrl(runId) {
  if (!apiKey) apiKey = localStorage.getItem("kenosm_key") || "";
  const base = "/api/v1/swarm/run/" + runId + "/events";
  return apiKey ? base + "?key=" + encodeURIComponent(apiKey) : base;
}

// ---------- Agents ----------
let agents = [];
let editingId = null;

async function loadAgents() {
  const res = await apiFetch("/api/v1/services");
  agents = await res.json();
  renderAgents();
}

function renderAgents() {
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
          <span class="badge">${a.provider === "openai" || a.provider === "deepseek" ? "deepseek" : "gemini"}</span>
          ${a.enabled ? "" : '<span class="badge">disabled</span>'}</div>
        <div class="sub">model: ${esc(a.model || "auto")} | proxy: ${esc(a.proxy_host)}:${a.proxy_port} | key: ${a.has_api_key ? "set" : "not set"}</div>
      </div>
      <div class="actions">
        <button class="btn small ghost" data-action="editAgent" data-id="${esc(a.id)}">Edit</button>
        <button class="btn small ghost" data-action="toggleAgent" data-id="${esc(a.id)}">${a.enabled ? "Disable" : "Enable"}</button>
        <button class="btn small ghost" data-action="deleteAgent" data-id="${esc(a.id)}">Delete</button>
      </div>
    </div>`
    )
    .join("");
}

function openNewAgent() {
  editingId = null;
  $("#agent-form-title").textContent = "Add agent";
  ["f-name", "f-slug", "f-baseurl", "f-model", "f-key", "f-phost", "f-pport", "f-puser", "f-ppass"].forEach((id) => ($("#" + id).value = ""));
  $("#f-provider").value = "gemini";
  $("#f-baseurl").value = "https://generativelanguage.googleapis.com/v1beta";
  $("#f-model").placeholder = "auto";
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
  $("#f-model").value = a.model || "";
  $("#f-provider").value = a.provider === "openai" || a.provider === "deepseek" ? "openai" : "gemini";
  $("#f-model").placeholder = a.provider === "openai" || a.provider === "deepseek" ? "deepseek-chat" : "auto";
  $("#f-key").value = "";
  $("#f-phost").value = a.proxy_host || "";
  $("#f-pport").value = a.proxy_port || "";
  $("#f-puser").value = a.proxy_username || "";
  $("#f-ppass").value = "";
  $("#f-enabled").checked = a.enabled;
  $("#agent-form").classList.remove("hidden");
}

async function saveAgent() {
  const name = $("#f-name").value.trim();
  const slug = ($("#f-slug").value.trim() || name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, ""));
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
  const method = editingId ? "PUT" : "POST";
  const res = await apiFetch(url, {
    method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
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
  await apiFetch("/api/v1/services/" + id, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ enabled: !a.enabled }),
  });
  loadAgents();
}

async function deleteAgent(id) {
  if (!confirm("Delete this agent?")) return;
  await apiFetch("/api/v1/services/" + id, { method: "DELETE" });
  loadAgents();
}

// ---------- Models ----------
async function loadModels() {
  try {
    const res = await apiFetch("/api/v1/models");
    const m = await res.json();
    $("#models-info").innerHTML =
      `orchestrator: <b>${esc(m.orchestrator_model)}</b><br>default: <b>${esc(m.default_model)}</b><br>available: ${esc((m.available_models || []).join(", "))}`;
  } catch (e) {}
}

// ---------- Attachments (images as the first request) ----------
let attached = []; // {mime_type, data, name}

$("#btn-attach").addEventListener("click", () => $("#file-input").click());
$("#file-input").addEventListener("change", (ev) => {
  for (const file of ev.target.files) addAttachment(file);
  ev.target.value = "";
});

function addAttachment(file) {
  const reader = new FileReader();
  reader.onload = () => {
    const dataURL = String(reader.result);
    const mime = dataURL.slice(5, dataURL.indexOf(";"));
    const data = dataURL.slice(dataURL.indexOf(",") + 1);
    attached.push({ mime_type: mime, data, name: file.name });
    renderAttachments();
  };
  reader.readAsDataURL(file);
}

function removeAttachment(i) {
  attached.splice(i, 1);
  renderAttachments();
}

function renderAttachments() {
  const el = $("#attachments");
  el.innerHTML = attached
    .map(
      (a, i) => `
      <span class="chip">
        <img src="data:${esc(a.mime_type)};base64,${esc(a.data)}" alt="" />
        ${esc(a.name)}
        <button class="chip-x" data-action="removeAttachment" data-index="${i}">x</button>
      </span>`
    )
    .join("");
}

// ---------- Chat ----------
let chats = [];
let currentChatId = null;
let currentMode = "single";
let currentRunId = null;
let currentES = null;

// Swarm graph state: one node per agent, laid out on a ring. Signals (lines)
// fly on SSE events: the planner dispatches work from the centre, and a finished
// agent broadcasts its result to the others.
const swarmNodes = new Map(); // agent name -> {x, y}
const swarmCenter = { x: 124, y: 124 };
const swarmRadius = 82;
const SVGNS = "http://www.w3.org/2000/svg";

function setRunning(running) {
  $("#btn-run").disabled = running;
  $("#btn-stop").classList.toggle("hidden", !running);
  if (!running) currentRunId = null;
}

function setMode(mode) {
  currentMode = mode;
  $("#btn-mode-single").classList.toggle("active", mode === "single");
  $("#btn-mode-swarm").classList.toggle("active", mode === "swarm");
  $("#btn-mode-polish").classList.toggle("active", mode === "polish");
}

// ---- chat list ----
async function loadChats() {
  const res = await apiFetch("/api/v1/chats");
  const data = await res.json();
  chats = data.chats || [];
  renderChatTabs();
  if (!chats.length) {
    await newChat();
  } else if (!currentChatId || !chats.find((c) => c.id === currentChatId)) {
    await selectChat(chats[0].id);
  }
}

async function newChat() {
  const res = await apiFetch("/api/v1/chats", { method: "POST" });
  const c = await res.json();
  chats.push(c);
  currentChatId = c.id;
  setMode(c.mode || "single");
  renderChatTabs();
  renderMessages([]);
  clearLiveFeed();
}

function renderChatTabs() {
  const el = $("#chat-tabs");
  el.innerHTML = chats
    .map(
      (c) => `
      <div class="chat-tab ${c.id === currentChatId ? "active" : ""}" data-id="${esc(c.id)}">
        <span class="chat-tab-title">${esc(c.title || "New chat")}</span>
        <button class="chat-tab-x" data-id="${esc(c.id)}" title="Delete chat">&times;</button>
      </div>`
    )
    .join("");
  el.querySelectorAll(".chat-tab").forEach((tab) => {
    tab.addEventListener("click", (ev) => {
      if (ev.target.classList.contains("chat-tab-x")) {
        deleteChat(tab.dataset.id);
        return;
      }
      selectChat(tab.dataset.id);
    });
  });
}

async function selectChat(id) {
  const res = await apiFetch("/api/v1/chats/" + encodeURIComponent(id));
  if (!res.ok) return;
  const c = await res.json();
  currentChatId = c.id;
  setMode(c.mode || "single");
  renderChatTabs();
  renderMessages(c.messages || []);
  clearLiveFeed();
}

async function deleteChat(id) {
  if (!confirm("Delete this chat?")) return;
  await apiFetch("/api/v1/chats/" + encodeURIComponent(id), { method: "DELETE" });
  chats = chats.filter((c) => c.id !== id);
  if (currentChatId === id) currentChatId = null;
  renderChatTabs();
  if (!chats.length) await newChat();
  else await selectChat(chats[0].id);
}

// ---- messages ----
function renderMessages(messages) {
  const c = $("#console-scroll");
  c.innerHTML = "";
  if (!messages.length) {
    c.innerHTML = '<div class="msg system" id="console-empty">New chat. Write a message below.</div>';
    return;
  }
  for (const m of messages) addMessageBubble(m.role, m.text);
}

function addMessageBubble(role, text) {
  const c = $("#console-scroll");
  const empty = $("#console-empty");
  if (empty) empty.remove();
  const div = document.createElement("div");
  div.className = "bubble " + (role === "user" ? "bubble-user" : "bubble-assistant");
  div.textContent = text;
  c.appendChild(div);
  c.scrollTop = c.scrollHeight;
  return div;
}

function addThinkingBubble() {
  const c = $("#console-scroll");
  const empty = $("#console-empty");
  if (empty) empty.remove();
  const div = document.createElement("div");
  div.className = "bubble bubble-assistant bubble-thinking";
  div.textContent = "…";
  c.appendChild(div);
  c.scrollTop = c.scrollHeight;
  return div;
}

// ---- send ----
async function sendMessage() {
  const text = $("#task").value.trim();
  if (!text) return;
  if (!currentChatId) await newChat();
  $("#task").value = "";

  addMessageBubble("user", text);
  const images = attached.map((a) => ({ mime_type: a.mime_type, data: a.data }));
  attached = [];
  renderAttachments();

  setRunning(true);
  $("#run-status").textContent = "Thinking…";
  const thinking = addThinkingBubble();

  try {
    const res = await apiFetch("/api/v1/chats/" + encodeURIComponent(currentChatId) + "/messages", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ text, mode: currentMode, images }),
    });
    const data = await res.json();
    if (!res.ok) {
      thinking.textContent = "Error: " + (data.error || res.status);
      thinking.classList.add("bubble-error");
      $("#run-status").textContent = "Error: " + (data.error || res.status);
      return;
    }
    currentRunId = data.run_id;
    await streamChatRun(data.run_id, thinking);
  } catch (e) {
    thinking.textContent = "Error: " + e.message;
    thinking.classList.add("bubble-error");
    $("#run-status").textContent = "Error: " + e.message;
  } finally {
    setRunning(false);
  }
}

async function stopRun() {
  if (!currentRunId) return;
  $("#run-status").textContent = "Stopping…";
  try {
    await apiFetch("/api/v1/swarm/run/" + currentRunId + "/stop", { method: "POST" });
  } catch (e) {}
}

function streamChatRun(runId, thinking) {
  return new Promise((resolve) => {
    const es = new EventSource(eventsUrl(runId));
    currentES = es;
    const finish = () => {
      es.close();
      if (currentES === es) currentES = null;
      resolve();
    };
    es.onmessage = (ev) => {
      let e;
      try {
        e = JSON.parse(ev.data);
      } catch {
        return;
      }
      liveFeed(e);
      swarmComm(e);
      if (e.type === "done") {
        thinking.textContent = e.result?.final || "";
        thinking.classList.remove("bubble-thinking");
        $("#run-status").textContent = "Done.";
        finish();
      } else if (e.type === "error") {
        thinking.textContent = "Error: " + (e.error || "unknown");
        thinking.classList.add("bubble-error");
        $("#run-status").textContent = "Error: " + (e.error || "unknown");
        finish();
      } else if (e.type === "stopped") {
        thinking.textContent = "Stopped.";
        thinking.classList.add("bubble-error");
        $("#run-status").textContent = "Stopped.";
        finish();
      }
    };
    es.onerror = () => {
      $("#run-status").textContent = "Disconnected.";
      finish();
    };
  });
}

// ---------- Files ----------
async function loadFiles() {
  const res = await apiFetch("/api/v1/workspace/files");
  const data = await res.json();
  const files = data.files || [];
  const el = $("#files-list");
  if (!files.length) {
    el.innerHTML = '<p class="hint">No files yet. Run a swarm task.</p>';
    return;
  }
  el.innerHTML = files
    .map(
      (f) => `<div class="card"><div class="meta name">${esc(f.path)}</div><div class="actions"><span class="hint">${f.size} bytes</span><button class="btn small ghost" data-action="viewFile" data-path="${esc(f.path)}">View</button></div></div>`
    )
    .join("");
}

async function viewFile(path) {
  const res = await apiFetch("/api/v1/workspace/files/" + encodeURIComponent(path));
  if (!res.ok) return;
  const data = await res.json();
  $("#file-viewer-title").textContent = data.path;
  const box = $("#file-viewer-content");
  box.textContent = "";
  if (data.mime_type && data.base64) {
    const img = document.createElement("img");
    img.src = "data:" + data.mime_type + ";base64," + data.base64;
    img.style.maxWidth = "100%";
    img.style.border = "1px solid #ccc";
    img.style.background = "#fff";
    box.appendChild(img);
  } else {
    box.textContent = data.content || "";
  }
  $("#file-viewer").classList.remove("hidden");
}

// ---------- Tool modules ----------
async function loadModules() {
  const res = await apiFetch("/api/v1/tools/modules");
  if (!res.ok) return;
  const mods = await res.json();
  const el = $("#modules-list");
  el.innerHTML = mods
    .map(
      (m) => `
      <div class="card">
        <div class="meta">
          <div class="name">${esc(m.label)} <span class="badge">${esc(m.name)}</span></div>
          <div class="sub">${m.tools.map(esc).join(" · ")}</div>
        </div>
        <div class="actions">
          ${m.available
            ? `<label class="check">enabled <input type="checkbox" data-action="toggleModule" data-module="${esc(m.name)}" ${m.enabled ? "checked" : ""} /></label>`
            : `<span class="hint">runtime unavailable</span>`}
        </div>
      </div>`
    )
    .join("");
}

async function toggleModule(name, enabled) {
  await apiFetch("/api/v1/tools/modules", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name, enabled }),
  });
  loadModules(); // re-sync with server state
}

// ---------- Stats ----------
let lastStats = null;

async function fetchStats() {
  try {
    const res = await apiFetch("/api/v1/stats");
    if (!res.ok) return null;
    return await res.json();
  } catch (e) {
    return null;
  }
}

async function loadStats() {
  const d = await fetchStats();
  if (!d) return;
  lastStats = d;
  renderStats(d);
  renderLiveStats(d);
}

function renderStats(d) {
  const t = d.totals || {};
  $("#stat-totals").innerHTML = `
    <div class="stat-card"><div class="stat-num">${fmtCost(t.cost)}</div><div class="stat-label">spend (USD)</div></div>
    <div class="stat-card"><div class="stat-num">${fmt(t.total_tokens)}</div><div class="stat-label">total tokens</div></div>
    <div class="stat-card"><div class="stat-num">${fmt(t.prompt_tokens)}</div><div class="stat-label">prompt (input)</div></div>
    <div class="stat-card"><div class="stat-num">${fmt(t.completion_tokens)}</div><div class="stat-label">completion (output)</div></div>
    <div class="stat-card"><div class="stat-num">${fmt(t.thought_tokens)}</div><div class="stat-label">thinking</div></div>
    <div class="stat-card"><div class="stat-num">${fmt(t.calls)}</div><div class="stat-label">LLM calls</div></div>
    <div class="stat-card"><div class="stat-num">${fmt(t.runs)}</div><div class="stat-label">runs</div></div>`;

  $("#stat-by-model").innerHTML = renderGroup(d.by_model);
  $("#stat-by-agent").innerHTML = renderGroup(d.by_agent);
  $("#stat-by-run").innerHTML =
    (d.by_run || []).length
      ? (d.by_run || []).map(
          (r) => `
    <div class="card">
      <div class="meta">
        <div class="name">${esc(r.run_id)}</div>
        <div class="sub">${esc(r.task)}</div>
      </div>
      <div class="actions"><span class="hint">${fmt(r.total_tokens)} tokens / ${fmt(r.calls)} calls / ${fmtCost(r.cost)}</span></div>
    </div>`
        ).join("")
      : '<p class="hint">No runs yet.</p>';

  renderTimelineSelector(d.by_run);
}

// Live stats panel in the Swarm sidebar: a magpie-style, always-updating view
// of total tokens, per-model / per-agent bars, and a real-time activity feed.
function renderLiveStats(d) {
  const t = d.totals || {};
  $("#live-total").textContent = fmt(t.total_tokens);
  $("#live-split").textContent =
    "in " + fmt(t.prompt_tokens) + " · out " + fmt(t.completion_tokens) + " · think " + fmt(t.thought_tokens);
  $("#live-cost").textContent = fmtCost(t.cost);
  $("#live-by-model").innerHTML = renderBars(d.by_model);
  $("#live-by-agent").innerHTML = renderBars(d.by_agent);
  $("#live-cost-model").innerHTML = renderCostBars(d.by_model);
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

function renderCostBars(rows) {
  if (!rows || !rows.length) return '<div class="hint">no data</div>';
  const max = Math.max.apply(null, rows.map((r) => r.cost || 0)) || 1;
  return rows
    .map(
      (r) => `
    <div class="bar-row">
      <span class="bar-label" title="${esc(r.key)}">${esc(r.key)}</span>
      <div class="bar-track"><div class="bar-fill" style="width:${((r.cost / max) * 100).toFixed(1)}%"></div></div>
      <span class="bar-val">${fmtCost(r.cost)}</span>
    </div>`
    )
    .join("");
}

// liveFeed appends tool activity (reads/writes) and token usage from the
// running swarm's SSE stream. Orchestration/communication goes to swarmComm().
function liveFeed(e) {
  let label = "";
  switch (e.type) {
    case "usage":
      label = (e.agent || "") + " +" + fmt(e.usage?.total_tokens || 0) + " tok · " + fmtCost(e.cost) + "  (" + (e.model || "") + ")";
      break;
    case "model":
      label = (e.agent || "") + " generating…";
      break;
    case "tool":
      label = (e.agent || "") + " " + (e.direction === "write" ? "WRITE" : e.direction === "read" ? "READ" : "·") + " " + (e.tool || "") + (e.detail ? " " + e.detail : "");
      break;
    default:
      return;
  }
  const el = $("#live-feed");
  if (!el) return;
  const line = document.createElement("div");
  line.className = "feed-line";
  if (e.type === "tool") line.classList.add(e.direction === "write" ? "feed-write" : "feed-read");
  line.textContent = label;
  el.prepend(line);
  while (el.children.length > 40) el.removeChild(el.lastChild);
}

function clearLiveFeed() {
  const el = $("#live-feed");
  if (el) el.innerHTML = "";
  const chat = $("#swarm-chat");
  if (chat) chat.innerHTML = "";
  swarmNodes.clear();
  renderSwarmGraph();
}

// ---------- Swarm graph + agents-talking transcript ----------
function swarmShort(name) {
  const words = String(name || "?").trim().split(/\s+/).filter(Boolean);
  if (!words.length) return "?";
  const key = words.length > 1 ? words[words.length - 1] : words[0];
  return key.slice(0, 3).toUpperCase();
}

function swarmNode(svg, name, x, y) {
  const g = document.createElementNS(SVGNS, "g");
  const title = document.createElementNS(SVGNS, "title");
  title.textContent = name;
  g.appendChild(title);
  const c = document.createElementNS(SVGNS, "circle");
  c.setAttribute("cx", x);
  c.setAttribute("cy", y);
  c.setAttribute("r", 22);
  c.setAttribute("class", "swarm-node");
  g.appendChild(c);
  const t = document.createElementNS(SVGNS, "text");
  t.setAttribute("x", x);
  t.setAttribute("y", y);
  t.setAttribute("class", "swarm-label");
  t.textContent = swarmShort(name);
  g.appendChild(t);
  svg.appendChild(g);
}

function renderSwarmGraph() {
  const el = $("#swarm-graph");
  if (!el) return;
  el.innerHTML = "";
  const svg = document.createElementNS(SVGNS, "svg");
  svg.setAttribute("viewBox", "0 0 248 248");
  svg.setAttribute("class", "swarm-svg");
  svg.setAttribute("role", "img");
  svg.setAttribute("aria-label", "swarm agents");
  const names = [...swarmNodes.keys()];
  names.forEach((n, i) => {
    const ang = (i / Math.max(1, names.length)) * Math.PI * 2 - Math.PI / 2;
    const x = swarmCenter.x + Math.cos(ang) * swarmRadius;
    const y = swarmCenter.y + Math.sin(ang) * swarmRadius;
    swarmNodes.set(n, { x, y });
    swarmNode(svg, n, x, y);
  });
  el.appendChild(svg);
}

function swarmPos(name) {
  return swarmNodes.get(name) || null;
}

// Draws a signal line between two points, fading out after a second.
function swarmLine(x1, y1, x2, y2) {
  const svg = $("#swarm-graph svg");
  if (!svg) return;
  const line = document.createElementNS(SVGNS, "line");
  line.setAttribute("x1", x1);
  line.setAttribute("y1", y1);
  line.setAttribute("x2", x2);
  line.setAttribute("y2", y2);
  line.setAttribute("class", "swarm-signal");
  svg.appendChild(line);
  setTimeout(() => line.remove(), 1000);
}

// The planner hands work out from the centre of the ring.
function swarmDispatch(name) {
  const p = swarmPos(name);
  if (!p) return;
  swarmLine(swarmCenter.x, swarmCenter.y, p.x, p.y);
}

// A finished agent shares its result with every other agent (the shared log).
function swarmBroadcast(name) {
  const p = swarmPos(name);
  if (!p) return;
  swarmNodes.forEach((q, other) => {
    if (other !== name) swarmLine(p.x, p.y, q.x, q.y);
  });
}

function swarmPulse(name) {
  const svg = $("#swarm-graph svg");
  if (!svg) return;
  const p = swarmPos(name);
  if (!p) return;
  const c = document.createElementNS(SVGNS, "circle");
  c.setAttribute("cx", p.x);
  c.setAttribute("cy", p.y);
  c.setAttribute("r", 22);
  c.setAttribute("class", "swarm-pulse");
  svg.appendChild(c);
  setTimeout(() => c.remove(), 800);
}

// swarmComm drives the circle graph and the "agents talking" transcript. It runs
// alongside liveFeed() on every SSE event from a running run. The transcript is
// chronological (oldest first) so it reads like a conversation, auto-scrolled to
// the newest line.
function swarmComm(e) {
  const name = e.agent || "";
  if (name && !swarmNodes.has(name)) {
    swarmNodes.set(name, { x: swarmCenter.x, y: swarmCenter.y });
    renderSwarmGraph();
  }

  switch (e.type) {
    case "subtask":
      if (name) swarmDispatch(name); // planner hands out work
      break;
    case "subtask_done":
      if (name) swarmBroadcast(name); // agent shares its result with everyone
      break;
    case "tool":
      if (name) swarmPulse(name); // agent is actively working
      break;
  }

  let label = null;
  let cls = "comm-sys";
  switch (e.type) {
    case "planning":
      label = e.detail || "orchestrator is planning…";
      break;
    case "plan":
      label = "orchestrator → " + (e.detail || "plan ready");
      cls = "comm-orch";
      break;
    case "direct":
      label = (e.agent || "orchestrator") + " → " + (e.detail || "");
      cls = "comm-orch";
      break;
    case "subtask": {
      const step = e.step != null ? " [step " + e.step + "]" : "";
      label = "→ " + name + step + ": " + (e.detail || "");
      cls = "comm-task";
      break;
    }
    case "subtask_done": {
      const step = e.step != null ? " [step " + e.step + "]" : "";
      label = name + step + " done: " + (e.detail || "ok");
      cls = "comm-done";
      break;
    }
    case "warning":
      label = "warning: " + (e.detail || "");
      break;
    case "done":
      label = "run complete";
      break;
    case "error":
      label = "error: " + (e.error || e.detail || "");
      break;
    case "stopped":
      label = "stopped";
      break;
    default:
      return;
  }
  const el = $("#swarm-chat");
  if (!el) return;
  const line = document.createElement("div");
  line.className = "comm-line " + cls;
  line.textContent = label;
  el.appendChild(line);
  while (el.children.length > 300) el.removeChild(el.firstChild);
  el.scrollTop = el.scrollHeight;
}

function renderGroup(rows) {
  if (!rows || !rows.length) return '<p class="hint">No data yet.</p>';
  return rows
    .map(
      (g) => `
    <div class="card">
      <div class="meta">
        <div class="name">${esc(g.key)}</div>
        <div class="sub">${fmt(g.total_tokens)} tokens (${fmt(g.prompt_tokens)} in / ${fmt(g.completion_tokens)} out) / ${fmt(g.calls)} calls / ${fmtCost(g.cost)}</div>
      </div>
    </div>`
    )
    .join("");
}

// ---------- Timeline (parallel distribution) ----------
let timelineRunId = null;

function renderTimelineSelector(runs) {
  const sel = $("#timeline-run");
  if (!sel) return;
  const current = timelineRunId || (runs && runs.length ? runs[0].run_id : null);
  sel.innerHTML = (runs || [])
    .map((r) => `<option value="${esc(r.run_id)}">${esc(r.run_id)} — ${esc((r.task || "").slice(0, 60))}</option>`)
    .join("");
  if (current) sel.value = current;
  if (sel.value) {
    timelineRunId = sel.value;
    loadTimeline(sel.value);
  } else {
    $("#timeline").innerHTML = '<p class="hint">No runs yet.</p>';
  }
}

async function loadTimeline(runId) {
  const el = $("#timeline");
  el.innerHTML = '<p class="hint">Loading…</p>';
  try {
    const res = await apiFetch("/api/v1/runs/" + encodeURIComponent(runId) + "/timeline");
    if (!res.ok) throw new Error(res.status);
    const data = await res.json();
    renderTimeline(data.timeline || []);
  } catch (e) {
    el.innerHTML = '<p class="hint">Could not load timeline.</p>';
  }
}

function renderTimeline(bars) {
  const el = $("#timeline");
  if (!bars || !bars.length) {
    el.innerHTML = '<p class="hint">No subtasks recorded for this run yet.</p>';
    return;
  }
  const times = bars
    .map((b) => new Date(b.started_at).getTime())
    .concat(bars.filter((b) => b.ended_at).map((b) => new Date(b.ended_at).getTime()));
  const t0 = Math.min.apply(null, times);
  const t1 = Math.max.apply(null, times);
  const span = Math.max(1, t1 - t0);
  const agents = [...new Set(bars.map((b) => b.agent))];
  const rows = agents
    .map((a) => {
      const ab = bars.filter((b) => b.agent === a);
      const barsHtml = ab
        .map((b) => {
          const s = new Date(b.started_at).getTime();
          const e = b.ended_at ? new Date(b.ended_at).getTime() : t1;
          const left = (((s - t0) / span) * 100).toFixed(2);
          const width = Math.max(2, ((e - s) / span) * 100).toFixed(2);
          const label = "step " + (b.step + 1);
          const title = label + " · " + (b.summary || "running…");
          return `<div class="tl-bar" style="left:${left}%;width:${width}%" title="${esc(title)}">${esc(label)}</div>`;
        })
        .join("");
      return `<div class="tl-row"><div class="tl-agent">${esc(a)}</div><div class="tl-track">${barsHtml}</div></div>`;
    })
    .join("");
  el.innerHTML = rows;
}

// ---------- Prompt generator ----------
function openPromptGen() {
  // Pre-fill with any draft already in the task box so it can be refined.
  $("#prompt-idea").value = $("#task").value.trim();
  $("#prompt-result").value = "";
  $("#prompt-status").textContent = "";
  $("#prompt-modal").classList.remove("hidden");
  setTimeout(() => $("#prompt-idea").focus(), 0);
}

function closePromptGen() {
  $("#prompt-modal").classList.add("hidden");
}

async function generatePrompt() {
  const idea = $("#prompt-idea").value.trim();
  if (!idea) {
    $("#prompt-status").textContent = "Type an idea first.";
    return;
  }
  const btn = $("#btn-prompt-generate");
  btn.disabled = true;
  $("#prompt-status").textContent = "Generating…";
  $("#prompt-result").value = "";
  try {
    const res = await apiFetch("/api/v1/prompt/generate", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ idea }),
    });
    const data = await res.json();
    if (!res.ok) {
      $("#prompt-status").textContent = "Error: " + (data.error || res.status);
      return;
    }
    $("#prompt-result").value = data.prompt || "";
    $("#prompt-status").textContent = "Done.";
  } catch (e) {
    $("#prompt-status").textContent = "Error: " + e.message;
  } finally {
    btn.disabled = false;
  }
}

async function copyPrompt() {
  const text = $("#prompt-result").value;
  if (!text) return;
  try {
    await navigator.clipboard.writeText(text);
  } catch (e) {
    // Fall back for contexts where the async Clipboard API is unavailable.
    $("#prompt-result").select();
    document.execCommand("copy");
  }
  $("#prompt-status").textContent = "Copied.";
}

function usePrompt() {
  const text = $("#prompt-result").value.trim();
  if (text) $("#task").value = text;
  closePromptGen();
  $("#task").focus();
}

// ---------- Modal ----------
let modalConfirmHandler = null;

function showModal(title, msg, onConfirm) {
  $("#modal-title").textContent = title;
  $("#modal-msg").textContent = msg;
  modalConfirmHandler = onConfirm;
  $("#modal").classList.remove("hidden");
}

function hideModal() {
  $("#modal").classList.add("hidden");
  modalConfirmHandler = null;
}

// ---------- Init ----------
$("#btn-new-agent").addEventListener("click", openNewAgent);
$("#btn-save-agent").addEventListener("click", saveAgent);
$("#btn-cancel-agent").addEventListener("click", () => $("#agent-form").classList.add("hidden"));
$("#f-provider").addEventListener("change", () => {
  const openai = $("#f-provider").value === "openai";
  const cur = $("#f-baseurl").value;
  if (openai && (!cur || cur.includes("generativelanguage"))) $("#f-baseurl").value = "https://api.deepseek.com";
  if (!openai && (!cur || cur.includes("deepseek"))) $("#f-baseurl").value = "https://generativelanguage.googleapis.com/v1beta";
  $("#f-model").placeholder = openai ? "deepseek-chat" : "auto";
});
$("#btn-run").addEventListener("click", sendMessage);
$("#btn-stop").addEventListener("click", stopRun);
$("#btn-new-chat").addEventListener("click", newChat);
$("#btn-mode-single").addEventListener("click", () => setMode("single"));
$("#btn-mode-swarm").addEventListener("click", () => setMode("swarm"));
$("#btn-mode-polish").addEventListener("click", () => setMode("polish"));
$("#timeline-run").addEventListener("change", () => {
  timelineRunId = $("#timeline-run").value;
  if (timelineRunId) loadTimeline(timelineRunId);
});
$("#modal-cancel").addEventListener("click", hideModal);
$("#modal-confirm").addEventListener("click", () => {
  if (modalConfirmHandler) modalConfirmHandler();
});
$("#modal").addEventListener("click", (e) => {
  if (e.target === $("#modal")) hideModal();
});
$("#btn-prompt-gen").addEventListener("click", openPromptGen);
$("#btn-prompt-generate").addEventListener("click", generatePrompt);
$("#btn-prompt-copy").addEventListener("click", copyPrompt);
$("#btn-prompt-use").addEventListener("click", usePrompt);
$("#btn-prompt-close").addEventListener("click", closePromptGen);
$("#prompt-modal").addEventListener("click", (e) => {
  if (e.target === $("#prompt-modal")) closePromptGen();
});
$("#prompt-idea").addEventListener("keydown", (e) => {
  if ((e.ctrlKey || e.metaKey) && e.key === "Enter") generatePrompt();
});

loadAgents();
loadModels();
loadStats();
loadChats();

// Poll stats every 2s so the live usage panel updates in parallel with any run.
setInterval(async () => {
  const d = await fetchStats();
  if (!d) return;
  const changed = !lastStats || d.totals.total_tokens !== lastStats.totals.total_tokens;
  lastStats = d;
  if (changed) renderLiveStats(d);
}, 2000);
