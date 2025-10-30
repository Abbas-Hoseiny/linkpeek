// Tunnel feature module migrated from legacy app.js
import {
  $,
  jget,
  showCopyFeedback,
  copyTextToClipboard,
  formatDateTime,
  escapeHTML,
  escapeAttr,
  extractItems,
} from "../lib/utils.js";
import { realtimeClient } from "../lib/realtime.js";

const HISTORY_LIMIT = 200;
const FAST_REFRESH_INTERVAL = 5000;
const FALLBACK_REFRESH_INTERVAL = 60000;
const RESTART_RETRIES = 10;

let appState = { currentTunnelURL: "", tunnelActive: null };
let rt = realtimeClient;

let dom = {};
let statusCache = null;
let historyCache = [];
let loadedOnce = false;

let fastTimer = null;
let fallbackTimer = null;

let unsubscribeStatus = null;
let unsubscribeHistory = null;

export function init(context = {}) {
  appState = context.state || appState;
  if (context.realtimeClient) {
    rt = context.realtimeClient;
  }

  dom = {
    page: $("#pageTunnel"),
    grid: document.getElementById("tunnelGrid"),
  };

  ensureTiles();
  subscribeRealtime();
  startAutoRefresh();
}

export function activate() {
  ensureTunnelPage();
}

function ensureTunnelPage(force = false) {
  ensureTiles();
  if (force) loadedOnce = false;
  if (!loadedOnce) {
    loadedOnce = true;
    loadTunnelStatus();
    loadTunnelHistory();
  } else {
    loadTunnelStatus({ silent: true });
    loadTunnelHistory({ silent: true });
  }
}

function ensureTiles() {
  if (!dom.grid) return;

  if (!dom.tunnelTile) {
    dom.tunnelTile = createTile("tunnel", "Tunnel", "Loading…");
    dom.grid.prepend(dom.tunnelTile);
    const head = dom.tunnelTile.querySelector(".tile-head");
    dom.copyButton = createHeadButton("Copy link", () => handleCopyMain());
    dom.copyAlphaButton = createHeadButton("Copy alpha", () =>
      handleCopyAlpha()
    );
    dom.restartButton = createHeadButton("Restart", () => handleRestart());
    head.append(dom.copyButton, dom.copyAlphaButton, dom.restartButton);
  }

  if (!dom.historyTile) {
    dom.historyTile = createTile("tunnel-history", "History", "Loading…");
    const head = dom.historyTile.querySelector(".tile-head");
    dom.clearHistoryButton = createHeadButton(
      "Clear",
      () => handleClearHistory(),
      "btn warn sm"
    );
    head.appendChild(dom.clearHistoryButton);
    dom.grid.appendChild(dom.historyTile);
  }

  updateCopyButtonState();
}

function subscribeRealtime() {
  if (!rt) return;

  if (!unsubscribeStatus) {
    unsubscribeStatus = rt.subscribe(
      "tunnel.status",
      (payload) => {
        applyTunnelStatus(payload, { fromRealtime: true });
      },
      { snapshot: true }
    );
  }

  if (!unsubscribeHistory) {
    unsubscribeHistory = rt.subscribe(
      "tunnel.history",
      (payload) => {
        const items = extractItems(payload);
        historyCache = items;
        renderTunnelHistory();
      },
      { snapshot: true }
    );
  }
}

function startAutoRefresh() {
  if (!fastTimer) {
    fastTimer = window.setInterval(() => {
      if (!isPageActive()) return;
      loadTunnelStatus({ silent: true });
      loadTunnelHistory({ silent: true });
    }, FAST_REFRESH_INTERVAL);
  }

  if (!fallbackTimer) {
    fallbackTimer = window.setInterval(() => {
      if (!isPageActive()) return;
      if (isRealtimeConnected()) return;
      loadTunnelStatus({ silent: true });
    }, FALLBACK_REFRESH_INTERVAL);
  }
}

