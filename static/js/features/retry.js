import {
  $,
  jget,
  showCopyFeedback,
  copyTextToClipboard,
  formatTimestamp,
  escapeHTML,
  extractItems,
} from "../lib/utils.js";
import { realtimeClient } from "../lib/realtime.js";

const REFRESH_INTERVAL = 60000;
const LOG_LIMIT = 200;
const RETRY_SCENARIO_HEADER = "x-retrylab-scenario";

let dom = {};
let appState = { currentTunnelURL: "" };
let rt = realtimeClient;

let scenariosCache = [];
let statsCache = [];
let loadedOnce = false;
let scenariosLoading = false;
let statsLoading = false;
let refreshTimer = null;
let tunnelSynced = false;

let logEntries = [];
let logFilterValue = "";
let logMessageTimer = null;
let scenarioIndex = new Map();

let unsubscribeStats = null;
let unsubscribeTunnel = null;
let unsubscribeLogs = null;

export function init(context = {}) {
  appState = context.state || appState;
  if (context.realtimeClient) {
    rt = context.realtimeClient;
  }

  dom = {
    page: $("#pageRetry"),
    scenarioList: document.getElementById("retryScenarioList"),
    statsContainer: document.getElementById("retryStats"),
    logList: document.getElementById("retryLogList"),
    logMessage: document.getElementById("retryLogMessage"),
    logFilter: document.getElementById("retryLogFilter"),
    logRefresh: document.getElementById("retryLogRefresh"),
    logClear: document.getElementById("retryLogClear"),
    logDownloadGroup: document.getElementById("retryDownloadGroup"),
  };

  bindEvents();
  subscribeRealtime();
}

export function activate() {
  ensureRetryLab();
}

function bindEvents() {
  if (dom.scenarioList && !dom.scenarioList.dataset.bound) {
    dom.scenarioList.addEventListener("click", handleScenarioListClick);
    dom.scenarioList.dataset.bound = "1";
  }

  if (dom.logFilter && !dom.logFilter.dataset.bound) {
    dom.logFilter.addEventListener("change", () => {
      logFilterValue = dom.logFilter.value || "";
      renderRetryLog();
    });
    dom.logFilter.dataset.bound = "1";
  }

  if (dom.logRefresh && !dom.logRefresh.dataset.bound) {
    dom.logRefresh.addEventListener("click", handleLogRefreshClick);
    dom.logRefresh.dataset.bound = "1";
  }

  if (dom.logClear && !dom.logClear.dataset.bound) {
    dom.logClear.addEventListener("click", handleLogClearClick);
    dom.logClear.dataset.bound = "1";
  }

  if (dom.logDownloadGroup && !dom.logDownloadGroup.dataset.bound) {
    dom.logDownloadGroup.addEventListener("click", handleLogDownloadClick);
    dom.logDownloadGroup.dataset.bound = "1";
  }
}

function subscribeRealtime() {
  if (!rt) return;

  if (!unsubscribeStats) {
    unsubscribeStats = rt.subscribe(
      "retry.stats",
      (payload) => {
        statsCache = extractItems(payload);
        renderRetryStats();
      },
      { snapshot: true }
    );
  }

  if (!unsubscribeTunnel) {
    unsubscribeTunnel = rt.subscribe(
      "tunnel.status",
      (status) => updateTunnelStatus(status),
      { snapshot: true }
    );
  }

  if (!unsubscribeLogs) {
    unsubscribeLogs = rt.subscribe(
      "log.event",
      (payload, meta = {}) => handleLogEvent(payload, meta),
      { snapshot: true }
    );
  }
}

function ensureRetryLab(force = false) {
  if (!dom.page) return;
  if (force) {
    loadedOnce = false;
  }

  if (!loadedOnce) {
    loadedOnce = true;
    showLoadingState();
    Promise.all([loadRetryScenarios(), loadRetryStats()]).catch(() => {});
  } else {
    loadRetryStats({ silent: true }).catch(() => {});
  }

  startRetryAutoRefresh();
}

