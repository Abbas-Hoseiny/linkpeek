// Home page feature module migrated from the legacy script
import {
  $,
  jget,
  formatTimestamp,
  formatBytes,
  escapeHTML,
  escapeAttr,
  copyTextToClipboard,
  showCopyFeedback,
} from "../lib/utils.js";
import { realtimeClient } from "../lib/realtime.js";

const INITIAL_LOAD_DELAY = 800;
const REFRESH_INTERVAL = 60000;
const SEARCH_DEBOUNCE = 250;
const LOG_HTTP_LIMIT = 100;
const LOG_RETENTION = 200;

let appState = { currentTunnelURL: "" };
let rt = realtimeClient;

let dom = {};
let selectedIP = "";
let loadingIPs = false;
let searchTimer = null;
let logEntries = [];
let logsLoading = false;
let logsLoadedOnce = false;
let logAutoScroll = true;
let unsubscribeLogs = null;

const timers = {
  initialLoad: null,
  ipRefresh: null,
  countRefresh: null,
  metricsRefresh: null,
  detailRefresh: null,
  logRefresh: null,
};

export function init(context = {}) {
  appState = context.state || appState;
  if (context.realtimeClient) {
    rt = context.realtimeClient;
  }

  dom = {
    page: $("#pageHome"),
    ipList: document.getElementById("ipList"),
    ipSearch: document.getElementById("ipSearch"),
    ipSort: document.getElementById("ipSort"),
    ipCount: document.getElementById("ipCount"),
    grid: document.getElementById("grid"),
    classBar: document.getElementById("classBar"),
    topActions: document.getElementById("topActions"),
    metricCPU: document.getElementById("metricCPU"),
    metricRAM: document.getElementById("metricRAM"),
    loadButton: document.getElementById("btnLoadIPs"),
    logPanel: document.getElementById("homeLogPanel"),
    logList: document.getElementById("homeLogList"),
    logMessage: document.getElementById("homeLogMessage"),
    logCount: document.getElementById("logCount"),
    logRefresh: document.getElementById("btnRefreshLogs"),
    logAutoScrollToggle: document.getElementById("logAutoScroll"),
  };

  bindEvents();
  bindLogEvents();
  subscribeRealtime();
  ensureDetailLayout();
  scheduleInitialLoads();
  startAutoRefresh();
}

export function activate() {
  ensureDetailLayout();
  ensureHomeData();
}

function bindEvents() {
  if (dom.loadButton && !dom.loadButton.dataset.bound) {
    dom.loadButton.addEventListener("click", () => loadIPs());
    dom.loadButton.dataset.bound = "1";
  }

  if (dom.ipSearch && !dom.ipSearch.dataset.bound) {
    dom.ipSearch.addEventListener("input", () => scheduleSearch());
    dom.ipSearch.addEventListener("keydown", (event) => {
      if (event.key === "Enter") {
        event.preventDefault();
        scheduleSearch(0);
      }
    });
    dom.ipSearch.dataset.bound = "1";
  }

  if (dom.ipSort && !dom.ipSort.dataset.bound) {
    dom.ipSort.addEventListener("change", () => loadIPs());
    dom.ipSort.dataset.bound = "1";
  }

  if (dom.ipList && !dom.ipList.dataset.bound) {
    dom.ipList.addEventListener("click", handleIpListClick);
    dom.ipList.dataset.bound = "1";
  }

  if (dom.topActions && !dom.topActions.dataset.bound) {
    dom.topActions.addEventListener("click", handleTopActionsClick);
    dom.topActions.dataset.bound = "1";
  }

  if (dom.page && !dom.page.dataset.bound) {
    dom.page.addEventListener("page:deactivate", (event) => {
      if (event?.detail?.route !== "home") return;
      if (unsubscribeLogs) {
        unsubscribeLogs();
        unsubscribeLogs = null;
      }
    });
    dom.page.addEventListener("page:activate", (event) => {
      if (event?.detail?.route !== "home") return;
      ensureLogFeed();
      subscribeRealtime();
    });
    dom.page.dataset.bound = "1";
  }
}

