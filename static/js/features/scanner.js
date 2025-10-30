import { $, jget, setLoading, extractItems } from "../lib/utils.js";
import { realtimeClient } from "../lib/realtime.js";
import { router } from "../lib/router.js";

const REFRESH_INTERVAL = 60000;
const RESULTS_LIMIT = 100;

let dom = {};
let rt = realtimeClient;

let jobsCache = [];
let resultsCache = [];
let loadedOnce = false;
let jobsLoading = false;
let resultsLoading = false;
let refreshTimer = null;

let unsubscribeJobs = null;
let unsubscribeResults = null;

export function init(context = {}) {
  if (context.realtimeClient) {
    rt = context.realtimeClient;
  }

  dom = {
    page: $("#pageScanner"),
    form: $("#scannerForm"),
    message: $("#scannerMessage"),
    jobsList: document.getElementById("scannerJobs"),
    resultsList: document.getElementById("scannerResults"),
    filter: document.getElementById("scannerFilter"),
    search: document.getElementById("scannerSearch"),
    activeToggle: document.getElementById("scannerActive"),
    intervalInput: document.getElementById("scannerInterval"),
    autoRefresh: document.getElementById("scannerAutoRefresh"),
    downloadJobs: document.getElementById("btnDownloadJobs"),
    clearJobs: document.getElementById("btnClearJobs"),
    downloadLogs: document.getElementById("btnDownloadLogs"),
    clearLogs: document.getElementById("btnClearLogs"),
  };

  bindEvents();
  subscribeRealtime();
}

export function activate() {
  ensureScannerPage();
}

function bindEvents() {
  if (dom.form && !dom.form.dataset.bound) {
    dom.form.addEventListener("submit", handleScannerJobSubmit);
    dom.form.dataset.bound = "1";
  }

  if (dom.jobsList && !dom.jobsList.dataset.bound) {
    dom.jobsList.addEventListener("click", handleJobsListClick);
    dom.jobsList.dataset.bound = "1";
  }

  if (dom.filter && !dom.filter.dataset.bound) {
    dom.filter.addEventListener("change", renderScannerResults);
    dom.filter.dataset.bound = "1";
  }

  if (dom.search && !dom.search.dataset.bound) {
    dom.search.addEventListener("input", renderScannerResults);
    dom.search.dataset.bound = "1";
  }

  if (dom.downloadJobs && !dom.downloadJobs.dataset.bound) {
    dom.downloadJobs.addEventListener("click", handleDownloadJobs);
    dom.downloadJobs.dataset.bound = "1";
  }

  if (dom.clearJobs && !dom.clearJobs.dataset.bound) {
    dom.clearJobs.addEventListener("click", handleClearJobs);
    dom.clearJobs.dataset.bound = "1";
  }

  if (dom.downloadLogs && !dom.downloadLogs.dataset.bound) {
    dom.downloadLogs.addEventListener("click", handleDownloadLogs);
    dom.downloadLogs.dataset.bound = "1";
  }

  if (dom.clearLogs && !dom.clearLogs.dataset.bound) {
    dom.clearLogs.addEventListener("click", handleClearLogs);
    dom.clearLogs.dataset.bound = "1";
  }

  if (dom.autoRefresh && !dom.autoRefresh.dataset.bound) {
    dom.autoRefresh.addEventListener("change", () => {
      if (dom.autoRefresh.checked) {
        startScannerAutoRefresh();
      } else {
        stopScannerAutoRefresh();
      }
    });
    dom.autoRefresh.dataset.bound = "1";
  }
}

function subscribeRealtime() {
  if (!rt) return;

  if (!unsubscribeJobs) {
    unsubscribeJobs = rt.subscribe(
      "scanner.jobs",
      (payload) => {
        jobsCache = extractItems(payload);
        renderScannerJobs();
        updateScannerFilterOptions();
      },
      { snapshot: true }
    );
  }

  if (!unsubscribeResults) {
    unsubscribeResults = rt.subscribe(
      "scanner.results",
      (payload) => {
        resultsCache = extractItems(payload);
        renderScannerResults();
      },
      { snapshot: true }
    );
  }
}

