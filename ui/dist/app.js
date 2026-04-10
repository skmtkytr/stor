"use strict";

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => document.querySelectorAll(sel);
const KEY = "stor_api_key";

let apiKey = localStorage.getItem(KEY) || "";
let pollTimer = null;
let selected = new Set(); // selected torrent IDs
let lastChecked = null;   // for shift-click range select

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

function encodeFile(file) {
  return file.arrayBuffer().then((buf) => {
    const bytes = new Uint8Array(buf);
    let raw = "";
    for (let i = 0; i < bytes.length; i += 8192) {
      raw += String.fromCharCode(...bytes.subarray(i, i + 8192));
    }
    return btoa(raw);
  });
}

$("#add-file").addEventListener("change", async (e) => {
  const files = Array.from(e.target.files);
  if (!files.length) return;
  let ok = 0, fail = 0;
  await Promise.all(files.map(async (file) => {
    try { await rpc("torrent.addFile", { data: await encodeFile(file) }); ok++; } catch { fail++; }
  }));
  toast(`${ok} added` + (fail ? `, ${fail} failed` : ""), fail > 0);
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
    try { await rpc("torrent.addFile", { data: await encodeFile(file) }); ok++; } catch { fail++; }
  }));
  toast(`${ok} added` + (fail ? `, ${fail} failed` : ""), fail > 0);
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
  return Math.floor(secs / 3600) + "h " + Math.floor((secs % 3600) / 60) + "m";
}

// Keep sorted torrent list for shift-click range select
let currentTorrents = [];