function bindLogEvents() {
  if (dom.logRefresh && !dom.logRefresh.dataset.bound) {
    dom.logRefresh.addEventListener("click", () =>
      loadLogFeed({ force: true })
    );
    dom.logRefresh.dataset.bound = "1";
  }

  if (dom.logAutoScrollToggle && !dom.logAutoScrollToggle.dataset.bound) {
    dom.logAutoScrollToggle.addEventListener("change", (event) => {
      logAutoScroll = Boolean(event.target?.checked);
      if (logAutoScroll) {
        scrollLogListToTop();
      }
    });
    dom.logAutoScrollToggle.dataset.bound = "1";
  }

  if (dom.logList && !dom.logList.dataset.bound) {
    dom.logList.addEventListener("click", handleLogListClick);
    dom.logList.dataset.bound = "1";
  }
}

function subscribeRealtime() {
  if (!rt || unsubscribeLogs) return;
  unsubscribeLogs = rt.subscribe(
    "log.event",
    (payload, meta = {}) => handleRealtimeLog(payload, meta),
    { snapshot: true }
  );
}

function handleRealtimeLog(payload, meta = {}) {
  if (meta?.type === "snapshot") {
    const snapshotEntries = normalizeLogEvents(
      Array.isArray(payload) ? payload : []
    );
    if (!snapshotEntries.length) {
      return;
    }
    const ids = new Set(snapshotEntries.map((item) => item.id));
    logEntries = snapshotEntries.concat(
      logEntries.filter((item) => !ids.has(item.id))
    );
    if (logEntries.length > LOG_RETENTION) {
      logEntries.length = LOG_RETENTION;
    }
    logsLoadedOnce = logEntries.length > 0;
    renderLogFeed();
    return;
  }

  const entry = normalizeLogEvent(payload);
  if (!entry) return;
  insertLogEntry(entry);
  logsLoadedOnce = true;
  renderLogFeed({ highlightLatest: true });
}

function ensureLogFeed(force = false) {
  if (!dom.logList) return;
  if (!logsLoadedOnce || force) {
    loadLogFeed({ force });
  }
}

async function loadLogFeed(options = {}) {
  if (logsLoading && !options.force) return;
  logsLoading = true;
  try {
    if (!options.silent) {
      setLogMessage("Loading logs…", "info");
    }
    const limit = options.limit || LOG_HTTP_LIMIT;
    const data = await jget(`/api/events?n=${encodeURIComponent(limit)}`);
    const items = Array.isArray(data)
      ? data
      : Array.isArray(data?.items)
      ? data.items
      : [];
    logEntries = normalizeLogEvents(items);
    logsLoadedOnce = logEntries.length > 0;
    renderLogFeed();
    setLogMessage("");
  } catch (error) {
    const message = error?.message || "Failed to load logs.";
    if (!options.silent) {
      setLogMessage(message, "error");
    }
  } finally {
    logsLoading = false;
  }
}

function insertLogEntry(entry) {
  if (!entry) return;
  const existingIndex = logEntries.findIndex((item) => item.id === entry.id);
  if (existingIndex !== -1) {
    logEntries.splice(existingIndex, 1);
  }
  logEntries.unshift(entry);
  logEntries.sort((a, b) => b.ts - a.ts);
  if (logEntries.length > LOG_RETENTION) {
    logEntries = logEntries.slice(0, LOG_RETENTION);
  }
}

function normalizeLogEvents(events) {
  const mapped = [];
  const seen = new Set();
  (events || []).forEach((evt) => {
    const normalized = normalizeLogEvent(evt);
    if (!normalized) return;
    if (seen.has(normalized.id)) return;
    seen.add(normalized.id);
    mapped.push(normalized);
  });
  mapped.sort((a, b) => b.ts - a.ts);
  if (mapped.length > LOG_RETENTION) {
    return mapped.slice(0, LOG_RETENTION);
  }
  return mapped;
}