function ensureScannerPage(force = false) {
  if (!dom.page) return;
  if (force) {
    loadedOnce = false;
  }

  if (!loadedOnce) {
    loadedOnce = true;
    loadScannerJobs().catch(() => {});
    loadScannerResults().catch(() => {});
  } else {
    loadScannerJobs({ silent: true }).catch(() => {});
    loadScannerResults({ silent: true }).catch(() => {});
  }

  startScannerAutoRefresh();
}

async function loadScannerJobs(options = {}) {
  if (jobsLoading) return;
  jobsLoading = true;
  const silent = Boolean(options.silent);

  try {
    const data = await jget("/api/scanner/jobs");
    jobsCache = extractItems(data);
    renderScannerJobs();
    updateScannerFilterOptions();
    if (!silent) {
      setScannerMessage("");
    }
  } catch (error) {
    if (!silent) {
      const message = error?.message || "Failed to load scanner jobs.";
      setScannerMessage(message, "error");
    }
    console.error("Failed to load scanner jobs", error);
  } finally {
    jobsLoading = false;
  }
}

async function loadScannerResults(options = {}) {
  if (resultsLoading) return;
  resultsLoading = true;
  const silent = Boolean(options.silent);

  try {
    const data = await jget(`/api/scanner/results?n=${RESULTS_LIMIT}`);
    resultsCache = extractItems(data);
    renderScannerResults();
    if (!silent) {
      setScannerMessage("");
    }
  } catch (error) {
    if (!silent) {
      const message = error?.message || "Failed to load scanner results.";
      setScannerMessage(message, "error");
    }
    console.error("Failed to load scanner results", error);
  } finally {
    resultsLoading = false;
  }
}

async function handleScannerJobSubmit(event) {
  event.preventDefault();
  if (!dom.form) return;
  const submitBtn = dom.form.querySelector("button[type='submit']");

  try {
    setScannerMessage("");
    if (submitBtn) setLoading(submitBtn, true, "Scheduling…");
    const form = new FormData(dom.form);
    const getString = (key) => {
      const raw = form.get(key);
      return raw === null || raw === undefined ? "" : raw.toString();
    };
    const payload = {
      name: getString("name").trim(),
      method: getString("method").trim() || "GET",
      url: getString("url").trim(),
      interval_seconds: Number(getString("interval")) || 60,
      body: getString("body"),
      content_type: getString("contentType").trim(),
      active: dom.activeToggle ? Boolean(dom.activeToggle.checked) : true,
    };

    const response = await fetch("/api/scanner/jobs", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });

    if (!response.ok) {
      const text = await response.text();
      throw new Error(text.trim() || `Request failed (${response.status})`);
    }

    dom.form.reset();
    if (dom.intervalInput) dom.intervalInput.value = "60";
    if (dom.activeToggle) dom.activeToggle.checked = true;
    setScannerMessage("Scanner job scheduled.", "success");
    await loadScannerJobs({ silent: true });
    await loadScannerResults({ silent: true });
  } catch (error) {
    const message = error?.message || "Failed to schedule job.";
    setScannerMessage(message, "error");
  } finally {
    if (submitBtn) setLoading(submitBtn, false);
  }
}

async function handleJobsListClick(event) {
  const target = event.target instanceof HTMLElement ? event.target : null;
  if (!target) return;

  const btn = target.closest("[data-job-remove]");
  if (!btn) return;

  const id = btn.getAttribute("data-job-remove") || "";
  if (!id) return;
  const name = btn.getAttribute("data-job-name") || id;

  if (!window.confirm(`Delete scanner job "${name}"? This cannot be undone.`)) {
    return;
  }

  btn.disabled = true;
  try {
    const response = await fetch(
      `/api/scanner/jobs/${encodeURIComponent(id)}`,
      {
        method: "DELETE",
      }
    );
    if (!response.ok) {
      const text = await response.text();
      throw new Error(text.trim() || `Request failed (${response.status})`);
    }
    setScannerMessage(`Deleted job "${name}".`, "success");
    await loadScannerJobs({ silent: true });
    await loadScannerResults({ silent: true });
  } catch (error) {
    const message = error?.message || "Failed to delete job.";
    setScannerMessage(message, "error");
  } finally {
    btn.disabled = false;
  }
}

