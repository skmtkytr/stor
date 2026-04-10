"use strict";

const $ = (sel) => document.querySelector(sel);

let daemonUrl = "";
let apiKey = "";

async function loadConfig() {
  const cfg = await chrome.storage.sync.get(["daemonUrl", "apiKey"]);
  daemonUrl = cfg.daemonUrl || "";
  apiKey = cfg.apiKey || "";
}

async function rpc(method, params) {
  if (!daemonUrl || !apiKey) throw new Error("not configured");
  const res = await fetch(daemonUrl.replace(/\/$/, "") + "/api/rpc", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: "Bearer " + apiKey,
    },
    body: JSON.stringify({ jsonrpc: "2.0", method, params, id: 1 }),
  });
  if (!res.ok) throw new Error("HTTP " + res.status);
  const data = await res.json();
  if (data.error) throw new Error(data.error.message);
  return data.result;
}

// --- Formatting ---

function formatBytes(b) {
  if (b >= 1 << 30) return (b / (1 << 30)).toFixed(1) + " GB";
  if (b >= 1 << 20) return (b / (1 << 20)).toFixed(1) + " MB";
  if (b >= 1 << 10) return (b / (1 << 10)).toFixed(0) + " KB";
  return b + " B";
}

function formatSpeed(b) {
  if (!b) return "";
  return formatBytes(b) + "/s";
}

function formatETA(downloaded, total, speed) {
  if (!speed || speed <= 0 || downloaded >= total) return "";
  const secs = Math.round((total - downloaded) / speed);
  if (secs < 60) return secs + "s";
  if (secs < 3600) return Math.floor(secs / 60) + "m " + (secs % 60) + "s";
  return Math.floor(secs / 3600) + "h " + Math.floor((secs % 3600) / 60) + "m";
}

function esc(s) {
  const d = document.createElement("div");
  d.textContent = s;
  return d.innerHTML;
}

// --- Sorting ---

const stateOrder = { downloading: 0, metadata: 1, adding: 2, paused: 3, error: 4, complete: 5 };

function getSortValue(t, key) {
  switch (key) {
    case "name": return (t.name || t.id).toLowerCase();
    case "size": return t.total_bytes || 0;
    case "progress": return t.progress.percent || 0;
    case "speed": return t.state === "downloading" ? (t.progress.down_speed || 0) : 0;
    case "eta": {
      const p = t.progress;
      if (t.state !== "downloading" || !p.down_speed) return Infinity;
      return (p.total - p.downloaded) / p.down_speed;
    }
    case "queue": return t.queue_position ?? 9999;
    default: return 0;
  }
}

function sortTorrents(list, key, desc) {
  return [...list].sort((a, b) => {
    const va = getSortValue(a, key);
    const vb = getSortValue(b, key);
    const cmp = typeof va === "string" ? va.localeCompare(vb) : va - vb;
    return desc ? -cmp : cmp;
  });
}

// --- Filtering ---

const filterMap = {
  all: () => true,
  downloading: (t) => t.state === "downloading" || t.state === "metadata" || t.state === "adding",
  seeding: (t) => t.state === "seeding",
  complete: (t) => t.state === "complete",
  paused: (t) => t.state === "paused",
  error: (t) => t.state === "error",
};

// --- Actions ---

async function doAction(method, id) {
  try {
    await rpc(method, { id });
  } catch (e) {
    console.error("stor action failed:", e);
  }
}

async function doRemove(id, name) {
  if (!confirm("Remove " + (name || id.slice(0, 12)) + "?")) return;
  const deleteFiles = confirm("Also delete downloaded files?");
  try {
    await rpc("torrent.remove", { id, delete_files: deleteFiles });
  } catch (e) {
    console.error("stor remove failed:", e);
  }
}

// --- Render ---

function renderStats(torrents) {
  const counts = { total: torrents.length, dl: 0, seed: 0, done: 0, paused: 0, error: 0 };
  for (const t of torrents) {
    if (t.state === "downloading" || t.state === "metadata" || t.state === "adding") counts.dl++;
    else if (t.state === "seeding") counts.seed++;
    else if (t.state === "complete") counts.done++;
    else if (t.state === "paused") counts.paused++;
    else if (t.state === "error") counts.error++;
  }
  $("#stat-total .n").textContent = counts.total;
  $("#stat-dl .n").textContent = counts.dl;
  $("#stat-seed .n").textContent = counts.seed;
  $("#stat-done .n").textContent = counts.done;
  $("#stat-paused .n").textContent = counts.paused;
  $("#stat-error .n").textContent = counts.error;

  // Highlight active stat
  document.querySelectorAll(".stat").forEach((el) => el.classList.remove("active"));
  const filter = $("#filter-select").value;
  const statMap = { all: "stat-total", downloading: "stat-dl", seeding: "stat-seed", complete: "stat-done", paused: "stat-paused", error: "stat-error" };
  if (statMap[filter]) document.getElementById(statMap[filter])?.classList.add("active");
}

