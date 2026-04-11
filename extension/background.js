"use strict";

// --- Config helpers ---

async function getConfig() {
  const cfg = await chrome.storage.sync.get([
    "daemonUrl",
    "apiKey",
    "handle_torrents",
    "handle_magnets",
    "context_menu",
    "badge_timeout",
  ]);
  return {
    daemonUrl: cfg.daemonUrl || "",
    apiKey: cfg.apiKey || "",
    handleTorrents: cfg.handle_torrents ?? true,
    handleMagnets: cfg.handle_magnets ?? true,
    contextMenu: cfg.context_menu ?? true,
    badgeTimeout: parseInt(cfg.badge_timeout) || 3000,
  };
}

// --- JSON-RPC client ---

async function rpc(method, params) {
  const { daemonUrl, apiKey } = await getConfig();
  if (!daemonUrl || !apiKey) {
    throw new Error("Not configured");
  }
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

// --- Badge notification ---

async function badge(text, color) {
  const cfg = await getConfig();
  chrome.action.setBadgeText({ text });
  chrome.action.setBadgeBackgroundColor({ color });
  setTimeout(() => chrome.action.setBadgeText({ text: "" }), cfg.badgeTimeout);
}

// --- Add torrent ---

async function addTorrentUrl(url) {
  try {
    await rpc("torrent.add", { source: url });
    badge("Add", "#238636");
  } catch (e) {
    console.error("stor: add failed:", e);
    badge("Fail", "#f85149");
  }
}

async function addTorrentFile(dataUrl) {
  try {
    const base64 = dataUrl.split(",")[1];
    if (!base64) throw new Error("Invalid data URL");
    await rpc("torrent.addFile", { data: base64 });
    badge("Add", "#238636");
  } catch (e) {
    console.error("stor: addFile failed:", e);
    badge("Fail", "#f85149");
  }
}

// Fetch a .torrent URL, convert to base64, send to daemon
async function downloadAndAdd(url) {
  try {
    const res = await fetch(url);
    if (!res.ok) throw new Error("HTTP " + res.status);
    const buf = await res.arrayBuffer();
    const bytes = new Uint8Array(buf);
    let raw = "";
    for (let i = 0; i < bytes.length; i += 8192) {
      raw += String.fromCharCode(...bytes.subarray(i, i + 8192));
    }
    const base64 = btoa(raw);
    await rpc("torrent.addFile", { data: base64 });
    badge("Add", "#238636");
  } catch (e) {
    console.error("stor: download_and_add failed:", e);
    badge("Fail", "#f85149");
  }
}

// --- Context menu ---

async function setupContextMenu() {
  await chrome.contextMenus.removeAll();
  const cfg = await getConfig();
  if (cfg.contextMenu) {
    chrome.contextMenus.create({
      id: "stor-add-link",
      title: "Download with stor",
      contexts: ["link"],
    });
  }
}

chrome.runtime.onInstalled.addListener(() => {
  setupContextMenu();
});

chrome.storage.onChanged.addListener((changes) => {
  if (changes.context_menu) {
    setupContextMenu();
  }
});

chrome.contextMenus.onClicked.addListener((info) => {
  if (info.menuItemId === "stor-add-link" && info.linkUrl) {
    addTorrentUrl(info.linkUrl);
  }
});

// --- .torrent download interception via chrome.downloads API ---
// This catches ALL .torrent downloads including:
// - Direct link clicks (already caught by content script, but this is a safety net)
// - Redirects that result in Content-Disposition: attachment; filename="x.torrent"
// - JavaScript-initiated downloads (window.location, form submit)

chrome.downloads.onCreated.addListener(async (item) => {
  const cfg = await getConfig();
  if (!cfg.handleTorrents) return;

  const url = item.finalUrl || item.url || "";
  const filename = (item.filename || "").toLowerCase();
  const mime = (item.mime || "").toLowerCase();

  const isTorrent =
    url.toLowerCase().includes(".torrent") ||
    filename.endsWith(".torrent") ||
    mime === "application/x-bittorrent";

  if (isTorrent) {
    // Cancel the browser download
    chrome.downloads.cancel(item.id);
    chrome.downloads.erase({ id: item.id });
    // Send to daemon instead
    downloadAndAdd(url);
  }
});

// --- Magnet link interception ---
// webNavigation.onBeforeNavigate detects magnet: navigations but cannot cancel them.
// We use tabs.update to redirect the tab away from the magnet: URL, preventing
// the browser from passing it to the OS protocol handler (xdg-open etc.).

chrome.webNavigation.onBeforeNavigate.addListener(
  async (details) => {
    if (!details.url.startsWith("magnet:")) return;
    const cfg = await getConfig();
    if (!cfg.handleMagnets) return;

    addTorrentUrl(details.url);

    // Redirect the tab to prevent the browser from handing off to OS
    // For main frame navigations, go back or close the blank tab
    if (details.frameId === 0) {
      try {
        // Navigate back to stop the magnet: protocol handler
        chrome.tabs.goBack(details.tabId).catch(() => {
          // If no history (new tab opened for magnet), close it
          chrome.tabs.remove(details.tabId).catch(() => {});
        });
      } catch {
        // Ignore errors (tab may already be gone)
      }
    }
  },
  { url: [{ urlPrefix: "magnet:" }] }
);

// --- Messages from content script ---

chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  if (msg.type === "add_torrent_url") {
    addTorrentUrl(msg.url).then(() => sendResponse({ ok: true }));
    return true;
  }
  if (msg.type === "add_torrent_file") {
    addTorrentFile(msg.dataUrl).then(() => sendResponse({ ok: true }));
    return true;
  }
  if (msg.type === "download_and_add") {
    downloadAndAdd(msg.url).then(() => sendResponse({ ok: true }));
    return true;
  }
});
