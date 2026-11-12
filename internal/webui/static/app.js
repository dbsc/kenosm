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

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c])
  );
}

function fmt(n) {
  return Number(n || 0).toLocaleString("en-US");
}

// ---------- Agents ----------
let agents = [];
let editingId = null;

async function loadAgents() {
  const res = await fetch("/api/v1/services");
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
        <button class="btn small ghost" onclick="editAgent('${a.id}')">Edit</button>
        <button class="btn small ghost" onclick="toggleAgent('${a.id}')">${a.enabled ? "Disable" : "Enable"}</button>
        <button class="btn small ghost" onclick="deleteAgent('${a.id}')">Delete</button>
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
  const res = await fetch(url, {
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
  await fetch("/api/v1/services/" + id, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ enabled: !a.enabled }),
  });
  loadAgents();
}

async function deleteAgent(id) {
  if (!confirm("Delete this agent?")) return;
  await fetch("/api/v1/services/" + id, { method: "DELETE" });
  loadAgents();
}

// ---------- Models ----------
async function loadModels() {
  try {
    const res = await fetch("/api/v1/models");
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
        <button class="chip-x" onclick="removeAttachment(${i})">x</button>
      </span>`
    )
    .join("");
}

// ---------- Swarm console ----------
let currentRunId = null;
let currentES = null;

function setRunning(running) {
  $("#btn-run").disabled = running;
  $("#btn-stop").classList.toggle("hidden", !running);
  if (!running) currentRunId = null;
}

async function runSwarm() {
  const task = $("#task").value.trim();
  if (!task) return;
  setRunning(true);
  $("#run-status").textContent = "Starting...";
  clearConsole();
  clearLiveFeed();

  try {
    const res = await fetch("/api/v1/swarm/run", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        task,
        images: attached.map((a) => ({ mime_type: a.mime_type, data: a.data })),
      }),
    });
    const data = await res.json();
    if (!res.ok) {
      $("#run-status").textContent = "Error: " + (data.error || res.status);
      setRunning(false);
      return;
    }
    currentRunId = data.run_id;
    await streamRun(data.run_id);
  } catch (e) {
    $("#run-status").textContent = "Error: " + e.message;
  } finally {
    setRunning(false);
  }
}

async function stopRun() {
  if (!currentRunId) return;
  $("#run-status").textContent = "Stopping...";
  try {
    await fetch("/api/v1/swarm/run/" + currentRunId + "/stop", { method: "POST" });
  } catch (e) {}
}

function clearConsole() {
  $("#console-scroll").innerHTML = "";
}

function streamRun(runId) {
  return new Promise((resolve) => {
    const es = new EventSource("/api/v1/swarm/run/" + runId + "/events");
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
      renderEvent(e);
      liveFeed(e);
      if (e.type === "done") {
        $("#run-status").textContent = "Done.";
        finish();
      } else if (e.type === "error") {
        $("#run-status").textContent = "Error: " + (e.error || "unknown");
        finish();
      } else if (e.type === "stopped") {
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

function consoleMsg(cls, html) {
  const c = $("#console-scroll");
  const div = document.createElement("div");
  div.className = "msg " + cls;
  div.innerHTML = html;
  c.appendChild(div);
  c.scrollTop = c.scrollHeight;
  return div;
}

function renderEvent(e) {
  switch (e.type) {
    case "start":
      consoleMsg("system", "run started");
      break;
    case "planning":
      consoleMsg("thinking", `<span class="who">orchestrator</span> planning…`);
      break;
    case "plan":
      consoleMsg("info", `<span class="who">plan</span> ${esc(e.detail || "")}`);
      break;
    case "subtask":
      consoleMsg(
        "subtask",
        `<span class="who">${esc(e.agent || "")}</span><span class="model">${esc(e.model || "")}</span> ${esc(e.detail || "")}`
      );
      break;
    case "tool":
      consoleMsg("tool", `<span class="toolmark">&gt;</span> ${esc(e.detail || "")}`);
      break;
    case "subtask_done":
      consoleMsg("done-sub", `${esc(e.detail || "")}`);
      break;
    case "usage":
    case "model":
      // Token spend and generation activity are shown in the live sidebar,
      // not the console transcript.
      break;
    case "done":
      consoleMsg("done", "run complete");
      if (e.result) renderResult(e.result);
      break;
    case "stopped":
      consoleMsg("error", "run stopped by user");
      break;
    case "error":
      consoleMsg("error", "error: " + esc(e.error || e.detail || ""));
      break;
    default:
      consoleMsg("", esc(e.type) + ": " + esc(e.detail || e.error || ""));
  }
}

function renderResult(d) {
  const c = $("#console-scroll");
  const box = document.createElement("div");
  box.className = "msg result";

  const plan = (d.plan || []).map((p) => `<li>${esc(p)}</li>`).join("");
  const subtasks = (d.subtasks || [])
    .map(
      (s) => `
      <div class="subtask">
        <div class="head"><b>${esc(s.agent)}</b><span>${esc(s.model)}</span><span>${(s.tool_calls || []).join(", ")}</span>${s.error ? "<span>error: " + esc(s.error) + "</span>" : ""}</div>
        <div class="summary">${esc(s.summary)}</div>
      </div>`
    )
    .join("");
  const files = (d.files || [])
    .map(
      (f) => `<div class="file-row"><span>${esc(f.path)}</span><span class="hint">${f.size} bytes</span><button class="btn small ghost" onclick="viewFile('${esc(f.path)}')">View</button></div>`
    )
    .join("");

  box.innerHTML = `
    <div class="result-head">Result</div>
    ${plan ? `<div class="result-sec"><b>Plan</b><ol>${plan}</ol></div>` : ""}
    ${subtasks ? `<div class="result-sec"><b>Subtasks</b>${subtasks}</div>` : ""}
    ${files ? `<div class="result-sec"><b>Workspace files</b>${files}</div>` : ""}
    ${d.final ? `<div class="result-sec"><b>Summary</b><pre>${esc(d.final)}</pre></div>` : ""}
  `;
  c.appendChild(box);
  c.scrollTop = c.scrollHeight;
}

// ---------- Files ----------
async function loadFiles() {
  const res = await fetch("/api/v1/workspace/files");
  const data = await res.json();
  const files = data.files || [];
  const el = $("#files-list");
  if (!files.length) {
    el.innerHTML = '<p class="hint">No files yet. Run a swarm task.</p>';
    return;
  }
  el.innerHTML = files
    .map(
      (f) => `<div class="card"><div class="meta name">${esc(f.path)}</div><div class="actions"><span class="hint">${f.size} bytes</span><button class="btn small ghost" onclick="viewFile('${esc(f.path)}')">View</button></div></div>`
    )
    .join("");
}

async function viewFile(path) {
  const res = await fetch("/api/v1/workspace/files/" + encodeURIComponent(path));
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
    const res = await fetch("/api/v1/stats");
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
      <div class="actions"><span class="hint">${fmt(r.total_tokens)} tokens / ${fmt(r.calls)} calls</span></div>
    </div>`
        ).join("")
      : '<p class="hint">No runs yet.</p>';
}

