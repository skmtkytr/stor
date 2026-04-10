"use strict";

const $ = (sel) => document.querySelector(sel);

// Load saved settings
chrome.storage.sync.get(
  ["daemonUrl", "apiKey", "handle_torrents", "handle_magnets", "context_menu", "badge_timeout"],
  (cfg) => {
    if (cfg.daemonUrl) $("#url").value = cfg.daemonUrl;
    if (cfg.apiKey) $("#key").value = cfg.apiKey;
    $("#handle-magnets").checked = cfg.handle_magnets ?? true;
    $("#handle-torrents").checked = cfg.handle_torrents ?? true;
    $("#context-menu").checked = cfg.context_menu ?? true;
    if (cfg.badge_timeout) $("#badge-timeout").value = String(cfg.badge_timeout);
  }
);

// Save settings
$("#save").addEventListener("click", () => {
  chrome.storage.sync.set(
    {
      daemonUrl: $("#url").value.trim(),
      apiKey: $("#key").value.trim(),
      handle_magnets: $("#handle-magnets").checked,
      handle_torrents: $("#handle-torrents").checked,
      context_menu: $("#context-menu").checked,
      badge_timeout: $("#badge-timeout").value,
    },
    () => {
      const el = $("#saved");
      el.style.display = "inline";
      setTimeout(() => (el.style.display = "none"), 2000);
    }
  );
});

// Test connection
$("#test-btn").addEventListener("click", async () => {
  const url = $("#url").value.trim();
  const key = $("#key").value.trim();
  const result = $("#test-result");

  if (!url || !key) {
    result.textContent = "Please enter URL and API key first.";
    result.className = "test-result err";
    return;
  }

  result.textContent = "Testing...";
  result.className = "test-result";

  try {
    const res = await fetch(url.replace(/\/$/, "") + "/api/rpc", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: "Bearer " + key,
      },
      body: JSON.stringify({ jsonrpc: "2.0", method: "daemon.version", id: 1 }),
    });

    if (!res.ok) throw new Error("HTTP " + res.status);
    const data = await res.json();
    if (data.error) throw new Error(data.error.message);

    result.textContent = "Connected! Version: " + (data.result?.version || "unknown");
    result.className = "test-result ok";
  } catch (e) {
    result.textContent = "Failed: " + e.message;
    result.className = "test-result err";
  }
});