function renderList(torrents) {
  const el = $("#list");
  if (!torrents || torrents.length === 0) {
    el.innerHTML = '<div class="empty">No torrents</div>';
    return;
  }

  el.innerHTML = torrents
    .map((t) => {
      const p = t.progress;
      const pct = (p.percent || 0).toFixed(1);
      const name = t.name || t.id.slice(0, 16);
      const speed = t.state === "downloading" && p.down_speed ? formatSpeed(p.down_speed) : "";
      const eta = t.state === "downloading"
        ? formatETA(p.downloaded, p.total, p.down_speed)
        : "";
      const size = t.total_bytes ? formatBytes(t.total_bytes) : "";
      const peers = t.state === "downloading" && p.active_peers ? p.active_peers + " peers" : "";
      const pauseBtn = t.state === "paused" || t.state === "error"
        ? `<button data-action="torrent.resume" data-id="${t.id}" title="Resume">&#9654;</button>`
        : `<button data-action="torrent.pause" data-id="${t.id}" title="Pause">&#10074;&#10074;</button>`;

      const meta = [t.state, size, speed ? `<span class="speed">${speed}</span>` : "", eta, peers]
        .filter(Boolean)
        .join(" &middot; ");

      return `<div class="item">
        <div class="item-top">
          <div class="item-name" title="${esc(t.name || t.id)}">${esc(name)}</div>
          <div class="item-actions">
            ${pauseBtn}
            <button data-action="torrent.queueUp" data-id="${t.id}" title="Queue Up">&#9650;</button>
            <button data-action="torrent.queueDown" data-id="${t.id}" title="Queue Down">&#9660;</button>
            <button class="danger" data-remove="${t.id}" data-name="${esc(name)}" title="Remove">&#10005;</button>
          </div>
        </div>
        <div class="item-meta">${meta} &middot; ${pct}%</div>
        <div class="bar"><div class="fill ${t.state}" style="width:${pct}%"></div></div>
      </div>`;
    })
    .join("");
}

// --- Event delegation for actions ---

$("#list").addEventListener("click", (e) => {
  const btn = e.target.closest("button[data-action]");
  if (btn) {
    doAction(btn.dataset.action, btn.dataset.id);
    return;
  }
  const rmBtn = e.target.closest("button[data-remove]");
  if (rmBtn) {
    doRemove(rmBtn.dataset.remove, rmBtn.dataset.name);
  }
});

// --- Main loop ---

let allTorrents = [];

async function refresh() {
  try {
    allTorrents = (await rpc("torrent.list")) || [];
    $("#conn").textContent = "Connected";
    $("#conn").className = "conn ok";
  } catch {
    allTorrents = [];
    $("#conn").textContent = "Disconnected";
    $("#conn").className = "conn err";
    if (!daemonUrl) {
      $("#list").innerHTML = '<div class="empty">Configure daemon URL in Settings</div>';
    }
    return;
  }

  renderStats(allTorrents);

  const filterKey = $("#filter-select").value;
  const sortKey = $("#sort-select").value;
  const desc = $("#sort-dir").value === "desc";

  let filtered = allTorrents.filter(filterMap[filterKey] || filterMap.all);
  filtered = sortTorrents(filtered, sortKey, desc);
  renderList(filtered);
}

// --- Add torrent ---

$("#add-btn").addEventListener("click", async () => {
  const url = $("#add-input").value.trim();
  if (!url) return;
  try {
    await rpc("torrent.add", { source: url });
    $("#add-input").value = "";
    refresh();
  } catch (e) {
    alert("Failed: " + e.message);
  }
});

$("#add-input").addEventListener("keydown", (e) => {
  if (e.key === "Enter") $("#add-btn").click();
});

// Sort/filter changes trigger re-render
$("#sort-select").addEventListener("change", refresh);
$("#sort-dir").addEventListener("change", refresh);
$("#filter-select").addEventListener("change", refresh);

// Save sort/filter preferences
function savePrefs() {
  chrome.storage.local.set({
    popupSort: $("#sort-select").value,
    popupDir: $("#sort-dir").value,
    popupFilter: $("#filter-select").value,
  });
}
$("#sort-select").addEventListener("change", savePrefs);
$("#sort-dir").addEventListener("change", savePrefs);
$("#filter-select").addEventListener("change", savePrefs);

// --- Init ---

(async () => {
  await loadConfig();

  // Restore preferences
  const prefs = await chrome.storage.local.get(["popupSort", "popupDir", "popupFilter"]);
  if (prefs.popupSort) $("#sort-select").value = prefs.popupSort;
  if (prefs.popupDir) $("#sort-dir").value = prefs.popupDir;
  if (prefs.popupFilter) $("#filter-select").value = prefs.popupFilter;

  refresh();
  setInterval(refresh, 2000);
})();