function normalizeLogEvent(raw) {
  if (!raw) return null;
  const tsValue = raw.ts ? new Date(raw.ts).getTime() : Date.now();
  if (!Number.isFinite(tsValue)) return null;
  const method = String(raw.method || "").toUpperCase();
  const path = String(raw.path || "/");
  const query = String(raw.query || "");
  const status = Number(raw.status || 0);
  const requestID = String(raw.request_id || raw.requestID || "").trim();
  const id =
    requestID || `${tsValue}-${method}-${path}-${raw.remote_ip || ""}-${query}`;
  return {
    id,
    ts: tsValue,
    method,
    path,
    query,
    status,
    duration: Number(raw.duration_ms || raw.duration || 0),
    remoteIP: String(raw.remote_ip || raw.remoteIp || ""),
    ua: String(raw.ua || raw.user_agent || ""),
    className: String(raw.class || ""),
    referer: String(raw.referer || ""),
    origin: String(raw.origin || ""),
    headers: raw.headers || {},
    raw,
  };
}

function renderLogFeed(options = {}) {
  if (!dom.logList) return;
  if (!logEntries.length) {
    dom.logList.innerHTML = '<p class="log-empty">No recent activity.</p>';
    updateLogCount();
    return;
  }

  const frag = document.createDocumentFragment();
  logEntries.forEach((entry, index) => {
    const node = renderLogEntry(entry);
    if (options.highlightLatest && index === 0) {
      node.classList.add("is-new");
      window.setTimeout(() => node.classList.remove("is-new"), 1200);
    }
    frag.appendChild(node);
  });
  dom.logList.replaceChildren(frag);
  updateLogCount();
  if (logAutoScroll) {
    scrollLogListToTop();
  }
}

function renderLogEntry(entry) {
  const node = document.createElement("article");
  node.className = "log-entry";
  node.dataset.logId = entry.id;

  const timestamp = formatTimestamp(entry.ts) || "";
  const statusBucket = entry.status > 0 ? Math.floor(entry.status / 100) : 0;
  const statusText = entry.status > 0 ? `HTTP ${entry.status}` : "—";
  const statusClass = statusBucket ? `status status-${statusBucket}` : "status";
  const tag = classifyRequest(entry.raw);
  const tagHtml = tag
    ? ` <span class="mini-tag ${escapeAttr(tag.cls)}" title="${escapeAttr(
        tag.label
      )}">${escapeHTML(tag.label)}</span>`
    : "";

  const queryHtml = entry.query
    ? `<span class="log-entry-query">?${escapeHTML(entry.query)}</span>`
    : "";

  const refHtml = entry.referer
    ? `<span>Referrer: ${escapeHTML(entry.referer)}</span>`
    : "";
  const originHtml = entry.origin
    ? `<span>Origin: ${escapeHTML(entry.origin)}</span>`
    : "";

  node.innerHTML = `
    <header>
      <span class="log-entry-time">${escapeHTML(timestamp)}</span>
      <span class="method method-${escapeAttr(entry.method)}">${escapeHTML(
    entry.method || "GET"
  )}</span>
      <span class="${escapeAttr(statusClass)}">${escapeHTML(statusText)}</span>
      <span class="log-entry-path">${escapeHTML(entry.path)}</span>
      ${queryHtml}
      ${tagHtml}
      <div class="log-entry-actions">
        <button type="button" class="btn ghost sm" data-log-copy="${escapeAttr(
          entry.id
        )}">Copy JSON</button>
      </div>
    </header>
    <div class="log-entry-meta">
      <span class="log-entry-ip">${escapeHTML(entry.remoteIP || "—")}</span>
      <span class="log-entry-duration">${escapeHTML(
        formatDurationMs(entry.duration)
      )}</span>
      ${refHtml}
      ${originHtml}
    </div>
    <div class="log-entry-ua">${escapeHTML(entry.ua || "")}</div>
  `;

  return node;
}

function updateLogCount() {
  if (!dom.logCount) return;
  dom.logCount.textContent = `Logs: ${logEntries.length}`;
}

function setLogMessage(text = "", variant = "info") {
  if (!dom.logMessage) return;
  dom.logMessage.classList.remove("show", "is-info", "is-error", "is-success");
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
}

function scrollLogListToTop() {
  if (!dom.logList) return;
  dom.logList.scrollTop = 0;
}

