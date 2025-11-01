import {
  $,
  jget,
  showCopyFeedback,
  setLoading,
  formatTimestamp,
  formatBytes,
  copyTextToClipboard,
  escapeHTML,
  escapeAttr,
  extractItems,
} from "../lib/utils.js";
import { realtimeClient } from "../lib/realtime.js";
import { router } from "../lib/router.js";

const CAPTURE_REQUEST_LIMIT = 200;
const CAPTURE_ACTIVITY_LIMIT = 50;
const REFRESH_INTERVAL = 60000;

let dom = {};
let appState = { currentTunnelURL: "" };
let rt = realtimeClient;

let hooksCache = [];
let activityCache = [];
let requestsCache = new Map();
let selectedHookId = "";

let hooksLoading = false;
let requestsLoading = false;
let activityLoading = false;

let homeLoadedOnce = false;
let pageLoadedOnce = false;

let homeTimer = null;
let pageTimer = null;

let unsubscribeHooks = null;
let unsubscribeActivity = null;
let unsubscribeRequests = null;

export function init(context = {}) {
  appState = context.state || appState;
  if (context.realtimeClient) {
    rt = context.realtimeClient;
  }

  dom = {
    homePage: $("#pageHome"),
    homePanel: $("#captureHomePanel"),
    homeList: document.getElementById("captureHomeList"),
    homeSelect: document.getElementById("captureHomeSelect"),
    homeRefresh: document.getElementById("btnRefreshCaptureHome"),
    page: $("#pageCapture"),
    list: document.getElementById("captureList"),
    form: document.getElementById("captureForm"),
    formLabel: document.getElementById("captureLabel"),
    message: document.getElementById("captureMessage"),
    select: document.getElementById("captureSelect"),
    logList: document.getElementById("captureLogList"),
    refresh: document.getElementById("btnCaptureRefresh"),
    downloadGroup: document.getElementById("captureDownloadGroup"),
    clear: document.getElementById("btnCaptureClear"),
  };

  bindEvents();
  subscribeRealtime();
  ensureCaptureHome();
}

export function activate() {
  ensureCapturePage();
}

function bindEvents() {
  if (dom.form && !dom.form.dataset.bound) {
    dom.form.addEventListener("submit", handleCreateHook, { passive: false });
    dom.form.dataset.bound = "1";
  }

  if (dom.list && !dom.list.dataset.bound) {
    dom.list.addEventListener("click", handleHookListClick);
    dom.list.dataset.bound = "1";
  }

  if (dom.select && !dom.select.dataset.bound) {
    dom.select.addEventListener("change", () => {
      setSelectedHook(dom.select.value, { forceFetch: true });
    });
    dom.select.dataset.bound = "1";
  }

  if (dom.refresh && !dom.refresh.dataset.bound) {
    dom.refresh.addEventListener("click", () => {
      if (!selectedHookId) {
        setCaptureMessage("Select a capture link first.", "error");
        return;
      }
      loadCaptureRequests(selectedHookId, { silent: true, force: true });
    });
    dom.refresh.dataset.bound = "1";
  }

  if (dom.downloadGroup && !dom.downloadGroup.dataset.bound) {
    dom.downloadGroup.addEventListener("click", handleDownloadClick);
    dom.downloadGroup.dataset.bound = "1";
  }

  if (dom.clear && !dom.clear.dataset.bound) {
    dom.clear.addEventListener("click", () => clearSelectedHook());
    dom.clear.dataset.bound = "1";
  }

  if (dom.homeRefresh && !dom.homeRefresh.dataset.bound) {
    dom.homeRefresh.addEventListener("click", () =>
      loadCaptureActivity({ silent: true, force: true })
    );
    dom.homeRefresh.dataset.bound = "1";
  }

  if (dom.homeSelect && !dom.homeSelect.dataset.bound) {
    dom.homeSelect.addEventListener("change", renderCaptureHome);
    dom.homeSelect.dataset.bound = "1";
  }
}

function handleDownloadClick(event) {
  const target = event.target instanceof HTMLElement ? event.target : null;
  if (!target) return;
  const button = target.closest("[data-capture-download]");
  if (!button) return;
  if (!selectedHookId) {
    setCaptureMessage("Select a capture link first.", "error");
    return;
  }
  const format = (
    button.getAttribute("data-capture-download") || "jsonl"
  ).toLowerCase();
  const href = `/api/hooks/${encodeURIComponent(
    selectedHookId
  )}/download?format=${encodeURIComponent(format)}`;
  window.open(href, "_blank", "noopener");
}