function renderTorrents(torrents) {
  const tbody = $("#torrent-list");
  const empty = $("#empty-msg");

  if (!torrents || torrents.length === 0) {
    tbody.innerHTML = "";
    empty.hidden = false;
    currentTorrents = [];
    return;
  }
  empty.hidden = true;

  torrents.sort((a, b) => {
    const so = { downloading: 0, metadata: 0, adding: 1, paused: 2, error: 3, complete: 4 };
    const sa = so[a.state] ?? 5, sb = so[b.state] ?? 5;
    if (sa !== sb) return sa - sb;
    return (a.queue_position ?? 999) - (b.queue_position ?? 999);
  });
  currentTorrents = torrents;

  // Clean up selected IDs that no longer exist
  const ids = new Set(torrents.map((t) => t.id));
  for (const id of selected) { if (!ids.has(id)) selected.delete(id); }

  tbody.innerHTML = torrents.map((t) => {
    const p = t.progress;
    const pct = p.percent ? p.percent.toFixed(1) : "0.0";
    const fillCls = t.state === "complete" ? "fill complete" : "fill";
    const speed = t.state === "downloading" && p.down_speed ? formatBytes(p.down_speed) + "/s" : "-";
    const eta = t.state === "downloading" ? formatETA(p.downloaded, p.total, p.down_speed) : "-";
    const peers = t.state === "downloading" && p.active_peers ? p.active_peers : "-";
    const size = t.total_bytes ? formatBytes(t.total_bytes) : "-";
    const name = t.name || t.id.slice(0, 12) + "...";
    const qpos = t.queue_position != null ? t.queue_position : "-";
    const rowCls = selected.has(t.id) ? "selected" : "";

    return `<tr class="${rowCls}" data-id="${t.id}">
      <td title="${t.id}">
        <div class="name">${esc(name)}</div>
        ${t.error ? `<div class="error-detail">${esc(t.error)}</div>` : ""}
      </td>
      <td>${size}</td>
      <td>
        <div class="progress-bar"><div class="${fillCls}" style="width:${pct}%"></div></div>
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
  if (document.activeElement !== input) input.value = stats.max_active;
  // Selection count
  const selCount = selected.size;
  $("#sel-count").textContent = selCount ? `${selCount} selected` : "";
}

// --- Selection: click row, Shift+click range, Ctrl/Cmd+click toggle ---

$("#torrent-list").addEventListener("click", (e) => {
  // Don't interfere with button clicks inside rows
  if (e.target.closest("button")) return;

  const row = e.target.closest("tr[data-id]");
  if (!row) return;
  const id = row.dataset.id;
  const idx = currentTorrents.findIndex((t) => t.id === id);

  if (e.shiftKey && lastChecked !== null) {
    // Range select
    const from = Math.min(lastChecked, idx);
    const to = Math.max(lastChecked, idx);
    if (!e.ctrlKey && !e.metaKey) selected.clear();
    for (let i = from; i <= to; i++) {
      selected.add(currentTorrents[i].id);
    }
  } else if (e.ctrlKey || e.metaKey) {
    // Toggle single
    if (selected.has(id)) selected.delete(id);
    else selected.add(id);
  } else {
    // Single select (replace)
    selected.clear();
    selected.add(id);
  }
  lastChecked = idx;
  renderTorrents(currentTorrents);
});

// Keyboard shortcuts: Ctrl+A select all, Escape deselect
document.addEventListener("keydown", (e) => {
  if (e.key === "a" && (e.ctrlKey || e.metaKey) && !e.target.closest("input")) {
    e.preventDefault();
    currentTorrents.forEach((t) => selected.add(t.id));
    renderTorrents(currentTorrents);
  }
  if (e.key === "Escape") {
    selected.clear();
    lastChecked = null;
    renderTorrents(currentTorrents);
    ctxMenu.classList.remove("visible");
  }
});

// --- Context menu ---

const ctxMenu = $("#ctx-menu");

$("#torrent-list").addEventListener("contextmenu", (e) => {
  e.preventDefault();
  // If right-clicked on a row that isn't selected, select only that row
  const row = e.target.closest("tr[data-id]");
  if (row) {
    const id = row.dataset.id;
    if (!selected.has(id)) {
      selected.clear();
      selected.add(id);
      renderTorrents(currentTorrents);
    }
  }
  if (selected.size === 0) return;
  ctxMenu.style.left = e.pageX + "px";
  ctxMenu.style.top = e.pageY + "px";
  ctxMenu.classList.add("visible");
});

document.addEventListener("click", () => {
  ctxMenu.classList.remove("visible");
});

async function ctxAction(action) {
  ctxMenu.classList.remove("visible");
  const ids = [...selected];
  if (!ids.length) return;

  switch (action) {
    case "pause":
      await batchRPC(ids, (id) => rpc("torrent.pause", { id }));
      toast(`${ids.length} paused`);
      break;
    case "resume":
      await batchRPC(ids, (id) => rpc("torrent.resume", { id }));
      toast(`${ids.length} resumed`);
      break;
    case "remove":
      if (!confirm(`Remove ${ids.length} torrent(s)?`)) return;
      const del = confirm("Also delete downloaded files?");
      await batchRPC(ids, (id) => rpc("torrent.remove", { id, delete_files: del }));
      selected.clear();
      toast(`${ids.length} removed`);
      break;
    case "queue-top":
      // Move in reverse order so they stack at top in selection order
      for (const id of ids.reverse()) { await rpc("torrent.queueTop", { id }); }
      toast(`${ids.length} moved to top`);
      break;
    case "queue-bottom":
      for (const id of ids) { await rpc("torrent.queueBottom", { id }); }
      toast(`${ids.length} moved to bottom`);
      break;
  }
  refresh();
}

async function batchRPC(ids, fn) {
  await Promise.all(ids.map((id) => fn(id).catch(() => {})));
}

window.ctxAction = ctxAction;

// --- Single row actions (kept for queue buttons) ---

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

window.queueMove = queueMove;
window.setMaxActive = setMaxActive;

// --- Toast ---

function toast(msg, isError) {
  const el = $("#toast");
  el.textContent = msg;
  el.className = "toast show" + (isError ? " error" : "");
  setTimeout(() => { el.className = "toast"; }, 3000);
}

// --- Column resize (persistent) ---

const COL_WIDTHS_KEY = "stor_col_widths";

(function initColumnResize() {
  const table = $("#torrent-table");
  const cols = [...table.querySelectorAll("colgroup col")];
  const ths = [...table.querySelectorAll("thead th")];

  // Restore saved widths or snapshot initial render
  const saved = (() => {
    try { return JSON.parse(localStorage.getItem(COL_WIDTHS_KEY)); } catch { return null; }
  })();

  requestAnimationFrame(() => {
    ths.forEach((th, i) => {
      if (!cols[i]) return;
      if (saved && saved[i]) {
        cols[i].style.width = saved[i] + "px";
      } else {
        cols[i].style.width = th.offsetWidth + "px";
      }
    });
  });

  function saveWidths() {
    const widths = ths.map((th) => th.offsetWidth);
    localStorage.setItem(COL_WIDTHS_KEY, JSON.stringify(widths));
  }

  ths.forEach((th, i) => {
    const handle = document.createElement("div");
    handle.className = "col-resize";
    th.appendChild(handle);

    // Drag to resize
    handle.addEventListener("mousedown", (e) => {
      e.preventDefault();
      e.stopPropagation();
      const startX = e.pageX;
      const startW = th.offsetWidth;
      handle.classList.add("active");

      const onMove = (ev) => {
        const w = Math.max(40, startW + ev.pageX - startX);
        cols[i].style.width = w + "px";
      };
      const onUp = () => {
        handle.classList.remove("active");
        document.removeEventListener("mousemove", onMove);
        document.removeEventListener("mouseup", onUp);
        document.body.style.cursor = "";
        saveWidths();
      };
      document.body.style.cursor = "col-resize";
      document.addEventListener("mousemove", onMove);
      document.addEventListener("mouseup", onUp);
    });

    // Double-click to auto-fit
    handle.addEventListener("dblclick", (e) => {
      e.preventDefault();
      e.stopPropagation();
      // Temporarily remove fixed layout to measure content
      table.style.tableLayout = "auto";
      cols[i].style.width = "";
      requestAnimationFrame(() => {
        const autoW = th.offsetWidth;
        table.style.tableLayout = "fixed";
        cols[i].style.width = Math.max(40, autoW) + "px";
        saveWidths();
      });
    });
  });
})();

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
  rpc("daemon.version").then(() => showApp()).catch(() => showAuth());
} else {
  showAuth();
}