async function handleLogListClick(event) {
  const target = event.target instanceof HTMLElement ? event.target : null;
  if (!target) return;
  const copyBtn = target.closest("[data-log-copy]");
  if (!copyBtn) return;

  const id = copyBtn.getAttribute("data-log-copy");
  if (!id) return;
  const entry = logEntries.find((item) => item.id === id);
  if (!entry) return;
  try {
    await copyTextToClipboard(JSON.stringify(entry.raw, null, 2));
    showCopyFeedback(copyBtn, "Copy JSON", "Copied!");
  } catch (error) {
    console.error("Failed to copy log entry", error);
    showCopyFeedback(copyBtn, "Copy JSON", "Failed");
  }
}

function formatDurationMs(ms) {
  const value = Number(ms || 0);
  if (!Number.isFinite(value) || value <= 0) return "—";
  if (value < 1000) return `${Math.round(value)} ms`;
  const seconds = value / 1000;
  if (seconds < 60) return `${seconds.toFixed(1)} s`;
  const minutes = Math.floor(seconds / 60);
  const remainder = Math.round(seconds % 60);
  return `${minutes}m ${String(remainder).padStart(2, "0")}s`;
}

function scheduleInitialLoads() {
  if (!timers.initialLoad) {
    timers.initialLoad = window.setTimeout(() => {
      loadIPs();
      refreshIPCount();
      refreshMetrics();
      loadLogFeed();
      if (selectedIP) {
        refreshSelectedIP();
        refreshSizes();
      }
    }, INITIAL_LOAD_DELAY);
  }
}

function startAutoRefresh() {
  if (!timers.ipRefresh) {
    timers.ipRefresh = window.setInterval(() => {
      if (!isHomeActive() || isRealtimeConnected()) return;
      loadIPs({ silent: true });
    }, REFRESH_INTERVAL);
  }

  if (!timers.countRefresh) {
    timers.countRefresh = window.setInterval(() => {
      if (!isHomeActive() || isRealtimeConnected()) return;
      refreshIPCount();
    }, REFRESH_INTERVAL);
  }

  if (!timers.metricsRefresh) {
    timers.metricsRefresh = window.setInterval(() => {
      if (!isHomeActive() || isRealtimeConnected()) return;
      refreshMetrics();
    }, REFRESH_INTERVAL);
  }

  if (!timers.detailRefresh) {
    timers.detailRefresh = window.setInterval(() => {
      if (!isHomeActive() || isRealtimeConnected()) return;
      if (selectedIP) {
        refreshSelectedIP({ silent: true });
        refreshSizes();
      }
    }, REFRESH_INTERVAL);
  }

  if (!timers.logRefresh) {
    timers.logRefresh = window.setInterval(() => {
      if (!isHomeActive() || isRealtimeConnected()) return;
      loadLogFeed({ silent: true });
    }, REFRESH_INTERVAL);
  }

  if (!timers.logRefresh) {
    timers.logRefresh = window.setInterval(() => {
      if (!isHomeActive() || isRealtimeConnected()) return;
      loadLogFeed({ silent: true });
    }, REFRESH_INTERVAL);
  }
}

function ensureHomeData() {
  loadIPs();
  refreshIPCount();
  refreshMetrics();
  ensureLogFeed();
  if (selectedIP) {
    refreshSelectedIP();
    refreshSizes();
  }
}

function scheduleSearch(delay = SEARCH_DEBOUNCE) {
  if (searchTimer) window.clearTimeout(searchTimer);
  searchTimer = window.setTimeout(() => {
    loadIPs();
  }, Math.max(0, delay));
}

async function loadIPs(options = {}) {
  if (!dom.ipList) return;
  if (!isHomeActive() && !options.force) return;
  if (loadingIPs) return;
  loadingIPs = true;
  try {
    const params = new URLSearchParams();
    const term = dom.ipSearch ? dom.ipSearch.value.trim() : "";
    if (term) params.set("search", term);
    const sort = dom.ipSort ? dom.ipSort.value : "";
    if (sort) params.set("sort", sort);
    const query = params.toString();
    const data = await jget(query ? `/api/ips?${query}` : "/api/ips");
    const items = Array.isArray(data?.items) ? data.items : [];
    renderIpList(items);
  } catch (error) {
    console.error("Failed to load IPs", error);
    if (!options.silent) {
      dom.ipList.innerHTML = '<div class="empty">Unable to load IPs.</div>';
    }
  } finally {
    loadingIPs = false;
  }
}