function subscribeRealtime() {
  if (!rt) return;

  if (!unsubscribeHooks) {
    unsubscribeHooks = rt.subscribe(
      "capture.hooks",
      (payload) => {
        const items = extractItems(payload);
        updateHooks(items, { silent: true });
      },
      { snapshot: true }
    );
  }

  if (!unsubscribeActivity) {
    unsubscribeActivity = rt.subscribe(
      "capture.activity",
      (payload) => {
        const items = extractItems(payload);
        activityCache = Array.isArray(items) ? items.slice() : [];
        renderCaptureHome();
      },
      { snapshot: true }
    );
  }
}

function ensureCaptureHome(force = false) {
  if (!dom.homePanel) return;
  if (!homeLoadedOnce || force) {
    loadCaptureActivity({ force, silent: force });
    homeLoadedOnce = true;
  }
  startCaptureHomeAutoRefresh();
}

function ensureCapturePage(force = false) {
  if (!dom.page) return;
  if (force) pageLoadedOnce = false;
  if (!pageLoadedOnce) {
    loadCaptureHooks({ silent: false });
    pageLoadedOnce = true;
  } else {
    loadCaptureHooks({ silent: true });
  }
  startCapturePageAutoRefresh();
}

async function loadCaptureHooks(options = {}) {
  if (hooksLoading) return;
  hooksLoading = true;
  const silent = Boolean(options.silent);

  try {
    await syncTunnelURL();
    const data = await jget("/api/hooks");
    const items = extractItems(data);
    updateHooks(items, { silent });
    if (!silent) {
      setCaptureMessage("", "info");
    }
  } catch (error) {
    console.error("Failed to load capture links", error);
    if (!silent) {
      const message = error?.message || "Failed to load capture links.";
      setCaptureMessage(message, "error");
    }
  } finally {
    hooksLoading = false;
  }
}

async function loadCaptureRequests(hookId, options = {}) {
  if (!hookId) {
    requestsCache.delete(hookId);
    renderCaptureLog();
    return;
  }
  if (requestsLoading && !options.force) return;
  requestsLoading = true;
  const silent = Boolean(options.silent);

  try {
    const data = await jget(
      `/api/hooks/${encodeURIComponent(
        hookId
      )}/requests?limit=${CAPTURE_REQUEST_LIMIT}`
    );
    const items = extractItems(data);
    requestsCache.set(hookId, items);
    if (hookId === selectedHookId) {
      renderCaptureLog();
    }
  } catch (error) {
    if (!silent) {
      const message = error?.message || "Failed to load request log.";
      setCaptureMessage(message, "error");
    }
  } finally {
    requestsLoading = false;
  }
}

async function loadCaptureActivity(options = {}) {
  if (!dom.homePanel) return;
  if (activityLoading && !options.force) return;
  activityLoading = true;
  const silent = Boolean(options.silent);

  try {
    const data = await jget(
      `/api/hooks/activity?limit=${CAPTURE_ACTIVITY_LIMIT}`
    );
    activityCache = extractItems(data);
    renderCaptureHome();
  } catch (error) {
    if (!silent && dom.homeList) {
      dom.homeList.innerHTML = `<p class="capture-home-error">${escapeHTML(
        error?.message || "Failed to load capture activity."
      )}</p>`;
    }
  } finally {
    activityLoading = false;
  }
}

function updateHooks(items, options = {}) {
  hooksCache = Array.isArray(items) ? items.slice() : [];
  const previous = selectedHookId;
  if (
    !selectedHookId ||
    !hooksCache.some((hook) => hook?.id === selectedHookId)
  ) {
    selectedHookId = hooksCache.length ? hooksCache[0]?.id || "" : "";
  }

  renderCaptureSelectors();
  renderCaptureHooks();

  if (selectedHookId) {
    subscribeToHookRequests(selectedHookId);
    if (
      !requestsCache.has(selectedHookId) ||
      !options.silent ||
      previous !== selectedHookId
    ) {
      loadCaptureRequests(selectedHookId, { silent: options.silent });
    } else {
      renderCaptureLog();
    }
  } else {
    unsubscribeFromHookRequests();
    renderCaptureLog();
  }
}

