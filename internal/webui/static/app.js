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
  $("#f-model").value = a.model || "";
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
  const body = {
    name,
    slug,
    upstream_url: $("#f-baseurl").value.trim(),
    auth_header: "x-goog-api-key",
    model: $("#f-model").value.trim(),
    proxy_host: $("#f-phost").value.trim(),
    proxy_port: parseInt($("#f-pport").value, 10) || 0,
    proxy_username: $("#f-puser").value.trim(),
    enabled: $("#f-enabled").checked,
  };
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

function setRunning(running) {
  $("#btn-run").disabled = running;
  $("#btn-stop").classList.toggle("hidden", !running);
  if (!running) currentRunId = null;
}

function setMode(mode) {
  currentMode = mode;
  $("#btn-mode-single").classList.toggle("active", mode === "single");
  $("#btn-mode-swarm").classList.toggle("active", mode === "swarm");
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

// liveFeed appends real-time activity from the running swarm's SSE stream.
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
    case "subtask":
      label = (e.agent || "") + " → " + (e.detail || "");
      break;
    case "subtask_done":
      label = (e.agent || "") + " ✓ " + ((e.detail || "").slice(0, 48) || "done");
      break;
    case "direct":
      label = (e.agent || "") + " → " + (e.detail || "");
      break;
    case "warning":
      label = "! " + (e.detail || "");
      break;
    case "done":
      label = "run complete";
      break;
    case "error":
      label = "error: " + (e.error || e.detail || "");
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
$("#btn-run").addEventListener("click", sendMessage);
$("#btn-stop").addEventListener("click", stopRun);
$("#btn-new-chat").addEventListener("click", newChat);
$("#btn-mode-single").addEventListener("click", () => setMode("single"));
$("#btn-mode-swarm").addEventListener("click", () => setMode("swarm"));
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
