"use strict";

const $ = (sel) => document.querySelector(sel);
const KEY = "stor_api_key";

let apiKey = localStorage.getItem(KEY) || "";
let pollTimer = null;

// --- Auth ---

function showAuth() {
  $("#auth-screen").classList.add("active");
  $("#app-screen").classList.remove("active");
}

function showApp() {
  $("#auth-screen").classList.remove("active");
  $("#app-screen").classList.add("active");
  startPolling();
}

$("#key-submit").addEventListener("click", async () => {
  const key = $("#key-input").value.trim();
  if (!key) return;
  apiKey = key;
  $("#auth-error").hidden = true;
  try {
    await rpc("daemon.version");
    localStorage.setItem(KEY, apiKey);
    showApp();
  } catch (e) {
    $("#auth-error").textContent = "Failed: " + e.message;
    $("#auth-error").hidden = false;
    apiKey = "";
  }
});

$("#key-input").addEventListener("keydown", (e) => {
  if (e.key === "Enter") $("#key-submit").click();
});

// --- RPC ---

async function rpc(method, params) {
  const key = apiKey.trim();
  const res = await fetch("/api/rpc", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: "Bearer " + key,
    },
    body: JSON.stringify({ jsonrpc: "2.0", method, params, id: 1 }),
  });
  if (res.status === 401) {
    const body = await res.text();
    localStorage.removeItem(KEY);
    apiKey = "";
    showAuth();
    throw new Error(body || "unauthorized");
  }
  const data = await res.json();
  if (data.error) throw new Error(data.error.message);
  return data.result;
}

// --- Add torrent ---

$("#add-btn").addEventListener("click", async () => {
  const source = $("#add-input").value.trim();
  if (!source) return;
  try {
    await rpc("torrent.add", { source });
    $("#add-input").value = "";
    toast("Torrent added");
    refresh();
  } catch (e) {
    toast("Add failed: " + e.message, true);
  }
});

$("#add-input").addEventListener("keydown", (e) => {
  if (e.key === "Enter") $("#add-btn").click();
});

$("#add-file").addEventListener("change", async (e) => {
  const files = Array.from(e.target.files);
  if (!files.length) return;
  let ok = 0, fail = 0;
  await Promise.all(files.map(async (file) => {
    try {
      const buf = await file.arrayBuffer();
      const bytes = new Uint8Array(buf);
      // btoa in chunks to avoid call stack overflow on large files
      let b64 = "";
      for (let i = 0; i < bytes.length; i += 8192) {
        b64 += String.fromCharCode(...bytes.subarray(i, i + 8192));
      }
      await rpc("torrent.addFile", { data: btoa(b64) });
      ok++;
    } catch {
      fail++;
    }
  }));
  const msg = `${ok} added` + (fail ? `, ${fail} failed` : "");
  toast(msg, fail > 0);
  refresh();
  e.target.value = "";
});

// --- Drag & Drop ---

document.addEventListener("dragover", (e) => { e.preventDefault(); });
document.addEventListener("drop", async (e) => {
  e.preventDefault();
  const files = Array.from(e.dataTransfer.files).filter((f) => f.name.endsWith(".torrent"));
  if (!files.length) return;
  let ok = 0, fail = 0;
  await Promise.all(files.map(async (file) => {
    try {
      const buf = await file.arrayBuffer();
      const bytes = new Uint8Array(buf);
      let b64 = "";
      for (let i = 0; i < bytes.length; i += 8192) {
        b64 += String.fromCharCode(...bytes.subarray(i, i + 8192));
      }
      await rpc("torrent.addFile", { data: btoa(b64) });
      ok++;
    } catch {
      fail++;
    }
  }));
  const msg = `${ok} added` + (fail ? `, ${fail} failed` : "");
  toast(msg, fail > 0);
  refresh();
});

// --- Torrent list ---

function formatBytes(b) {
  if (b >= 1 << 30) return (b / (1 << 30)).toFixed(2) + " GB";
  if (b >= 1 << 20) return (b / (1 << 20)).toFixed(1) + " MB";
  if (b >= 1 << 10) return (b / (1 << 10)).toFixed(0) + " KB";
  return b + " B";
}

function formatETA(downloaded, total, speed) {
  if (!speed || speed <= 0 || downloaded >= total) return "-";
  const secs = Math.round((total - downloaded) / speed);
  if (secs < 60) return secs + "s";
  if (secs < 3600) return Math.floor(secs / 60) + "m " + (secs % 60) + "s";
  const h = Math.floor(secs / 3600);
  const m = Math.floor((secs % 3600) / 60);
  return h + "h " + m + "m";
}

