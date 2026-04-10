"use strict";

// Context menu: right-click any link → "Download with stor"
chrome.runtime.onInstalled.addListener(() => {
  chrome.contextMenus.create({
    id: "stor-add-link",
    title: "Download with stor",
    contexts: ["link"],
  });
});

chrome.contextMenus.onClicked.addListener((info) => {
  if (info.menuItemId === "stor-add-link" && info.linkUrl) {
    addTorrent(info.linkUrl);
  }
});

// Intercept magnet: link clicks via navigation
chrome.webNavigation?.onBeforeNavigate?.addListener(
  (details) => {
    if (details.url.startsWith("magnet:")) {
      addTorrent(details.url);
    }
  },
  { url: [{ urlPrefix: "magnet:" }] }
);

async function addTorrent(url) {
  const { daemonUrl, apiKey } = await chrome.storage.sync.get([
    "daemonUrl",
    "apiKey",
  ]);

  if (!daemonUrl || !apiKey) {
    console.error("stor: daemon URL or API key not configured");
    notify("stor", "Please configure daemon URL and API key in extension options.");
    return;
  }

  try {
    const res = await fetch(daemonUrl.replace(/\/$/, "") + "/api/add", {
      method: "POST",
      headers: {
        "Content-Type": "application/x-www-form-urlencoded",
        Authorization: "Bearer " + apiKey,
      },
      body: "url=" + encodeURIComponent(url),
    });

    if (!res.ok) {
      const text = await res.text();
      throw new Error(text);
    }

    const data = await res.json();
    notify("stor", "Torrent added: " + (data.id || "").slice(0, 12) + "...");
  } catch (e) {
    console.error("stor: add failed:", e);
    notify("stor", "Failed to add torrent: " + e.message);
  }
}

function notify(title, message) {
  // Use the badge as a simple notification since chrome.notifications
  // requires additional permission
  chrome.action.setBadgeText({ text: "!" });
  chrome.action.setBadgeBackgroundColor({ color: "#238636" });
  setTimeout(() => chrome.action.setBadgeText({ text: "" }), 3000);
  console.log(`[${title}] ${message}`);
}