async function handleCreateHook(event) {
  event.preventDefault();
  const submit = dom.form?.querySelector('button[type="submit"]');
  const label = dom.formLabel ? dom.formLabel.value.trim() : "";
  try {
    if (submit) setLoading(submit, true, "Creating…");
    const response = await fetch("/api/hooks", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ label }),
    });
    if (!response.ok) {
      const text = await response.text();
      throw new Error(text.trim() || `Request failed (${response.status})`);
    }
    setCaptureMessage("Capture link created.", "success");
    dom.form?.reset();
    loadCaptureHooks({ silent: true });
  } catch (error) {
    const message = error?.message || "Failed to create capture link.";
    setCaptureMessage(message, "error");
  } finally {
    if (submit) setLoading(submit, false);
  }
}

function handleHookListClick(event) {
  const target = event.target instanceof HTMLElement ? event.target : null;
  if (!target) return;

  const copyBtn = target.closest("[data-copy-url]");
  if (copyBtn) {
    const url = copyBtn.getAttribute("data-copy-url") || "";
    if (!url) return;
    copyTextToClipboard(url)
      .then((copied) => {
        if (copied) {
          showCopyFeedback(copyBtn, "Copy URL", "Copied!");
        }
      })
      .catch(() => {
        showCopyFeedback(copyBtn, "Copy URL", "Failed");
      });
    return;
  }

  const selectBtn = target.closest("[data-select-hook]");
  if (selectBtn) {
    const id = selectBtn.getAttribute("data-select-hook") || "";
    setSelectedHook(id, { forceFetch: true });
    router.navigate("capture");
    return;
  }

  const clearBtn = target.closest("[data-clear-hook]");
  if (clearBtn) {
    const id = clearBtn.getAttribute("data-clear-hook") || "";
    if (!id) return;
    if (!window.confirm("Clear all stored requests for this link?")) return;
    clearBtn.disabled = true;
    fetch(`/api/hooks/${encodeURIComponent(id)}/clear`, { method: "POST" })
      .then((response) => {
        if (!response.ok) {
          return response.text().then((text) => {
            throw new Error(
              text.trim() || `Request failed (${response.status})`
            );
          });
        }
        requestsCache.delete(id);
        if (selectedHookId === id) {
          renderCaptureLog();
        }
        setCaptureMessage("Requests cleared.", "success");
      })
      .catch((error) => {
        const message = error?.message || "Failed to clear requests.";
        setCaptureMessage(message, "error");
      })
      .finally(() => {
        clearBtn.disabled = false;
      });
    return;
  }

  const deleteBtn = target.closest("[data-delete-hook]");
  if (deleteBtn) {
    const id = deleteBtn.getAttribute("data-delete-hook") || "";
    if (!id) return;
    if (!window.confirm("Delete this capture link? This cannot be undone."))
      return;
    deleteBtn.disabled = true;
    fetch(`/api/hooks/${encodeURIComponent(id)}`, { method: "DELETE" })
      .then((response) => {
        if (!response.ok) {
          return response.text().then((text) => {
            throw new Error(
              text.trim() || `Request failed (${response.status})`
            );
          });
        }
        requestsCache.delete(id);
        if (selectedHookId === id) {
          setSelectedHook("", { forceFetch: false });
        }
        setCaptureMessage("Capture link deleted.", "success");
        loadCaptureHooks({ silent: true });
      })
      .catch((error) => {
        const message = error?.message || "Failed to delete link.";
        setCaptureMessage(message, "error");
      })
      .finally(() => {
        deleteBtn.disabled = false;
      });
  }
}

function setSelectedHook(id, options = {}) {
  const nextId = id || "";
  if (nextId === selectedHookId && !options.forceFetch) {
    renderCaptureSelectors();
    renderCaptureLog();
    return;
  }

  const previous = selectedHookId;
  selectedHookId = nextId;
  renderCaptureSelectors();

  if (selectedHookId) {
    subscribeToHookRequests(selectedHookId);
    if (
      !requestsCache.has(selectedHookId) ||
      options.forceFetch ||
      previous !== selectedHookId
    ) {
      loadCaptureRequests(selectedHookId, { silent: options.silent });
    } else {
      renderCaptureLog();
    }
  } else {
    unsubscribeFromHookRequests();
    renderCaptureLog();
  }
}

function clearSelectedHook() {
  if (!selectedHookId) {
    setCaptureMessage("Select a capture link first.", "error");
    return;
  }
  if (!window.confirm("Clear all stored requests for this link?")) return;
  if (!dom.clear) return;
  dom.clear.disabled = true;
  fetch(`/api/hooks/${encodeURIComponent(selectedHookId)}/clear`, {
    method: "POST",
  })
    .then((response) => {
      if (!response.ok) {
        return response.text().then((text) => {
          throw new Error(text.trim() || `Request failed (${response.status})`);
        });
      }
      requestsCache.delete(selectedHookId);
      renderCaptureLog();
      setCaptureMessage("Requests cleared.", "success");
    })
    .catch((error) => {
      const message = error?.message || "Failed to clear requests.";
      setCaptureMessage(message, "error");
    })
    .finally(() => {
      dom.clear.disabled = false;
    });
}

