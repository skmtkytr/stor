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
    // dataUrl is "data:application/x-bittorrent;base64,XXXX"
    const base64 = dataUrl.split(",")[1];
    if (!base64) throw new Error("Invalid data URL");
    await rpc("torrent.addFile", { data: base64 });
    badge("Add", "#238636");
  } catch (e) {
    console.error("stor: addFile failed:", e);
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

// Re-setup when settings change
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

// --- Magnet link interception via webNavigation ---

chrome.webNavigation.onBeforeNavigate.addListener(
  async (details) => {
    if (details.url.startsWith("magnet:")) {
      const cfg = await getConfig();
      if (cfg.handleMagnets) {
        addTorrentUrl(details.url);
      }
    }
  },
  { url: [{ urlPrefix: "magnet:" }] }
);

// --- Messages from content script ---

chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  if (msg.type === "add_torrent_url") {
    addTorrentUrl(msg.url).then(() => sendResponse({ ok: true }));
    return true; // async
  }
  if (msg.type === "add_torrent_file") {
    // Content script sends a fetch'd .torrent as data URL
    addTorrentFile(msg.dataUrl).then(() => sendResponse({ ok: true }));
    return true;
  }
  if (msg.type === "download_and_add") {
    // Background fetches .torrent URL, converts to base64, sends to daemon
    (async () => {
      try {
        const res = await fetch(msg.url);
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
      sendResponse({ ok: true });
    })();
    return true;
  }
});