function showLoadingState() {
  if (dom.scenarioList) {
    dom.scenarioList.innerHTML =
      '<p class="retry-loading">Loading scenarios…</p>';
  }
  if (dom.statsContainer) {
    dom.statsContainer.innerHTML =
      '<p class="retry-loading">Loading stats…</p>';
  }
}

async function loadRetryScenarios(options = {}) {
  if (scenariosLoading) return;
  scenariosLoading = true;
  const silent = Boolean(options.silent);

  try {
    await ensureTunnelBase();
    const data = await jget("/api/retrylab/scenarios");
    scenariosCache = extractItems(data);
    renderRetryScenarios();
  } catch (error) {
    if (!silent && dom.scenarioList) {
      dom.scenarioList.innerHTML = `<p class="retry-error">${escapeHTML(
        error?.message || "Failed to load scenarios."
      )}</p>`;
    }
    console.error("Failed to load retry scenarios", error);
  } finally {
    scenariosLoading = false;
  }
}

async function loadRetryStats(options = {}) {
  if (statsLoading) return;
  statsLoading = true;
  const silent = Boolean(options.silent);

  try {
    const data = await jget("/api/retrylab/stats");
    statsCache = extractItems(data);
    renderRetryStats();
  } catch (error) {
    if (!silent && dom.statsContainer) {
      dom.statsContainer.innerHTML = `<p class="retry-error">${escapeHTML(
        error?.message || "Failed to load stats."
      )}</p>`;
    }
    console.error("Failed to load retry stats", error);
  } finally {
    statsLoading = false;
  }
}

function renderRetryScenarios() {
  if (!dom.scenarioList) return;
  if (!scenariosCache.length) {
    dom.scenarioList.innerHTML =
      '<p class="retry-empty">No retry scenarios available.</p>';
    updateLogFilterOptions(true);
    return;
  }

  const fragment = document.createDocumentFragment();
  scenariosCache.forEach((item) => {
    if (!item) return;
    const card = document.createElement("article");
    card.className = "retry-card";
    if (item.id) card.dataset.retryId = item.id;

    const head = document.createElement("div");
    head.className = "retry-card-head";

    const title = document.createElement("h3");
    title.textContent = item.title || item.id || "Scenario";
    head.appendChild(title);

    card.appendChild(head);

    if (item.description) {
      const desc = document.createElement("p");
      desc.className = "retry-card-desc";
      desc.textContent = item.description;
      card.appendChild(desc);
    }

    const resolvedURL = resolveRetryLabURL(item.url || item.path || "");

    const actions = document.createElement("div");
    actions.className = "retry-card-actions";

    const open = document.createElement("a");
    open.className = "btn ghost";
    open.textContent = "Open";
    if (resolvedURL) {
      open.href = resolvedURL;
      open.target = "_blank";
      open.rel = "noopener";
    } else {
      open.href = "#";
      open.setAttribute("aria-disabled", "true");
      open.classList.add("is-disabled");
    }
    actions.appendChild(open);

    const copy = document.createElement("button");
    copy.type = "button";
    copy.className = "btn ghost";
    copy.dataset.copyLink = resolvedURL || "";
    copy.textContent = "Copy link";
    copy.disabled = !resolvedURL;
    actions.appendChild(copy);

    card.appendChild(actions);
    fragment.appendChild(card);
  });

  dom.scenarioList.replaceChildren(fragment);
  updateLogFilterOptions();
}

function updateLogFilterOptions(forceReset = false) {
  if (!dom.logFilter) return;
  scenarioIndex = new Map();
  const fragment = document.createDocumentFragment();
  const allOption = document.createElement("option");
  allOption.value = "";
  allOption.textContent = "All scenarios";
  fragment.appendChild(allOption);

  scenariosCache.forEach((item) => {
    if (!item || !item.id) return;
    scenarioIndex.set(item.id, item);
    const option = document.createElement("option");
    option.value = item.id;
    option.textContent = item.title || item.id;
    fragment.appendChild(option);
  });

  dom.logFilter.replaceChildren(fragment);

  const desired =
    !forceReset && logFilterValue && scenarioIndex.has(logFilterValue)
      ? logFilterValue
      : "";
  dom.logFilter.value = desired;
  logFilterValue = desired;
  renderRetryLog();
}