function renderCaptureSelectors() {
  const options = hooksCache.map((hook) => ({
    value: hook?.id || "",
    label: hook?.label || hook?.id || "Unnamed link",
  }));

  if (dom.select) {
    const previous = dom.select.value;
    dom.select.innerHTML = "";
    options.forEach((opt) => {
      const option = document.createElement("option");
      option.value = opt.value;
      option.textContent = opt.label;
      if (opt.value && opt.value === selectedHookId) {
        option.selected = true;
      }
      dom.select.appendChild(option);
    });
    if (!selectedHookId && options.length) {
      dom.select.value = options[0].value;
    } else if (!options.length) {
      dom.select.value = "";
    } else if (previous) {
      dom.select.value = previous;
    }
  }

  if (dom.homeSelect) {
    const previous = dom.homeSelect.value;
    dom.homeSelect.innerHTML = "";
    const allOption = document.createElement("option");
    allOption.value = "";
    allOption.textContent = "All links";
    dom.homeSelect.appendChild(allOption);
    options.forEach((opt) => {
      const option = document.createElement("option");
      option.value = opt.value;
      option.textContent = opt.label;
      dom.homeSelect.appendChild(option);
    });
    dom.homeSelect.value = previous;
  }
}

function renderCaptureHooks() {
  if (!dom.list) return;
  if (!hooksCache.length) {
    dom.list.innerHTML = '<p class="capture-empty">No capture links yet.</p>';
    return;
  }

  const fragment = document.createDocumentFragment();
  hooksCache.forEach((hook) => {
    if (!hook) return;
    const card = document.createElement("article");
    card.className = "capture-card";
    card.dataset.captureId = hook.id || "";
    const url = resolveCaptureURL(hook?.token);

    card.innerHTML = `
      <div class="capture-card-head">
        <div>
          <h3>${escapeHTML(hook?.label || hook?.id || "Capture link")}</h3>
          <p class="capture-meta">Total hits: ${Number(
            hook?.total_requests || 0
          )}</p>
        </div>
        <button class="btn ghost sm" data-select-hook="${escapeAttr(
          hook?.id
        )}">View log</button>
      </div>
      <div class="capture-card-body">
        <code>${escapeHTML(url)}</code>
      </div>
      <div class="capture-card-actions">
        <button class="btn ghost sm" data-copy-url="${escapeAttr(
          url
        )}">Copy URL</button>
        <a class="btn ghost sm" href="${escapeAttr(
          url
        )}" target="_blank" rel="noopener">Open</a>
        <button class="btn ghost sm warn" data-clear-hook="${escapeAttr(
          hook?.id
        )}">Clear</button>
        <button class="btn ghost sm warn" data-delete-hook="${escapeAttr(
          hook?.id
        )}">Delete</button>
      </div>
    `;
    fragment.appendChild(card);
  });

  dom.list.replaceChildren(fragment);
}

function renderCaptureLog() {
  if (!dom.logList) return;
  if (!selectedHookId) {
    dom.logList.innerHTML =
      '<p class="capture-empty">Select a capture link to view logs.</p>';
    return;
  }
  const entries = requestsCache.get(selectedHookId) || [];
  if (!entries.length) {
    dom.logList.innerHTML =
      '<p class="capture-empty">No requests recorded yet.</p>';
    return;
  }

  const fragment = document.createDocumentFragment();
  entries
    .slice()
    .reverse()
    .forEach((req) => {
      const row = document.createElement("article");
      row.className = "capture-log-row";
      const query = req?.query ? `?${req.query}` : "";
      const hookLabel = req?.hook_label || req?.hook_id || "";
      const headers = req?.headers ? Object.entries(req.headers) : [];
      const headersBlock = headers.length
        ? `<details class="capture-headers"><summary>Headers (${
            headers.length
          })</summary><pre>${escapeHTML(
            headers.map(([k, v]) => `${k}: ${v}`).join("\n")
          )}</pre></details>`
        : "";
      const preview = req?.body_preview
        ? `<pre class="capture-body" data-encoding="${escapeAttr(
            req?.body_encoding || "utf-8"
          )}">${escapeHTML(req.body_preview)}</pre>`
        : "";

      row.innerHTML = `
        <header>
          <span class="capture-method">${escapeHTML(
            req?.method || "GET"
          )}</span>
          <strong class="capture-path">${escapeHTML(
            req?.path || "/"
          )}${escapeHTML(query)}</strong>
          <span class="capture-time">${escapeHTML(
            formatTimestamp(req?.created_at) || ""
          )}</span>
        </header>
        <div class="capture-meta-row">
          <span>Hook: ${escapeHTML(hookLabel)}</span>
          <span>IP: ${escapeHTML(req?.remote_ip || "—")}</span>
          <span>Body: ${formatBytes(req?.body_size || 0)}</span>
        </div>
        ${preview}
        ${headersBlock}
      `;
      fragment.appendChild(row);
    });

  dom.logList.replaceChildren(fragment);
}