function handleDownloadJobs() {
  if (!jobsCache.length) {
    window.alert("No jobs to download.");
    return;
  }
  const json = JSON.stringify(jobsCache, null, 2);
  const blob = new Blob([json], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `scanner-jobs-${Date.now()}.json`;
  a.click();
  URL.revokeObjectURL(url);
}

async function handleClearJobs() {
  if (!jobsCache.length) {
    window.alert("No jobs to clear.");
    return;
  }
  if (
    !window.confirm(
      `Delete all ${jobsCache.length} scanner jobs? This cannot be undone.`
    )
  ) {
    return;
  }

  if (!dom.clearJobs) return;
  dom.clearJobs.disabled = true;

  try {
    for (const job of jobsCache) {
      if (!job || !job.id) continue;
      const response = await fetch(
        `/api/scanner/jobs/${encodeURIComponent(job.id)}`,
        { method: "DELETE" }
      );
      if (!response.ok) {
        throw new Error(`Failed to delete job ${job.id}`);
      }
    }
    setScannerMessage("All jobs cleared.", "success");
    await loadScannerJobs({ silent: true });
    await loadScannerResults({ silent: true });
  } catch (error) {
    const message = error?.message || "Failed to clear jobs.";
    setScannerMessage(message, "error");
  } finally {
    dom.clearJobs.disabled = false;
  }
}

function handleDownloadLogs() {
  if (!resultsCache.length) {
    window.alert("No logs to download.");
    return;
  }
  const json = JSON.stringify(resultsCache, null, 2);
  const blob = new Blob([json], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `scanner-logs-${Date.now()}.json`;
  a.click();
  URL.revokeObjectURL(url);
}

async function handleClearLogs() {
  if (!resultsCache.length) {
    window.alert("No logs to clear.");
    return;
  }

  if (!window.confirm("Clear all scanner logs? This cannot be undone.")) {
    return;
  }

  if (!dom.clearLogs) return;
  dom.clearLogs.disabled = true;

  try {
    const response = await fetch("/api/scanner/results", {
      method: "DELETE",
    });
    if (!response.ok) {
      const text = await response.text();
      throw new Error(text.trim() || `Request failed (${response.status})`);
    }
    setScannerMessage("Logs cleared.", "success");
    await loadScannerResults({ silent: true });
  } catch (error) {
    const message = error?.message || "Failed to clear logs.";
    setScannerMessage(message, "error");
  } finally {
    dom.clearLogs.disabled = false;
  }
}

function renderScannerJobs() {
  if (!dom.jobsList) return;
  if (!jobsCache.length) {
    dom.jobsList.innerHTML =
      '<p class="empty">No active scanner jobs yet. Add one to start outbound probes.</p>';
    return;
  }

  const fragment = document.createDocumentFragment();
  jobsCache.forEach((job) => {
    if (!job) return;
    const card = document.createElement("article");
    card.className = "scanner-card";
    if (job.id) card.dataset.jobId = job.id;

    const head = document.createElement("header");
    head.className = "scanner-card-head";

    const titleWrap = document.createElement("div");
    titleWrap.className = "scanner-card-title";
    const name = document.createElement("span");
    name.className = "scanner-card-name";
    name.textContent = job.name || job.url || job.id || "Job";
    const methodBadge = document.createElement("span");
    methodBadge.className = "badge";
    methodBadge.textContent = (job.method || "GET").toUpperCase();
    titleWrap.append(name, methodBadge);
    head.appendChild(titleWrap);

    const status = document.createElement("span");
    status.className = "scanner-card-status";
    if (job.active === false) {
      status.classList.add("is-paused");
      status.textContent = "Paused";
    } else {
      status.classList.add("is-active");
      status.textContent = "Active";
    }
    head.appendChild(status);

    card.appendChild(head);

    const url = document.createElement("p");
    url.className = "scanner-card-url";
    url.textContent = job.url || "";
    card.appendChild(url);

    const meta = document.createElement("dl");
    meta.className = "scanner-card-meta";

    const lastRunRaw = job.last_run;
    const lastRun =
      lastRunRaw && lastRunRaw !== "0001-01-01T00:00:00Z"
        ? formatTimestamp(lastRunRaw)
        : "Never";
    const lastStatus = job.last_error
      ? `Error: ${job.last_error}`
      : job.last_status || "No response yet";
    const interval = job.interval_seconds ? `${job.interval_seconds}s` : "—";

    appendMeta(meta, "Interval", interval);
    appendMeta(meta, "Last run", lastRun);
    appendMeta(meta, "Last status", lastStatus);
    card.appendChild(meta);

    const actions = document.createElement("div");
    actions.className = "scanner-card-actions";
    const remove = document.createElement("button");
    remove.className = "btn warn sm";
    remove.type = "button";
    remove.dataset.jobRemove = job.id || "";
    remove.dataset.jobName = job.name || job.url || job.id || "job";
    remove.textContent = "Delete";
    actions.appendChild(remove);
    card.appendChild(actions);

    fragment.appendChild(card);
  });

  dom.jobsList.replaceChildren(fragment);
}

function renderScannerResults() {
  if (!dom.resultsList) return;

  const selectedJobId = dom.filter ? dom.filter.value : "";
  const searchValue = dom.search ? dom.search.value.trim().toLowerCase() : "";

  let items = selectedJobId
    ? resultsCache.filter((item) => item?.job_id === selectedJobId)
    : resultsCache.slice();

  if (searchValue) {
    items = items.filter((item) => {
      const url = (item?.url || "").toLowerCase();
      const snippet = (item?.response_snippet || "").toLowerCase();
      const error = (item?.error || "").toLowerCase();
      return (
        url.includes(searchValue) ||
        snippet.includes(searchValue) ||
        error.includes(searchValue)
      );
    });
  }

  if (!items.length) {
    dom.resultsList.innerHTML =
      '<p class="empty">No recent responses recorded for this selection.</p>';
    return;
  }

  const fragment = document.createDocumentFragment();
  items
    .slice()
    .reverse()
    .forEach((item) => {
      if (!item) return;
      const card = document.createElement("article");
      card.className = "scanner-result";
      if (item.job_id) card.dataset.jobId = item.job_id;

      const head = document.createElement("header");
      head.className = "scanner-result-head";

      const titleWrap = document.createElement("div");
      titleWrap.className = "scanner-result-title";
      const name = document.createElement("span");
      name.className = "scanner-result-job";
      name.textContent = item.job_name || item.url || item.job_id || "Job";
      const methodBadge = document.createElement("span");
      methodBadge.className = "badge";
      methodBadge.textContent = (item.method || "GET").toUpperCase();
      titleWrap.append(name, methodBadge);
      head.appendChild(titleWrap);

      const statusWrap = document.createElement("div");
      statusWrap.className = "scanner-result-status";
      const status = document.createElement("span");
      if (item.error) {
        status.textContent = item.error;
        status.classList.add("is-error");
      } else if (typeof item.status === "number" && item.status > 0) {
        status.textContent = `HTTP ${item.status}`;
        status.classList.add(item.status >= 400 ? "is-error" : "is-ok");
      } else {
        status.textContent = "No response";
      }
      statusWrap.appendChild(status);
      if (item.ts) {
        const timeEl = document.createElement("time");
        timeEl.dateTime = item.ts;
        timeEl.textContent = formatTimestamp(item.ts);
        statusWrap.appendChild(timeEl);
      }
      head.appendChild(statusWrap);
      card.appendChild(head);

      const url = document.createElement("p");
      url.className = "scanner-result-url";
      url.textContent = item.url || "";
      card.appendChild(url);

      const info = document.createElement("div");
      info.className = "scanner-result-info";
      info.textContent = `Duration ${formatDuration(item.duration_ms)} · Job ${
        item.job_id || ""
      }`;
      card.appendChild(info);

      if (item.response_snippet) {
        const pre = document.createElement("pre");
        pre.className = "scanner-result-snippet";
        pre.textContent = item.response_snippet;
        card.appendChild(pre);
      }

      fragment.appendChild(card);
    });

  dom.resultsList.replaceChildren(fragment);
}

function updateScannerFilterOptions() {
  if (!dom.filter) return;
  const previous = dom.filter.value;
  const fragment = document.createDocumentFragment();

  const all = document.createElement("option");
  all.value = "";
  all.textContent = "All jobs";
  fragment.appendChild(all);

  jobsCache.forEach((job) => {
    if (!job) return;
    const opt = document.createElement("option");
    opt.value = job.id || "";
    opt.textContent = job.name || job.url || job.id || "Job";
    fragment.appendChild(opt);
  });

  dom.filter.replaceChildren(fragment);

  if (previous && jobsCache.some((job) => job?.id === previous)) {
    dom.filter.value = previous;
  } else {
    dom.filter.value = "";
  }
}

function startScannerAutoRefresh() {
  if (refreshTimer) return;
  if (dom.autoRefresh && !dom.autoRefresh.checked) return;
  refreshTimer = window.setInterval(() => {
    if (!isPageActive()) return;
    if (dom.autoRefresh && !dom.autoRefresh.checked) return;
    if (!isRealtimeConnected()) {
      loadScannerJobs({ silent: true }).catch(() => {});
      loadScannerResults({ silent: true }).catch(() => {});
    }
  }, REFRESH_INTERVAL);
}

function stopScannerAutoRefresh() {
  if (refreshTimer) {
    clearInterval(refreshTimer);
    refreshTimer = null;
  }
}

function setScannerMessage(message, type = "") {
  if (!dom.message) return;
  dom.message.textContent = message || "";
  dom.message.classList.remove("is-error", "is-success", "show");
  if (!message) return;
  dom.message.classList.add("show");
  if (type === "error") {
    dom.message.classList.add("is-error");
  } else if (type === "success") {
    dom.message.classList.add("is-success");
  }
}

function isPageActive() {
  if (typeof router?.getCurrentPage === "function") {
    return router.getCurrentPage() === "scanner";
  }
  return dom.page ? dom.page.style.display !== "none" : false;
}

function isRealtimeConnected() {
  return Boolean(rt && rt.connected);
}

function formatTimestamp(value) {
  if (!value) return "";
  const date = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const y = date.getFullYear();
  const m = String(date.getMonth() + 1).padStart(2, "0");
  const d = String(date.getDate()).padStart(2, "0");
  const h = String(date.getHours()).padStart(2, "0");
  const min = String(date.getMinutes()).padStart(2, "0");
  const s = String(date.getSeconds()).padStart(2, "0");
  return `${y}-${m}-${d} ${h}:${min}:${s}`;
}

function formatDuration(value) {
  const ms = Number(value);
  if (!Number.isFinite(ms) || ms < 0) return "—";
  if (ms < 1000) return `${Math.round(ms)} ms`;
  const seconds = ms / 1000;
  if (seconds < 60) return `${seconds.toFixed(1)} s`;
  const mins = Math.floor(seconds / 60);
  const remainder = Math.round(seconds % 60);
  return `${mins}m ${String(remainder).padStart(2, "0")}s`;
}

function appendMeta(container, label, value) {
  const dt = document.createElement("dt");
  dt.textContent = label;
  const dd = document.createElement("dd");
  dd.textContent = value;
  container.append(dt, dd);
}
