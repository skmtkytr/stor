"use strict";

// Content script: intercept .torrent and magnet: link clicks on pages.
// Runs on all http/https pages.

async function getConfig() {
  return new Promise((resolve) => {
    chrome.storage.sync.get(["handle_torrents", "handle_magnets"], (cfg) => {
      resolve({
        handleTorrents: cfg.handle_torrents ?? true,
        handleMagnets: cfg.handle_magnets ?? true,
      });
    });
  });
}

document.addEventListener(
  "click",
  async (e) => {
    const link = e.target.closest("a[href]");
    if (!link) return;

    const href = link.href;
    const cfg = await getConfig();

    // Magnet links
    if (href.startsWith("magnet:") && cfg.handleMagnets) {
      e.preventDefault();
      e.stopPropagation();
      chrome.runtime.sendMessage({ type: "add_torrent_url", url: href });
      return;
    }

    // .torrent file links
    if (cfg.handleTorrents) {
      const url = new URL(href, location.href);
      const path = url.pathname.toLowerCase();
      if (path.endsWith(".torrent")) {
        e.preventDefault();
        e.stopPropagation();
        // Let background script download the .torrent file (avoids CORS issues)
        chrome.runtime.sendMessage({ type: "download_and_add", url: href });
      }
    }
  },
  true // capture phase to intercept before default behavior
);
