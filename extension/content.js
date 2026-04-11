"use strict";

// Content script: intercept .torrent and magnet: link clicks on pages.
// Runs at document_start to catch events before Chrome's protocol handler.

// Cache config to avoid async getConfig() in the click handler critical path.
// Default: both enabled. Updated async on load and when storage changes.
let handleTorrents = true;
let handleMagnets = true;

function loadConfig() {
  chrome.storage.sync.get(["handle_torrents", "handle_magnets"], (cfg) => {
    handleTorrents = cfg.handle_torrents ?? true;
    handleMagnets = cfg.handle_magnets ?? true;
  });
}
loadConfig();
chrome.storage.onChanged.addListener(() => loadConfig());

// Capture-phase click handler — MUST be synchronous to call preventDefault
// before Chrome hands the URL to the OS protocol handler (xdg-open).
document.addEventListener(
  "click",
  (e) => {
    const link = e.target.closest("a[href]");
    if (!link) return;

    const href = link.href;

    // Magnet links — synchronous preventDefault is critical on Linux
    if (handleMagnets && href.startsWith("magnet:")) {
      e.preventDefault();
      e.stopImmediatePropagation();
      chrome.runtime.sendMessage({ type: "add_torrent_url", url: href });
      return;
    }

    // .torrent file links
    if (handleTorrents) {
      try {
        const url = new URL(href, location.href);
        if (url.pathname.toLowerCase().endsWith(".torrent")) {
          e.preventDefault();
          e.stopImmediatePropagation();
          chrome.runtime.sendMessage({ type: "download_and_add", url: href });
        }
      } catch {
        // invalid URL, ignore
      }
    }
  },
  true // capture phase
);