async function loadTunnelStatus(options = {}) {
  if (!isPageActive() && !options.force) return;
  try {
    const status = await jget("/api/tunnel/status");
    let combined = { ...status };
    try {
      const info = await jget("/api/tunnel");
      if (info && typeof info === "object") {
        combined = { ...info, ...combined };
        if (!combined.url && info.url) combined.url = info.url;
        if (!combined.alpha_url && info.alpha_url)
          combined.alpha_url = info.alpha_url;
      }
    } catch (err) {
      if (!options.silent) {
        console.warn("Failed to fetch tunnel info", err);
      }
    }
    applyTunnelStatus(combined, options);
  } catch (error) {
    if (!options.silent) {
      console.error("Failed to load tunnel status", error);
    }
  }
}

function applyTunnelStatus(status, options = {}) {
  if (!status || typeof status !== "object") return;
  statusCache = status;

  const sanitized = sanitizeTunnelURL(status.url);
  if (appState) {
    appState.currentTunnelURL = sanitized || appState.currentTunnelURL || "";
    if (typeof status.active === "boolean") {
      appState.tunnelActive = Boolean(status.active);
    }
  }

  updateCopyButtonState();
  renderTunnelStatus();

  if (!options.silent && !options.fromRealtime && !status.active) {
    console.warn(
      "Tunnel inactive; external links fall back to localhost until restarted."
    );
  }
}

async function loadTunnelHistory(options = {}) {
  if (!isPageActive() && !options.force) return;
  try {
    const data = await jget(`/api/tunnel/history?n=${HISTORY_LIMIT}`);
    historyCache = Array.isArray(data)
      ? data
      : Array.isArray(data?.items)
      ? data.items
      : [];
    renderTunnelHistory();
  } catch (error) {
    if (!options.silent) {
      console.error("Failed to load tunnel history", error);
    }
  }
}

function renderTunnelStatus() {
  ensureTiles();
  if (!dom.tunnelTile) return;

  const head = dom.tunnelTile.querySelector(".tile-head strong");
  const box = dom.tunnelTile.querySelector(".box");
  const active = Boolean(statusCache?.active);
  if (head) {
    head.textContent = `Tunnel ${active ? "· active" : "· inactive"}`;
  }

  if (!box) return;

  const lines = [];
  if (statusCache?.url) lines.push(`URL: ${statusCache.url}`);
  if (statusCache?.alpha_url) lines.push(`Alpha: ${statusCache.alpha_url}`);
  if (statusCache?.since)
    lines.push(`Since: ${formatDateTime(statusCache.since)}`);
  if (statusCache?.last_seen)
    lines.push(`Last seen: ${formatDateTime(statusCache.last_seen)}`);
  if (statusCache?.uptime_secs != null) {
    lines.push(`Uptime: ${formatDuration(Number(statusCache.uptime_secs))}`);
  }

  box.textContent = lines.length ? lines.join("\n") : "No tunnel information.";
}

function renderTunnelHistory() {
  ensureTiles();
  if (!dom.historyTile) return;

  const count = historyCache.length;
  const head = dom.historyTile.querySelector(".tile-head strong");
  if (head) {
    head.textContent = `History (${count})`;
  }

  const box = dom.historyTile.querySelector(".box");
  if (!box) return;

  if (!count) {
    box.textContent = "No tunnel history yet.";
    return;
  }

  const lines = historyCache.map((item) => {
    const seenAt = formatDateTime(item?.seen_at) || "—";
    const url = String(item?.url || "");
    const safeUrl = escapeAttr(url);
    return `${escapeHTML(
      seenAt
    )} · <a href="${safeUrl}" target="_blank" rel="noopener">${escapeHTML(
      url
    )}</a>`;
  });
  box.innerHTML = lines.join("<br>");
}

function updateCopyButtonState() {
  const hasURL = Boolean(appState?.currentTunnelURL);
  if (dom.copyButton) dom.copyButton.disabled = !hasURL;
  if (dom.copyAlphaButton) dom.copyAlphaButton.disabled = !hasURL;
}

async function handleCopyMain() {
  const url = appState?.currentTunnelURL;
  if (!url || !dom.copyButton) return;
  try {
    const copied = await copyTextToClipboard(url);
    if (copied) {
      showCopyFeedback(dom.copyButton, "Copy link", "Copied!");
    } else {
      showCopyFeedback(dom.copyButton, "Copy link", "Failed");
    }
  } catch (error) {
    console.warn("Copy failed", error);
    showCopyFeedback(dom.copyButton, "Copy link", "Failed");
  }
}

