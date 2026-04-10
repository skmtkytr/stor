"use strict";

const $ = (sel) => document.querySelector(sel);
const KEY = "stor_api_key";

let apiKey = localStorage.getItem(KEY) || "";
let pollTimer = null;

// --- Auth ---

function showAuth() {
  $("#auth-screen").hidden = false;
  $("#app-screen").hidden = true;
}

function showApp() {
  $("#auth-screen").hidden = true;
  $("#app-screen").hidden = false;
  startPolling();
}

$("#key-submit").addEventListener("click", async () => {
  const key = $("#key-input").value.trim();
  if (!key) return;
  apiKey = key;
  $("#auth-error").hidden = true;
  try {
    const result = await rpc("daemon.version");
    console.log("Auth OK:", result);
    localStorage.setItem(KEY, apiKey);
    showApp();
  } catch (e) {
    console.error("Auth failed:", e);
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
  const res = await fetch("/api/rpc", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: "Bearer " + apiKey,
    },
    body: JSON.stringify({ jsonrpc: "2.0", method, params, id: 1 }),
  });
  if (res.status === 401) {
    localStorage.removeItem(KEY);
    apiKey = "";
    showAuth();
    throw new Error("unauthorized");
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
    refresh();
  } catch (e) {
    alert("Add failed: " + e.message);
  }
});

$("#add-input").addEventListener("keydown", (e) => {
  if (e.key === "Enter") $("#add-btn").click();
});

$("#add-file").addEventListener("change", async (e) => {
  const file = e.target.files[0];
  if (!file) return;
  const buf = await file.arrayBuffer();
  const b64 = btoa(String.fromCharCode(...new Uint8Array(buf)));
  try {
    await rpc("torrent.addFile", { data: b64 });
    refresh();
  } catch (err) {
    alert("Upload failed: " + err.message);
  }
  e.target.value = "";
});

// --- Torrent list ---

function formatBytes(b) {
  if (b >= 1 << 30) return (b / (1 << 30)).toFixed(2) + " GB";
  if (b >= 1 << 20) return (b / (1 << 20)).toFixed(1) + " MB";
  if (b >= 1 << 10) return (b / (1 << 10)).toFixed(0) + " KB";
  return b + " B";
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

  tbody.innerHTML = torrents
    .map((t) => {
      const p = t.progress;
      const pct = p.percent ? p.percent.toFixed(1) : "0.0";
      const fillClass = t.state === "complete" ? "fill complete" : "fill";
      const speed =
        t.state === "downloading" && p.down_speed
          ? formatBytes(p.down_speed) + "/s"
          : "-";
      const peers =
        t.state === "downloading" && p.active_peers ? p.active_peers : "-";
      const size = t.total_bytes ? formatBytes(t.total_bytes) : "-";
      const name = t.name || t.id.slice(0, 12) + "...";

      return `<tr>
      <td title="${t.id}">${esc(name)}</td>
      <td>${size}</td>
      <td>
        <div class="progress-bar"><div class="${fillClass}" style="width:${pct}%"></div></div>
        <div class="progress-text">${pct}%${p.done_pieces ? ` (${p.done_pieces}/${p.total_pieces})` : ""}</div>
      </td>
      <td>${speed}</td>
      <td>${peers}</td>
      <td><span class="state state-${t.state}">${t.state}</span></td>
      <td class="actions">
        ${t.state === "downloading" || t.state === "metadata" ? `<button onclick="pause('${t.id}')">Pause</button>` : ""}
        ${t.state === "paused" || t.state === "error" ? `<button onclick="resume('${t.id}')">Resume</button>` : ""}
        <button class="danger" onclick="remove('${t.id}')">Remove</button>
      </td>
    </tr>`;
    })
    .join("");
}

function esc(s) {
  const d = document.createElement("div");
  d.textContent = s;
  return d.innerHTML;
}

function renderStats(stats) {
  const el = $("#stats");
  if (!stats) return;
  const speed = stats.total_down_speed
    ? formatBytes(stats.total_down_speed) + "/s"
    : "0 B/s";
  el.textContent = `${stats.active_torrents} active / ${stats.total_torrents} total | ${speed}`;
}

// --- Actions ---

async function pause(id) {
  try { await rpc("torrent.pause", { id }); refresh(); } catch (e) { alert(e.message); }
}

async function resume(id) {
  try { await rpc("torrent.resume", { id }); refresh(); } catch (e) { alert(e.message); }
}

async function remove(id) {
  if (!confirm("Remove this torrent?")) return;
  const deleteFiles = confirm("Also delete downloaded files?");
  try { await rpc("torrent.remove", { id, delete_files: deleteFiles }); refresh(); } catch (e) { alert(e.message); }
}

// Expose to inline onclick
window.pause = pause;
window.resume = resume;
window.remove = remove;

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
    .catch((e) => {
      console.error("Init auth failed:", e);
      showAuth();
    });
} else {
  showAuth();
}