function renderRetryStats() {
  if (!dom.statsContainer) return;
  if (!statsCache.length) {
    dom.statsContainer.innerHTML =
      '<p class="retry-empty">No hits recorded yet.</p>';
    return;
  }

  const byId = new Map();
  scenariosCache.forEach((item) => {
    if (item && item.id) byId.set(item.id, item);
  });

  const fragment = document.createDocumentFragment();
  statsCache.forEach((stat) => {
    if (!stat) return;
    const row = document.createElement("div");
    row.className = "retry-stat";

    const meta = byId.get(stat.id) || {};

    const title = document.createElement("div");
    title.className = "retry-stat-title";
    title.textContent = meta.title || stat.id || "Scenario";
    row.appendChild(title);

    const value = document.createElement("div");
    value.className = "retry-stat-value";
    value.textContent = String(stat.total_hits || 0);
    row.appendChild(value);

    if (stat.unique_ips) {
      const ips = document.createElement("div");
      ips.className = "retry-stat-detail";
      ips.textContent = `${stat.unique_ips} unique IPs`;
      row.appendChild(ips);
    }

    if (stat.last_seen) {
      const last = document.createElement("div");
      last.className = "retry-stat-detail";
      last.textContent = `Last seen ${formatTimestamp(stat.last_seen)}`;
      row.appendChild(last);
    }

    fragment.appendChild(row);
  });

  dom.statsContainer.replaceChildren(fragment);
}

function renderRetryLog(options = {}) {
  if (!dom.logList) return;
  if (!logEntries.length) {
    dom.logList.innerHTML = '<p class="retry-log-empty">No retry hits yet.</p>';
    return;
  }

  const filtered = logEntries.filter((entry) => {
    if (!logFilterValue) return true;
    return entry.scenario === logFilterValue;
  });

  if (!filtered.length) {
    dom.logList.innerHTML =
      '<p class="retry-log-empty">No hits for the selected scenario.</p>';
    return;
  }

  const fragment = document.createDocumentFragment();
  filtered.forEach((entry) => {
    const node = renderRetryLogEntry(entry);
    if (options.highlightId && entry.id === options.highlightId) {
      node.classList.add("is-new");
      window.setTimeout(() => node.classList.remove("is-new"), 1200);
    }
    fragment.appendChild(node);
  });

  dom.logList.replaceChildren(fragment);
}

function renderRetryLogEntry(entry) {
  const node = document.createElement("article");
  node.className = "retry-log-entry";

  const scenario = getScenarioTitle(entry.scenario);
  const timestamp = formatTimestamp(entry.ts);
  const path = entry.query
    ? `${entry.path || ""}?${entry.query}`
    : entry.path || "";
  const statusBucket = entry.status > 0 ? Math.floor(entry.status / 100) : 0;
  const statusClass = statusBucket
    ? `retry-log-status status-${statusBucket}`
    : "retry-log-status";

  node.innerHTML = `
    <header>
      <span class="retry-log-time">${escapeHTML(timestamp)}</span>
      <span class="retry-log-scenario">${escapeHTML(scenario)}</span>
      <span class="${statusClass}">${escapeHTML(entry.status || "—")}</span>
    </header>
    <div class="retry-log-meta">
      <span class="retry-log-method">${escapeHTML(entry.method || "")}</span>
      <span>${escapeHTML(path)}</span>
      <span>${escapeHTML(entry.remoteIP || "—")}</span>
      <span>${escapeHTML(formatDuration(entry.durationMs))}</span>
    </div>
  `;

  return node;
}

function getScenarioTitle(id) {
  if (!id) return "Retry Lab";
  const meta = scenarioIndex.get(id);
  if (meta) return meta.title || meta.id || id;
  return id;
}

function handleScenarioListClick(event) {
  const target = event.target instanceof HTMLElement ? event.target : null;
  if (!target) return;
  const button = target.closest("[data-copy-link]");
  if (!button) return;
  const url = button.getAttribute("data-copy-link") || "";
  if (!url) return;

  copyTextToClipboard(url)
    .then((copied) => {
      if (copied) {
        showCopyFeedback(button, "Copy link", "Copied!");
      }
    })
    .catch(() => {
      showCopyFeedback(button, "Copy link", "Failed");
    });
}

