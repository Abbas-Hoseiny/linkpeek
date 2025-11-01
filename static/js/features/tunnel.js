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
const TUNNEL_EXPORT_LIMIT = 300;
const TUNNEL_LOG_LIMIT = 150;

let appState = { currentTunnelURL: "", tunnelActive: null };
let rt = realtimeClient;

let dom = {};
let statusCache = null;
let historyCache = [];
let loadedOnce = false;
let tunnelLogEntries = [];

let fastTimer = null;
let fallbackTimer = null;

let unsubscribeStatus = null;
let unsubscribeHistory = null;
let unsubscribeLogs = null;
let logMessageTimer = null;

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
    loadTunnelLogs();
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

  if (!dom.logsTile) {
    dom.logsTile = createTile("tunnel-logs", "Logs", "Loading…");
    const head = dom.logsTile.querySelector(".tile-head");
    dom.logsDownloadGroup = createTunnelDownloadGroup();
    dom.logsRefreshButton = createHeadButton(
      "Refresh",
      () => handleTunnelLogRefresh(),
      "btn ghost sm"
    );
    head.append(dom.logsDownloadGroup, dom.logsRefreshButton);
    if (!dom.logsDownloadGroup.dataset.bound) {
      dom.logsDownloadGroup.addEventListener(
        "click",
        handleTunnelDownloadClick
      );
      dom.logsDownloadGroup.dataset.bound = "1";
    }
    const box = dom.logsTile.querySelector(".box");
    if (box) {
      box.innerHTML = "";
      dom.logsMessage = document.createElement("div");
      dom.logsMessage.className = "tunnel-log-message payload-message";
      dom.logsMessage.setAttribute("role", "status");
      dom.logsMessage.setAttribute("aria-live", "polite");
      dom.logsMessage.hidden = true;
      box.appendChild(dom.logsMessage);

      dom.logsList = document.createElement("div");
      dom.logsList.className = "tunnel-log-list";
      dom.logsList.innerHTML = '<p class="tunnel-log-empty">Loading…</p>';
      box.appendChild(dom.logsList);
    }
    dom.grid.appendChild(dom.logsTile);
  }

  updateCopyButtonState();
  renderTunnelLogs();
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

  if (!unsubscribeLogs) {
    unsubscribeLogs = rt.subscribe(
      "log.event",
      (payload, meta = {}) => handleTunnelLogEvent(payload, meta),
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

async function loadTunnelLogs(options = {}) {
  if (!isPageActive() && !options.force) return;
  try {
    const params = new URLSearchParams();
    params.set("class", "tunnel");
    params.set("n", "200");
    const data = await jget(`/api/events?${params.toString()}`);
    const raw = extractItems(data);
    const items = raw.map(normalizeTunnelEvent).filter(Boolean);
    items.sort((a, b) => b.ts - a.ts);
    tunnelLogEntries = items.slice(0, TUNNEL_LOG_LIMIT);
    renderTunnelLogs();
    if (!options.silent) {
      setTunnelLogMessage("", "info");
    }
  } catch (error) {
    if (!options.silent) {
      const message = error?.message || "Failed to load tunnel logs.";
      setTunnelLogMessage(message, "error", 3600);
    }
  }
}

function handleTunnelLogEvent(payload, meta = {}) {
  if (meta?.type === "snapshot") {
    const snapshot = extractItems(payload)
      .map((item) => normalizeTunnelEvent(item))
      .filter(Boolean);
    snapshot.sort((a, b) => b.ts - a.ts);
    tunnelLogEntries = snapshot.slice(0, TUNNEL_LOG_LIMIT);
    renderTunnelLogs();
    return;
  }

  const entry = normalizeTunnelEvent(payload);
  if (!entry) return;
  const existingIndex = tunnelLogEntries.findIndex(
    (item) => item.id === entry.id
  );
  if (existingIndex !== -1) {
    tunnelLogEntries.splice(existingIndex, 1);
  }
  tunnelLogEntries.unshift(entry);
  tunnelLogEntries.sort((a, b) => b.ts - a.ts);
  if (tunnelLogEntries.length > TUNNEL_LOG_LIMIT) {
    tunnelLogEntries.length = TUNNEL_LOG_LIMIT;
  }
  renderTunnelLogs({ highlightId: entry.id });
}

function normalizeTunnelEvent(raw) {
  if (!raw) return null;
  const cls = String(raw.class || raw.Class || "").toLowerCase();
  const path = String(raw.path || "");
  const hostInfo = detectTunnelHost(raw);
  const isTunnelClass = cls === "tunnel";
  const relevantByHost = hostInfo.isTunnel && isTunnelRelevantPath(path);
  if (!isTunnelClass && !relevantByHost) return null;

  let tsValue = Date.now();
  if (raw.ts) {
    const parsed = new Date(raw.ts).getTime();
    if (Number.isFinite(parsed)) {
      tsValue = parsed;
    }
  }

  const method = String(raw.method || "").toUpperCase() || "GET";
  const rawQuery = String(raw.query || "");
  const query = rawQuery.startsWith("?") ? rawQuery.slice(1) : rawQuery;
  const status = Number(raw.status || 0);
  const durationMs = Number(
    raw.duration_ms || raw.durationMs || raw.duration || 0
  );
  const remoteIP = String(raw.remote_ip || raw.remoteIp || "");
  const host = hostInfo.host || "";
  const requestID = String(raw.request_id || raw.requestId || "").trim();
  const fallbackId = `${tsValue}-${method}-${path}-${remoteIP}-${query}-${host}`;
  const id = requestID || fallbackId;

  return {
    id,
    ts: tsValue,
    method,
    path,
    query,
    status,
    durationMs,
    remoteIP,
    host,
  };
}

function renderTunnelLogs(options = {}) {
  if (!dom.logsList) return;
  if (!tunnelLogEntries.length) {
    dom.logsList.innerHTML =
      '<p class="tunnel-log-empty">No tunnel events yet.</p>';
    return;
  }

  const fragment = document.createDocumentFragment();
  tunnelLogEntries.forEach((entry) => {
    const node = createTunnelLogEntry(entry);
    if (options.highlightId && entry.id === options.highlightId) {
      node.classList.add("is-new");
      window.setTimeout(() => node.classList.remove("is-new"), 1200);
    }
    fragment.appendChild(node);
  });

  dom.logsList.replaceChildren(fragment);
}

function createTunnelLogEntry(entry) {
  const node = document.createElement("article");
  node.className = "tunnel-log-entry";
  const timestamp = formatDateTime(entry.ts);
  const status = entry.status > 0 ? String(entry.status) : "—";
  const method = entry.method || "GET";
  const path = entry.query
    ? `${entry.path || ""}?${entry.query}`
    : entry.path || "";
  const remoteIP = entry.remoteIP || "—";
  const duration = formatLogDuration(entry.durationMs);
  const statusClass = getTunnelStatusClass(entry.status);
  const host = entry.host || "—";

  node.innerHTML = `
    <header>
      <span class="tunnel-log-time">${escapeHTML(timestamp)}</span>
      <span class="tunnel-log-method">${escapeHTML(method)}</span>
      <span class="tunnel-log-path">${escapeHTML(path)}</span>
      <span class="tunnel-log-status${
        statusClass ? ` ${statusClass}` : ""
      }">${escapeHTML(status)}</span>
    </header>
    <div class="tunnel-log-meta">
      <span class="tunnel-log-host" title="Tunnel host">${escapeHTML(
        host
      )}</span>
      <span class="tunnel-log-ip" title="Remote IP">IP ${escapeHTML(
        remoteIP
      )}</span>
      <span class="tunnel-log-duration" title="Request duration">${escapeHTML(
        duration
      )}</span>
    </div>
  `;
  return node;
}

function handleTunnelLogRefresh() {
  if (rt && typeof rt.requestSnapshots === "function") {
    setTunnelLogMessage("Refreshing tunnel logs…", "info", 2000);
    rt.requestSnapshots("log.event");
    return;
  }
  setTunnelLogMessage("Reloading tunnel logs…", "info", 2000);
  loadTunnelLogs({ silent: true, force: true });
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

function createTunnelDownloadGroup() {
  const group = document.createElement("div");
  group.className = "download-group";
  group.id = "tunnelDownloadGroup";
  group.setAttribute("role", "group");
  group.setAttribute("aria-label", "Download tunnel logs");

  const formats = [
    { key: "jsonl", label: "NDJSON" },
    { key: "json", label: "JSON" },
    { key: "pdf", label: "PDF" },
  ];

  formats.forEach(({ key, label }) => {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "btn ghost sm";
    btn.textContent = label;
    btn.setAttribute("data-tunnel-download", key);
    group.appendChild(btn);
  });

  return group;
}

function handleTunnelDownloadClick(event) {
  const target = event.target instanceof HTMLElement ? event.target : null;
  if (!target) return;
  const button = target.closest("[data-tunnel-download]");
  if (!button) return;

  const format = (
    button.getAttribute("data-tunnel-download") || "json"
  ).toLowerCase();
  const params = new URLSearchParams();
  params.set("class", "tunnel");
  params.set("format", format);
  params.set("limit", String(TUNNEL_EXPORT_LIMIT));
  setTunnelLogMessage("Preparing export…", "info", 2000);
  window.open(`/api/events/export?${params.toString()}`, "_blank", "noopener");
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

function isTunnelRelevantPath(path) {
  if (!path || typeof path !== "string") return true;
  let normalized = path.trim().toLowerCase();
  if (normalized === "" || normalized === "/") return true;
  while (normalized.endsWith("/") && normalized.length > 1) {
    normalized = normalized.slice(0, -1);
  }
  return normalized === "/alpha";
}

function formatLogDuration(durationMs) {
  const value = Number.isFinite(durationMs) ? durationMs : 0;
  if (value <= 0) return "—";
  if (value < 1000) return `${Math.round(value)} ms`;
  if (value < 60000) return `${(value / 1000).toFixed(1)} s`;
  const minutes = value / 60000;
  return `${minutes.toFixed(1)} min`;
}

function getTunnelStatusClass(status) {
  const value = Number(status);
  if (!Number.isFinite(value) || value <= 0) return "";
  if (value >= 500) return "is-error";
  if (value >= 400) return "is-warn";
  if (value >= 300) return "is-redirect";
  if (value >= 200) return "is-success";
  return "is-info";
}

function detectTunnelHost(raw = {}) {
  const headers = normalizeHeaderMap(raw.headers || raw.Headers);
  const storedHost = extractHostFromValue(headers["tunnel-host"]);
  const hostHeader = extractHostFromValue(headers.host || raw.host || raw.Host);
  const forwardedHost = extractHostFromValue(headers["x-forwarded-host"]);
  const originHost = extractHostFromValue(
    headers.origin || raw.origin || raw.Origin
  );
  const refererHost = extractHostFromValue(
    headers.referer || raw.referer || raw.Referer
  );

  const candidates = [
    storedHost,
    hostHeader,
    forwardedHost,
    originHost,
    refererHost,
  ].filter(Boolean);

  const tunnelCandidate =
    candidates.find((value) =>
      value.toLowerCase().endsWith("trycloudflare.com")
    ) || "";

  return { host: tunnelCandidate, isTunnel: Boolean(tunnelCandidate) };
}

function normalizeHeaderMap(headers) {
  if (!headers || typeof headers !== "object") return {};
  const map = {};
  Object.entries(headers).forEach(([key, value]) => {
    if (!key) return;
    map[key.toLowerCase()] = value;
  });
  return map;
}

function extractHostFromValue(value) {
  if (!value) return "";
  const raw = String(value).trim();
  if (!raw) return "";
  const first = raw.split(",")[0].trim();
  if (!first) return "";
  if (first.includes("://")) {
    try {
      const url = new URL(first);
      return url.host || "";
    } catch (error) {
      // fall through if parsing fails
    }
  }
  const cleaned = first.replace(/^https?:\/\//i, "");
  const slashIndex = cleaned.indexOf("/");
  if (slashIndex !== -1) {
    return cleaned.slice(0, slashIndex).trim();
  }
  return cleaned.trim();
}

function setTunnelLogMessage(text, variant = "info", ttl = 0) {
  if (!dom.logsMessage) return;
  if (logMessageTimer) {
    window.clearTimeout(logMessageTimer);
    logMessageTimer = null;
  }

  const message = String(text || "");
  dom.logsMessage.classList.remove("show", "is-info", "is-error", "is-success");

  if (!message.trim()) {
    dom.logsMessage.textContent = "";
    dom.logsMessage.hidden = true;
    return;
  }

  dom.logsMessage.hidden = false;
  dom.logsMessage.textContent = message;
  const styleClass =
    variant === "error"
      ? "is-error"
      : variant === "success"
      ? "is-success"
      : "is-info";
  dom.logsMessage.classList.add("show", styleClass);

  if (ttl > 0 && variant !== "error") {
    logMessageTimer = window.setTimeout(() => {
      setTunnelLogMessage("");
    }, ttl);
  }
}