function renderCaptureHome() {
  if (!dom.homeList) return;
  if (!activityCache.length) {
    dom.homeList.innerHTML =
      '<p class="capture-empty">No capture activity yet.</p>';
    return;
  }
  const filter = dom.homeSelect ? dom.homeSelect.value : "";
  const fragment = document.createDocumentFragment();
  activityCache
    .slice()
    .reverse()
    .forEach((req) => {
      if (filter && req?.hook_id !== filter) return;
      const row = document.createElement("article");
      row.className = "capture-log-row";
      const query = req?.query ? `?${req.query}` : "";
      const hookLabel = req?.hook_label || req?.hook_id || "";
      row.innerHTML = `
        <header>
          <span class="capture-method">${escapeHTML(
            req?.method || "GET"
          )}</span>
          <strong class="capture-path">${escapeHTML(
            req?.path || "/"
          )}${escapeHTML(query)}</strong>
          <span class="capture-time">${escapeHTML(
            formatTimestamp(req?.created_at) || ""
          )}</span>
        </header>
        <div class="capture-meta-row">
          <span>Hook: ${escapeHTML(hookLabel)}</span>
          <span>IP: ${escapeHTML(req?.remote_ip || "—")}</span>
          <span>${formatBytes(req?.body_size || 0)}</span>
        </div>
      `;
      fragment.appendChild(row);
    });

  if (!fragment.children.length) {
    dom.homeList.innerHTML =
      '<p class="capture-empty">No matching activity.</p>';
    return;
  }

  dom.homeList.replaceChildren(fragment);
}

function setCaptureMessage(text = "", variant = "info") {
  if (!dom.message) return;
  dom.message.classList.remove("show", "is-info", "is-success", "is-error");
  if (!text) {
    dom.message.textContent = "";
    return;
  }
  const type = ["info", "success", "error"].includes(variant)
    ? variant
    : "info";
  dom.message.textContent = text;
  dom.message.classList.add("show", `is-${type}`);
}

function subscribeToHookRequests(hookId) {
  if (!rt || !hookId) return;
  unsubscribeFromHookRequests();
  unsubscribeRequests = rt.subscribe(
    `capture.requests::${hookId}`,
    (payload) => {
      const items = extractItems(payload);
      requestsCache.set(hookId, items);
      if (hookId === selectedHookId) {
        renderCaptureLog();
      }
    },
    { snapshot: true }
  );
}

function unsubscribeFromHookRequests() {
  if (typeof unsubscribeRequests === "function") {
    unsubscribeRequests();
  }
  unsubscribeRequests = null;
}

function startCaptureHomeAutoRefresh() {
  if (homeTimer || !dom.homePanel) return;
  homeTimer = window.setInterval(() => {
    if (!isPageActive(dom.homePage)) return;
    if (rt && rt.connected) return;
    loadCaptureActivity({ silent: true });
  }, REFRESH_INTERVAL);
}

function startCapturePageAutoRefresh() {
  if (pageTimer || !dom.page) return;
  pageTimer = window.setInterval(() => {
    if (!isPageActive(dom.page)) return;
    if (rt && rt.connected) return;
    loadCaptureHooks({ silent: true });
    if (selectedHookId) {
      loadCaptureRequests(selectedHookId, { silent: true });
    }
  }, REFRESH_INTERVAL);
}

async function syncTunnelURL() {
  try {
    const status = await jget("/api/tunnel");
    const sanitized = sanitizeTunnelURL(status?.url);
    if (appState) {
      appState.currentTunnelURL = sanitized || appState.currentTunnelURL || "";
    }
  } catch (error) {
    console.warn("Failed to sync tunnel status", error);
  }
}

function resolveCaptureURL(token) {
  if (!token) return "";
  const base = getTunnelBaseURL();
  if (!base) return `/capture/${token}`;
  return `${base}/capture/${token}`;
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