function handleLogEvent(payload, meta = {}) {
  if (meta?.type === "snapshot") {
    const snapshot = Array.isArray(payload)
      ? payload
          .map((item) => normalizeLogEvent(item))
          .filter(Boolean)
          .sort((a, b) => b.ts - a.ts)
      : [];
    logEntries = snapshot.slice(0, LOG_LIMIT);
    renderRetryLog();
    return;
  }

  const entry = normalizeLogEvent(payload);
  if (!entry) return;
  insertLogEntry(entry);
  renderRetryLog({ highlightId: entry.id });
}

function normalizeLogEvent(raw) {
  if (!raw) return null;
  const classification = String(raw.class || raw.Class || "").toLowerCase();
  if (classification && classification !== "retrylab") {
    return null;
  }

  const headers = raw.headers || {};
  const headerKey = RETRY_SCENARIO_HEADER.toLowerCase();
  const scenarioRaw =
    headers[RETRY_SCENARIO_HEADER] ||
    headers[headerKey] ||
    headers[RETRY_SCENARIO_HEADER.toUpperCase()] ||
    "";
  const scenario = String(scenarioRaw || deriveScenarioFromPath(raw.path))
    .trim()
    .toLowerCase();
  if (!scenario) return null;

  const tsValue = raw.ts ? new Date(raw.ts).getTime() : Date.now();
  if (!Number.isFinite(tsValue)) return null;

  const method = String(raw.method || "").toUpperCase();
  const path = String(raw.path || "");
  const query = String(raw.query || "");
  const status = Number(raw.status || 0);
  const id =
    (raw.request_id || raw.requestId || "").trim() ||
    `${tsValue}-${method}-${path}-${raw.remote_ip || ""}-${query}`;

  return {
    id,
    ts: tsValue,
    scenario: scenario,
    method,
    path,
    query,
    status,
    remoteIP: String(raw.remote_ip || raw.remoteIp || ""),
    durationMs: Number(raw.duration_ms || raw.durationMs || raw.duration || 0),
  };
}

function deriveScenarioFromPath(path) {
  const match = /\/retrylab\/([^/?]+)/i.exec(String(path || ""));
  return match ? match[1] : "";
}

function insertLogEntry(entry) {
  if (!entry) return;
  const existingIndex = logEntries.findIndex((item) => item.id === entry.id);
  if (existingIndex !== -1) {
    logEntries.splice(existingIndex, 1);
  }
  logEntries.unshift(entry);
  logEntries.sort((a, b) => b.ts - a.ts);
  if (logEntries.length > LOG_LIMIT) {
    logEntries.length = LOG_LIMIT;
  }
}

function removeLogEntriesForScenario(scenarioId) {
  if (!scenarioId) {
    logEntries = [];
  } else {
    logEntries = logEntries.filter((entry) => entry.scenario !== scenarioId);
  }
  renderRetryLog();
}

function handleLogDownloadClick(event) {
  const target = event.target instanceof HTMLElement ? event.target : null;
  if (!target) return;
  const button = target.closest("[data-retry-download]");
  if (!button) return;

  if (!logEntries.length) {
    setLogMessage("No retry logs available yet.", "error", 2400);
    return;
  }

  const format = (
    button.getAttribute("data-retry-download") || "json"
  ).toLowerCase();
  const params = new URLSearchParams();
  params.set("class", "retrylab");
  params.set("format", format);
  params.set("limit", String(LOG_LIMIT));
  if (logFilterValue) {
    params.set("scenario", logFilterValue);
  }
  window.open(`/api/events/export?${params.toString()}`, "_blank", "noopener");
}

function handleLogRefreshClick(event) {
  if (event) event.preventDefault();
  if (rt && typeof rt.requestSnapshots === "function") {
    setLogMessage("Refreshing log…", "info", 2000);
    rt.requestSnapshots("log.event");
  } else {
    setLogMessage("Realtime connection unavailable.", "error");
  }
}