function renderTorrents(torrents) {
  const tbody = $("#torrent-list");
  const empty = $("#empty-msg");

  if (!torrents || torrents.length === 0) {
    tbody.innerHTML = "";
    empty.hidden = false;
    return;
  }
  empty.hidden = true;

  // Sort: active first (by queue position), then queued, then complete
  torrents.sort((a, b) => {
    const stateOrder = { downloading: 0, metadata: 0, adding: 1, paused: 2, error: 3, complete: 4 };
    const sa = stateOrder[a.state] ?? 5;
    const sb = stateOrder[b.state] ?? 5;
    if (sa !== sb) return sa - sb;
    return (a.queue_position ?? 999) - (b.queue_position ?? 999);
  });

  tbody.innerHTML = torrents.map((t) => {
    const p = t.progress;
    const pct = p.percent ? p.percent.toFixed(1) : "0.0";
    const fillClass = t.state === "complete" ? "fill complete" : "fill";
    const speed = t.state === "downloading" && p.down_speed ? formatBytes(p.down_speed) + "/s" : "-";
    const eta = t.state === "downloading" ? formatETA(p.downloaded, p.total, p.down_speed) : "-";
    const peers = t.state === "downloading" && p.active_peers ? p.active_peers : "-";
    const size = t.total_bytes ? formatBytes(t.total_bytes) : "-";
    const name = t.name || t.id.slice(0, 12) + "...";
    const qpos = t.queue_position != null ? t.queue_position : "-";

    return `<tr>
      <td title="${t.id}">
        <div class="name">${esc(name)}</div>
        ${t.error ? `<div class="error-detail">${esc(t.error)}</div>` : ""}
      </td>
      <td>${size}</td>
      <td>
        <div class="progress-bar"><div class="${fillClass}" style="width:${pct}%"></div></div>
        <div class="progress-text">${pct}%${p.done_pieces ? ` (${p.done_pieces}/${p.total_pieces})` : ""}</div>
      </td>
      <td>${speed}</td>
      <td>${eta}</td>
      <td>${peers}</td>
      <td><span class="state state-${t.state}">${t.state}</span></td>
      <td class="queue-col">
        <span class="qpos">#${qpos}</span>
        <span class="queue-btns">
          <button title="Top" onclick="queueMove('${t.id}','top')">&#x23EB;</button>
          <button title="Up" onclick="queueMove('${t.id}','up')">&#x25B2;</button>
          <button title="Down" onclick="queueMove('${t.id}','down')">&#x25BC;</button>
          <button title="Bottom" onclick="queueMove('${t.id}','bottom')">&#x23EC;</button>
        </span>
      </td>
      <td class="actions">
        ${t.state === "downloading" || t.state === "metadata" ? `<button onclick="pause('${t.id}')">Pause</button>` : ""}
        ${t.state === "paused" || t.state === "error" ? `<button onclick="resume('${t.id}')">Resume</button>` : ""}
        <button class="danger" onclick="remove('${t.id}')">Remove</button>
      </td>
    </tr>`;
  }).join("");
}

function esc(s) {
  const d = document.createElement("div");
  d.textContent = s;
  return d.innerHTML;
}

function renderStats(stats) {
  if (!stats) return;
  const speed = stats.total_down_speed ? formatBytes(stats.total_down_speed) + "/s" : "0 B/s";
  $("#stats-text").textContent = `${stats.active_torrents}/${stats.max_active} active | ${stats.total_torrents} total | ${speed}`;
  const input = $("#max-active-input");
  if (document.activeElement !== input) {
    input.value = stats.max_active;
  }
}

// --- Actions ---

async function pause(id) {
  try { await rpc("torrent.pause", { id }); refresh(); } catch (e) { toast(e.message, true); }
}

async function resume(id) {
  try { await rpc("torrent.resume", { id }); refresh(); } catch (e) { toast(e.message, true); }
}

async function remove(id) {
  if (!confirm("Remove this torrent?")) return;
  const deleteFiles = confirm("Also delete downloaded files?");
  try {
    await rpc("torrent.remove", { id, delete_files: deleteFiles });
    toast("Removed");
    refresh();
  } catch (e) { toast(e.message, true); }
}

async function queueMove(id, direction) {
  try { await rpc("torrent.queue" + direction.charAt(0).toUpperCase() + direction.slice(1), { id }); refresh(); } catch (e) { toast(e.message, true); }
}

async function setMaxActive() {
  const val = parseInt($("#max-active-input").value);
  if (!val || val < 1) return;
  try {
    await rpc("daemon.setMaxActive", { max_active: val });
    toast("Max active set to " + val);
  } catch (e) { toast(e.message, true); }
}

// Expose to inline handlers
window.pause = pause;
window.resume = resume;
window.remove = remove;
window.queueMove = queueMove;
window.setMaxActive = setMaxActive;

// --- Toast ---

function toast(msg, isError) {
  const el = $("#toast");
  el.textContent = msg;
  el.className = "toast show" + (isError ? " error" : "");
  setTimeout(() => { el.className = "toast"; }, 3000);
}

// --- Settings ---

$("#max-active-input").addEventListener("change", setMaxActive);
$("#max-active-input").addEventListener("keydown", (e) => {
  if (e.key === "Enter") setMaxActive();
});

// --- Polling ---

async function refresh() {
  try {
    const [torrents, stats] = await Promise.all([
      rpc("torrent.list"),
      rpc("daemon.stats"),
    ]);
    renderTorrents(torrents);
    renderStats(stats);
  } catch {
    // ignore polling errors
  }
}

function startPolling() {
  refresh();
  if (pollTimer) clearInterval(pollTimer);
  pollTimer = setInterval(refresh, 1000);
}

// --- Init ---

if (apiKey) {
  rpc("daemon.version")
    .then(() => showApp())
    .catch(() => showAuth());
} else {
  showAuth();
}