function renderIpList(items) {
  if (!dom.ipList) return;
  if (!items.length) {
    dom.ipList.innerHTML = '<div class="empty">No IPs tracked yet.</div>';
    return;
  }

  const fragment = document.createDocumentFragment();
  items.forEach((row) => {
    if (!row) return;
    const div = document.createElement("div");
    div.className = "row";
    div.dataset.ip = row.ip || "";
    const lastSeen = row.last_seen ? formatTimestamp(row.last_seen) : "";
    div.innerHTML = `
      <span class="ip">${escapeHTML(row.ip || "")}</span>
      <span class="count-badge" title="requests">${Number(
        row.req_count || 0
      )}</span>
      <span class="time" title="last seen">${escapeHTML(lastSeen)}</span>
    `;
    if (row.ip && row.ip === selectedIP) {
      div.classList.add("is-selected");
    }
    fragment.appendChild(div);
  });

  dom.ipList.replaceChildren(fragment);
}

function handleIpListClick(event) {
  const target = event.target instanceof HTMLElement ? event.target : null;
  if (!target) return;
  const row = target.closest(".row[data-ip]");
  if (!row) return;
  const ip = row.getAttribute("data-ip") || "";
  if (!ip) return;
  openIP(ip);
}

function openIP(ipCidr) {
  ensureDetailLayout();
  const ip = ipCidr.replace(/\/(\d+)$/, "");
  selectedIP = ip;
  markSelectedRow();
  refreshSelectedIP();
  refreshSizes();
}

function markSelectedRow() {
  if (!dom.ipList) return;
  const rows = dom.ipList.querySelectorAll(".row[data-ip]");
  rows.forEach((row) => {
    const ip = row.getAttribute("data-ip");
    row.classList.toggle("is-selected", Boolean(ip && ip === selectedIP));
  });
}

async function refreshSelectedIP(options = {}) {
  if (!selectedIP) return;
  ensureDetailLayout();
  const ip = selectedIP;

  try {
    const summary = await jget(
      `/api/ip/${encodeURIComponent(ip)}/summary?bucket=hour&range=48h`
    );
    renderIpSummary(ip, summary);
  } catch (error) {
    const message = error?.message || String(error);
    setTile("ip-ua", `IP ${ip} · User Agents`, message);
    setTile("ip-summary", `IP ${ip} · Summary`, message);
  }

  try {
    const requests = await jget(
      `/api/ip/${encodeURIComponent(ip)}/requests?limit=50&offset=0`
    );
    renderIpRequests(ip, requests?.items || []);
  } catch (error) {
    const message = error?.message || String(error);
    setTile("ip-req", `IP ${ip} · Requests`, message);
  }

  try {
    const geo = await jget(`/api/ip/${encodeURIComponent(ip)}/geo?ttl=7d`);
    setTile("ip-geo", `IP ${ip} · Geo`, JSON.stringify(geo, null, 2));
  } catch (error) {
    const message = error?.message || String(error);
    setTile("ip-geo", `IP ${ip} · Geo`, `Geo unavailable: ${message}`);
  }
}

function renderIpSummary(ip, summary) {
  const uaList = Array.isArray(summary?.ua) ? summary.ua : [];
  if (!uaList.length) {
    setTile("ip-ua", `IP ${ip} · User Agents`, "No user agents");
  } else {
    const html = `
      <div class="log-list">
        ${uaList
          .map((entry) => {
            const ua = entry?.ua || "";
            const count = Number(entry?.count || 0);
            return `
              <div class="log-row">
                <span class="count-badge">${count}</span>
                <span class="ua">${escapeHTML(ua || "(empty)")}</span>
              </div>`;
          })
          .join("")}
      </div>`;
    setTileHTML("ip-ua", `IP ${ip} · User Agents`, html);
  }

  let title = `IP ${ip} · Summary`;
  if (summary && summary.classes) {
    const parts = buildClassBadges(summary.classes);
    if (parts.length) {
      title += ` ${parts.join(" ")}`;
      if (dom.classBar) {
        dom.classBar.innerHTML = parts.join(" ");
      }
    }
  } else if (dom.classBar) {
    dom.classBar.textContent = "";
  }

  setTile("ip-summary", title, JSON.stringify(summary, null, 2));
}