async function handleLogClearClick(event) {
  if (event) event.preventDefault();
  const scenarioId = logFilterValue;
  const body =
    scenarioId && scenarioId !== ""
      ? JSON.stringify({ ids: [scenarioId] })
      : "{}";
  setLogMessage("Clearing retry stats…", "info");
  try {
    const response = await fetch("/api/retrylab/reset", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body,
    });
    if (!response.ok) {
      const text = await response.text();
      throw new Error(text || `Request failed (${response.status})`);
    }
    const data = await response.json().catch(() => ({}));
    const items = extractItems(data);
    if (items.length) {
      statsCache = items;
      renderRetryStats();
    }
    removeLogEntriesForScenario(scenarioId);
    setLogMessage(
      scenarioId ? "Scenario reset." : "All scenarios reset.",
      "success",
      2400
    );
    if (rt && typeof rt.requestSnapshots === "function") {
      rt.requestSnapshots("retry.stats");
    }
  } catch (error) {
    console.error("Failed to reset retry lab", error);
    setLogMessage(error?.message || "Reset failed.", "error");
  }
}

function setLogMessage(text = "", variant = "info", ttlMs = 0) {
  if (!dom.logMessage) return;
  dom.logMessage.classList.remove("show", "is-info", "is-error", "is-success");
  if (logMessageTimer) {
    window.clearTimeout(logMessageTimer);
    logMessageTimer = null;
  }
  if (!text) {
    dom.logMessage.textContent = "";
    return;
  }
  const cls =
    variant === "error"
      ? "is-error"
      : variant === "success"
      ? "is-success"
      : "is-info";
  dom.logMessage.textContent = text;
  dom.logMessage.classList.add("show", cls);
  if (ttlMs > 0 && variant !== "error") {
    logMessageTimer = window.setTimeout(() => {
      setLogMessage("");
    }, ttlMs);
  }
}

function formatDuration(ms) {
  const value = Number(ms || 0);
  if (!Number.isFinite(value) || value <= 0) return "—";
  if (value < 1000) return `${Math.round(value)} ms`;
  const seconds = value / 1000;
  if (seconds < 60) return `${seconds.toFixed(1)} s`;
  const minutes = Math.floor(seconds / 60);
  const remainder = Math.round(seconds % 60);
  return `${minutes}m ${String(remainder).padStart(2, "0")}s`;
}

function startRetryAutoRefresh() {
  if (refreshTimer) return;
  refreshTimer = window.setInterval(() => {
    if (!isPageActive(dom.page)) return;
    if (rt && rt.connected) return;
    loadRetryStats({ silent: true }).catch(() => {});
  }, REFRESH_INTERVAL);
}

async function ensureTunnelBase() {
  if (tunnelSynced && appState?.currentTunnelURL) return;
  try {
    const status = await jget("/api/tunnel");
    updateTunnelStatus(status);
    tunnelSynced = true;
  } catch (error) {
    console.warn("Failed to sync tunnel status", error);
  }
}

function updateTunnelStatus(status) {
  if (!status || typeof status !== "object") return;
  const sanitized = sanitizeTunnelURL(status.url);
  if (appState) {
    appState.currentTunnelURL = sanitized || appState.currentTunnelURL || "";
    if (typeof status.active === "boolean") {
      appState.tunnelActive = status.active;
    }
  }
}

function resolveRetryLabURL(url) {
  const base = getTunnelBaseURL();
  if (!url) {
    return base ? `${base}/retrylab/retry-hint` : "";
  }
  try {
    const parsed = new URL(url, window.location.origin);
    if (!base) {
      return parsed.toString();
    }
    return `${base.replace(/\/$/, "")}${parsed.pathname}${parsed.search}`;
  } catch (error) {
    if (!base) return url;
    const cleanPath = url.startsWith("/") ? url : `/${url}`;
    return `${base}${cleanPath}`;
  }
}

function getTunnelBaseURL() {
  const current = appState?.currentTunnelURL;
  if (current) return current;
  if (window.location?.origin) {
    return window.location.origin.replace(/\/$/, "");
  }
  return "";
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

function isPageActive(element) {
  if (!element) return false;
  return element.style.display !== "none";
}