// Live stats panel in the Swarm sidebar: a magpie-style, always-updating view
// of total tokens, per-model / per-agent bars, and a real-time activity feed.
function renderLiveStats(d) {
  const t = d.totals || {};
  $("#live-total").textContent = fmt(t.total_tokens);
  $("#live-split").textContent =
    "in " + fmt(t.prompt_tokens) + " · out " + fmt(t.completion_tokens) + " · think " + fmt(t.thought_tokens);
  $("#live-by-model").innerHTML = renderBars(d.by_model);
  $("#live-by-agent").innerHTML = renderBars(d.by_agent);
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

// liveFeed appends real-time activity from the running swarm's SSE stream.
function liveFeed(e) {
  let label = "";
  switch (e.type) {
    case "usage":
      label = (e.agent || "") + " +" + fmt(e.usage?.total_tokens || 0) + " tok  (" + (e.model || "") + ")";
      break;
    case "model":
      label = (e.agent || "") + " generating…";
      break;
    case "tool":
      label = (e.agent || "") + " > " + (e.detail || "");
      break;
    case "subtask":
      label = (e.agent || "") + " → " + (e.detail || "");
      break;
    case "subtask_done":
      label = (e.agent || "") + " ✓ " + ((e.detail || "").slice(0, 48) || "done");
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
        <div class="sub">${fmt(g.total_tokens)} tokens (${fmt(g.prompt_tokens)} in / ${fmt(g.completion_tokens)} out) / ${fmt(g.calls)} calls</div>
      </div>
    </div>`
    )
    .join("");
}

// ---------- Modal + clear context ----------
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

function clearContext() {
  showModal(
    "Clear model context?",
    "Resets the swarm session: clears the task, attachments and log so the next run starts with a fresh conversation. Workspace files are left untouched.",
    () => {
      hideModal();
      attached = [];
      renderAttachments();
      $("#task").value = "";
      clearConsole();
      clearLiveFeed();
      $("#run-status").textContent = "Context cleared.";
    }
  );
}

// ---------- Init ----------
$("#btn-new-agent").addEventListener("click", openNewAgent);
$("#btn-save-agent").addEventListener("click", saveAgent);
$("#btn-cancel-agent").addEventListener("click", () => $("#agent-form").classList.add("hidden"));
$("#btn-run").addEventListener("click", runSwarm);
$("#btn-stop").addEventListener("click", stopRun);
$("#btn-clear-console").addEventListener("click", clearConsole);
$("#btn-clear-context").addEventListener("click", clearContext);
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

// Poll stats every 2s so the live usage panel updates in parallel with any run.
setInterval(async () => {
  const d = await fetchStats();
  if (!d) return;
  const changed = !lastStats || d.totals.total_tokens !== lastStats.totals.total_tokens;
  lastStats = d;
  if (changed) renderLiveStats(d);
}, 2000);