async function handleCopyAlpha() {
  const base = appState?.currentTunnelURL;
  if (!base || !dom.copyAlphaButton) return;
  const alpha = `${base.replace(/\/$/, "")}/alpha`;
  try {
    const copied = await copyTextToClipboard(alpha);
    if (copied) {
      showCopyFeedback(dom.copyAlphaButton, "Copy alpha", "Copied!");
    } else {
      showCopyFeedback(dom.copyAlphaButton, "Copy alpha", "Failed");
    }
  } catch (error) {
    console.warn("Copy alpha failed", error);
    showCopyFeedback(dom.copyAlphaButton, "Copy alpha", "Failed");
  }
}

async function handleRestart() {
  if (!dom.restartButton) return;
  const previousURL = appState?.currentTunnelURL || "";
  const buttons = [dom.copyButton, dom.copyAlphaButton];
  const originalLabel = dom.restartButton.textContent;
  dom.restartButton.disabled = true;
  dom.restartButton.innerHTML = '<span class="spinner"></span> Restarting…';
  buttons.forEach((btn) => btn && (btn.disabled = true));

  try {
    const response = await fetch("/api/tunnel/restart", { method: "POST" });
    if (!response.ok) {
      const text = await response.text();
      throw new Error(text.trim() || `Restart failed (${response.status})`);
    }
    const changed = await waitForTunnelChange(previousURL);
    if (!changed) {
      console.warn("Tunnel restart did not yield a new URL within timeout");
    }
  } catch (error) {
    alert(error?.message || "Restart failed");
  } finally {
    dom.restartButton.disabled = false;
    dom.restartButton.textContent = originalLabel;
    updateCopyButtonState();
  }
}

async function waitForTunnelChange(previousURL) {
  for (let attempt = 0; attempt < RESTART_RETRIES; attempt++) {
    await loadTunnelStatus({ silent: true, force: true });
    if (
      appState?.currentTunnelURL &&
      appState.currentTunnelURL !== previousURL
    ) {
      return true;
    }
    await delay(1000);
  }
  return false;
}

async function handleClearHistory() {
  if (!dom.clearHistoryButton) return;
  if (!window.confirm("Clear tunnel history? This cannot be undone.")) return;
  dom.clearHistoryButton.disabled = true;
  try {
    const response = await fetch("/api/tunnel/history/clear", {
      method: "POST",
    });
    if (!response.ok) {
      const text = await response.text();
      throw new Error(text.trim() || `Request failed (${response.status})`);
    }
    await loadTunnelHistory({ silent: true, force: true });
  } catch (error) {
    alert(error?.message || "Clear failed");
  } finally {
    dom.clearHistoryButton.disabled = false;
  }
}

function createTile(id, title, content) {
  const tile = document.createElement("section");
  tile.className = "tile";
  tile.dataset.tileId = id;
  tile.innerHTML = `<div class="tile-head"><strong>${title}</strong></div><div class="box"></div>`;
  const box = tile.querySelector(".box");
  if (box) box.textContent = content || "";
  return tile;
}

function createHeadButton(label, handler, className = "btn") {
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = className;
  btn.textContent = label;
  btn.addEventListener("click", handler);
  return btn;
}

function formatDuration(seconds) {
  const value = Number.isFinite(seconds) && seconds >= 0 ? seconds : 0;
  const h = Math.floor(value / 3600);
  const m = Math.floor((value % 3600) / 60);
  const s = Math.floor(value % 60);
  return `${h}h ${m}m ${s}s`;
}

function sanitizeTunnelURL(url) {
  if (!url || typeof url !== "string") return "";
  try {
    const parsed = new URL(url, window.location.origin);
    return parsed.origin.replace(/\/$/, "");
  } catch (error) {
    return "";
  }
}

function isPageActive() {
  if (!dom.page) return false;
  return dom.page.style.display !== "none";
}

function isRealtimeConnected() {
  return Boolean(rt && rt.connected);
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
