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

function formatBytes(b) {
  if (b >= 1 << 30) return (b / (1 << 30)).toFixed(1) + " GB";
  if (b >= 1 << 20) return (b / (1 << 20)).toFixed(1) + " MB";
  if (b >= 1 << 10) return (b / (1 << 10)).toFixed(0) + " KB";
  return b + " B";
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
      const pct = p.percent ? p.percent.toFixed(1) : "0";
      const fillClass = t.state === "complete" ? "fill done" : "fill";
      const speed = t.state === "downloading" && p.down_speed ? formatBytes(p.down_speed) + "/s" : "";
      const name = t.name || t.id.slice(0, 12);
      const meta = [t.state, speed, t.total_bytes ? formatBytes(t.total_bytes) : ""].filter(Boolean).join(" | ");

      return `<div class="item">
        <div class="item-name" title="${t.id}">${esc(name)}</div>
        <div class="item-meta">${meta} | ${pct}%</div>
        <div class="bar"><div class="${fillClass}" style="width:${pct}%"></div></div>
      </div>`;
    })
    .join("");
}

function esc(s) {
  const d = document.createElement("div");
  d.textContent = s;
  return d.innerHTML;
}

async function refresh() {
  try {
    const torrents = await rpc("torrent.list");
    renderList(torrents);
    $("#status").textContent = "Connected";
    $("#status").className = "status ok";
  } catch {
    $("#status").textContent = "Disconnected";
    $("#status").className = "status err";
    if (!daemonUrl) {
      $("#list").innerHTML = '<div class="empty">Configure daemon URL in Settings</div>';
    }
  }
}

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

(async () => {
  await loadConfig();
  refresh();
  setInterval(refresh, 2000);
})();