function renderIpRequests(ip, items) {
  if (!items.length) {
    setTileHTML(
      "ip-req",
      `IP ${ip} · Requests`,
      '<div class="log-list">No requests</div>'
    );
    return;
  }

  const rows = items
    .map((req) => {
      const timestamp = formatTimestamp(req?.ts) || "";
      const method = String(req?.method || "").toUpperCase();
      const status = Number(req?.status || 0);
      const path = String(req?.path || "/");
      const query = String(req?.query || "");
      const tag = classifyRequest(req);
      const queryHtml = query
        ? `<span class="query">?${escapeHTML(query)}</span>`
        : "";
      const tagHtml = tag
        ? ` <span class="mini-tag ${tag.cls}" title="${escapeAttr(
            tag.label
          )}">${escapeHTML(tag.label)}</span>`
        : "";

      return `
        <div class="log-row">
          <span class="time">${escapeHTML(timestamp)}</span>${tagHtml}
          <span class="method method-${escapeHTML(method)}">${escapeHTML(
        method
      )}</span>
          <span class="status status-${Math.floor(status / 100)}">${
        status || ""
      }</span>
          <span class="path">${escapeHTML(path)}</span>
          ${queryHtml}
        </div>`;
    })
    .join("");

  setTileHTML(
    "ip-req",
    `IP ${ip} · Requests`,
    `<div class="log-list">${rows}</div>`
  );
}

async function refreshSizes() {
  if (!selectedIP) return;
  try {
    const data = await jget(`/api/ip/${encodeURIComponent(selectedIP)}/sizes`);
    const value = data?.all?.bytes_estimate;
    if (dom.topActions) {
      const el = dom.topActions.querySelector("#size-all");
      if (el) {
        el.textContent = value != null ? `~${formatBytes(value)}` : "—";
      }
    }
  } catch (error) {
    console.warn("Failed to refresh IP sizes", error);
  }
}

async function refreshIPCount() {
  if (!dom.ipCount) return;
  if (!isHomeActive()) return;
  try {
    const data = await jget("/api/ips/count");
    dom.ipCount.textContent = `IPs: ${data?.count ?? "—"}`;
  } catch (error) {
    console.warn("Failed to refresh IP count", error);
  }
}

async function refreshMetrics() {
  try {
    const metrics = await jget("/api/metrics");
    if (dom.metricCPU) {
      const cpu = Math.max(0, Math.min(100, Number(metrics?.cpu_percent || 0)));
      dom.metricCPU.textContent = `CPU: ${cpu.toFixed(1)}%`;
    }
    if (dom.metricRAM) {
      dom.metricRAM.textContent = `RAM: ${formatBytes(
        metrics?.rss_bytes || 0
      )}`;
    }
  } catch (error) {
    console.warn("Failed to refresh metrics", error);
  }
}

function handleTopActionsClick(event) {
  const target = event.target instanceof HTMLElement ? event.target : null;
  if (!target) return;
  const actionEl = target.closest("[data-cat]");
  if (!actionEl || !selectedIP) return;
  const category = actionEl.getAttribute("data-cat");
  if (!category) return;

  if (target.hasAttribute("data-dl")) {
    const format = target.getAttribute("data-dl") || "json";
    const url = `/api/ip/${encodeURIComponent(
      selectedIP
    )}/export?cat=${encodeURIComponent(category)}&fmt=${encodeURIComponent(
      format
    )}`;
    window.open(url, "_blank", "noopener");
  } else if (target.hasAttribute("data-del")) {
    if (
      !window.confirm(
        `Delete ${category} for ${selectedIP}? This cannot be undone.`
      )
    )
      return;
    fetch(
      `/api/ip/${encodeURIComponent(
        selectedIP
      )}/delete?cat=${encodeURIComponent(category)}`,
      {
        method: "POST",
      }
    )
      .then((response) => {
        if (!response.ok) {
          return response.text().then((text) => {
            throw new Error(
              text.trim() || `Request failed (${response.status})`
            );
          });
        }
        refreshSizes();
        refreshSelectedIP();
        loadIPs({ silent: true });
        refreshIPCount();
      })
      .catch((error) => {
        alert(error?.message || "Delete failed");
      });
  }
}

function ensureDetailLayout() {
  if (!dom.grid || dom.grid.dataset.init === "1") return;
  dom.grid.innerHTML = "";
  const createTile = (id, title) => {
    const tile = document.createElement("section");
    tile.className = "tile";
    tile.dataset.tileId = id;
    tile.innerHTML = `<div class=\"tile-head\"><strong>${title}</strong></div><div class=\"box\"></div>`;
    return tile;
  };
  dom.grid.appendChild(createTile("ip-geo", "Geo"));
  dom.grid.appendChild(createTile("ip-ua", "User Agents"));
  dom.grid.appendChild(createTile("ip-req", "Requests"));
  dom.grid.appendChild(createTile("ip-summary", "Summary"));
  dom.grid.dataset.init = "1";
}

function setTile(id, title, content) {
  if (!dom.grid) return;
  const tile = dom.grid.querySelector(`[data-tile-id="${id}"]`);
  if (!tile) return;
  const head = tile.querySelector(".tile-head strong");
  if (head && typeof title === "string") {
    if (title.includes("<")) head.innerHTML = title;
    else head.textContent = title;
  }
  const box = tile.querySelector(".box");
  if (box) {
    box.textContent = content || "";
  }
}

function setTileHTML(id, title, html) {
  if (!dom.grid) return;
  const tile = dom.grid.querySelector(`[data-tile-id="${id}"]`);
  if (!tile) return;
  const head = tile.querySelector(".tile-head strong");
  if (head) head.textContent = title;
  const box = tile.querySelector(".box");
  if (box) {
    box.innerHTML = html || "";
  }
}

function buildClassBadges(classes) {
  const badges = [];
  const addBadge = (label, value, cls) => {
    const count = Number(value || 0);
    badges.push(
      `<span class="badge ${cls}" title="${escapeAttr(label)}">${escapeHTML(
        label
      )}: ${count}</span>`
    );
  };
  addBadge("Real-User Click", classes.click_user, "real");
  addBadge("Real-User Preview", classes.real_user_preview, "preview");
  addBadge("PreviewBot", classes.preview_bot, "bot");
  addBadge("Scanner", classes.scanner, "scanner");
  return badges;
}

function classifyRequest(request) {
  const path = String(request?.path || "");
  const query = String(request?.query || "");
  const status = Number(request?.status || 0);
  const ua = String(request?.ua || "");
  const botUA =
    /(WhatsApp|Telegram|facebookexternalhit|Slackbot|Twitterbot|LinkedInBot|Discordbot|SkypeUriPreview|Pinterestbot|bot|crawler)/i;

  if (path === "/alpha/js" && status === 204) {
    if (query.includes("evt=click"))
      return { label: "Real-User Click", cls: "real" };
    if (
      query.includes("evt=boot") ||
      query.includes("evt=visible") ||
      query.includes("evt=pagehide") ||
      query.includes("evt=challenge")
    ) {
      return { label: "Real-User Preview", cls: "preview" };
    }
    return { label: "Real-User Preview", cls: "preview" };
  }
  if (path === "/alpha/pixel" && status === 202)
    return { label: "Scanner", cls: "scanner" };
  if (path === "/" || path === "/static/favicon.svg")
    return { label: "Scanner", cls: "scanner" };
  if (botUA.test(ua)) return { label: "PreviewBot", cls: "bot" };
  if (path === "/alpha" && status === 200)
    return { label: "Real-Host System", cls: "host" };
  return null;
}

function isHomeActive() {
  if (!dom.page) return false;
  return dom.page.style.display !== "none";
}

function isRealtimeConnected() {
  return Boolean(rt && rt.connected);
}
