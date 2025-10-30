import {
  $,
  jget,
  showCopyFeedback,
  setLoading,
  formatTimestamp,
  formatBytes,
  copyTextToClipboard,
} from "../lib/utils.js";
import { realtimeClient } from "../lib/realtime.js";

const SPECTRUM_WINDOW = 10;
const SUCCESS_MESSAGE_TTL = 2500;
const SNIPPET_SUCCESS_TTL = 1500;

let dom = {};
let appState = { currentTunnelURL: "" };
let rt = realtimeClient;

let payloadLoading = false;
let payloadLoadedOnce = false;
let payloadItemsCache = [];
let payloadMessageTimeout = null;
let snippetMessageTimeout = null;
let tunnelBannerTimeout = null;
let tunnelInitialized = false;
let tunnelActive = null;
let lastAnnouncedTunnelURL = "";
let realtimeSubscribed = false;

export function init(ctx = {}) {
  appState = ctx.state || appState;
  if (ctx.realtimeClient) {
    rt = ctx.realtimeClient;
  }

  dom = {
    page: $("#pagePayload"),
    form: $("#payloadForm"),
    file: $("#payloadFile"),
    name: $("#payloadName"),
    category: $("#payloadCategory"),
    list: $("#payloadList"),
    message: $("#payloadMessage"),
    tunnelBanner: $("#payloadTunnelStatus"),
    snippetForm: $("#snippetForm"),
    snippetContent: $("#snippetContent"),
    snippetMime: $("#snippetMime"),
    snippetFilename: $("#snippetFilename"),
    snippetResult: $("#snippetResult"),
    snippetMessage: $("#snippetMessage"),
  };

  bindEventListeners();
  subscribeRealtime();
}

export function activate() {
  ensurePayloadLab();
}

function bindEventListeners() {
  if (dom.form && !dom.form.dataset.bound) {
    dom.form.addEventListener("submit", handlePayloadUpload, {
      passive: false,
    });
    dom.form.dataset.bound = "1";
  }

  if (dom.list && !dom.list.dataset.bound) {
    dom.list.addEventListener("click", handlePayloadListClick);
    dom.list.dataset.bound = "1";
  }

  if (dom.snippetForm && !dom.snippetForm.dataset.bound) {
    dom.snippetForm.addEventListener("submit", handleSnippetSubmit, {
      passive: false,
    });
    dom.snippetForm.dataset.bound = "1";
  }

  if (dom.snippetResult && !dom.snippetResult.dataset.bound) {
    dom.snippetResult.addEventListener("click", handleSnippetClick);
    dom.snippetResult.dataset.bound = "1";
  }
}

function subscribeRealtime() {
  if (realtimeSubscribed || !rt) return;
  rt.subscribe("payload.list", handleRealtimePayloadList, { snapshot: true });
  rt.subscribe(
    "tunnel.status",
    (status) => handleTunnelStatus(status, { announce: false }),
    {
      snapshot: true,
    }
  );
  realtimeSubscribed = true;
}

function ensurePayloadLab(force = false) {
  if (!dom.page) return;
  if (!payloadLoadedOnce || force) {
    loadPayloads(force);
  }
}

async function loadPayloads(force = false) {
  if (!dom.list || payloadLoading) return;
  payloadLoading = true;
  if (force || !payloadLoadedOnce) {
    setPayloadMessage("Loading payloads…", "info");
  }

  try {
    await syncTunnelStatus(false);
    const data = await jget("/api/payloads");
    const items = Array.isArray(data?.items) ? data.items : [];
    rememberPayloadItems(items);
    renderPayloadList(payloadItemsCache);
    setPayloadMessage("");
    payloadLoadedOnce = true;
  } catch (error) {
    console.error("Failed to load payloads", error);
    const message = error?.message || "Failed to load payloads";
    setPayloadMessage(message, "error");
  } finally {
    payloadLoading = false;
  }
}

async function handlePayloadUpload(event) {
  event.preventDefault();
  if (!dom.file || !dom.file.files || dom.file.files.length === 0) {
    setPayloadMessage("Select a file to upload.", "error");
    return;
  }

  const submitBtn = dom.form?.querySelector('button[type="submit"]');
  if (submitBtn) setLoading(submitBtn, true, "Uploading…");
  dom.form?.classList.add("is-uploading");
  setPayloadMessage("Uploading…", "info");

  try {
    const fd = new FormData();
    fd.append("file", dom.file.files[0]);
    if (dom.name && dom.name.value.trim()) {
      fd.append("name", dom.name.value.trim());
    }
    if (dom.category && dom.category.value.trim()) {
      fd.append("category", dom.category.value.trim());
    }

    const response = await fetch("/api/payloads", {
      method: "POST",
      body: fd,
    });
    if (!response.ok) {
      const text = await response.text();
      throw new Error(text || `HTTP ${response.status}`);
    }

    dom.form?.reset();
    if (dom.file) dom.file.value = "";
    await loadPayloads(true);
    setPayloadMessage("Upload complete.", "success", SUCCESS_MESSAGE_TTL);
  } catch (error) {
    const message = error?.message || "Upload failed";
    setPayloadMessage(`Upload failed: ${message}`, "error");
  } finally {
    if (submitBtn) setLoading(submitBtn, false);
    dom.form?.classList.remove("is-uploading");
  }
}

async function handlePayloadListClick(event) {
  const target = event.target instanceof HTMLElement ? event.target : null;
  if (!target) return;
  const actionEl = target.closest("[data-action]");
  if (!actionEl) return;

  const action = actionEl.getAttribute("data-action");
  switch (action) {
    case "delete-payload":
      await handleDeletePayload(actionEl);
      break;
    case "copy-variant":
      await handleCopyVariant(actionEl, "Variant link copied to clipboard.");
      break;
    case "copy-spectrum":
      await handleCopyVariant(actionEl, "Spectrum link copied.");
      break;
    case "copy-spectrum-wrapper":
      await handleCopyWrapper(actionEl);
      break;
    case "copy-spectrum-window":
      await handleCopySpectrumWindow(actionEl);
      break;
    default:
      break;
  }
}

async function handleDeletePayload(button) {
  const id = button.getAttribute("data-payload");
  if (!id) return;
  if (!window.confirm("Delete this payload? This cannot be undone.")) {
    return;
  }

  setLoading(button, true, "Deleting…");
  try {
    const resp = await fetch(`/api/payloads/${encodeURIComponent(id)}`, {
      method: "DELETE",
    });
    if (!resp.ok) {
      const text = await resp.text();
      throw new Error(text || `HTTP ${resp.status}`);
    }
    await loadPayloads(true);
    setPayloadMessage("Payload deleted.", "success", SUCCESS_MESSAGE_TTL);
  } catch (error) {
    const message = error?.message || "Delete failed";
    setPayloadMessage(`Delete failed: ${message}`, "error");
  } finally {
    setLoading(button, false);
  }
}

async function handleCopyVariant(button, successMessage) {
  const url = button.getAttribute("data-url") || "";
  if (!url) return;
  try {
    const copied = await copyTextToClipboard(url);
    if (copied) {
      showCopyFeedback(button, "Copy", "Copied!");
      setPayloadMessage(successMessage, "success", 1800);
    }
  } catch (error) {
    const message = error?.message || "Copy failed";
    setPayloadMessage(`Copy failed: ${message}`, "error");
  }
}

async function handleCopyWrapper(button) {
  const url = button.getAttribute("data-url") || "";
  if (!url) return;
  const kind = (button.getAttribute("data-kind") || "wrapper").toUpperCase();
  try {
    const copied = await copyTextToClipboard(url);
    if (copied) {
      showCopyFeedback(button, "Copy", "Copied!");
      setPayloadMessage(`${kind} spectrum wrapper copied.`, "success", 1800);
    }
  } catch (error) {
    const message = error?.message || "Copy failed";
    setPayloadMessage(`Copy failed: ${message}`, "error");
  }
}

async function handleCopySpectrumWindow(button) {
  const wrapper = button.closest(".payload-spectrum");
  if (!wrapper) return;
  const urls = Array.from(
    wrapper.querySelectorAll('button[data-action="copy-spectrum"]')
  )
    .map((el) => el.getAttribute("data-url") || "")
    .filter(Boolean);

  if (urls.length === 0) return;
  const range =
    button.getAttribute("data-range") ||
    `${wrapper.dataset.start || 0}-${wrapper.dataset.end || 0}`;

  try {
    const copied = await copyTextToClipboard(urls.join("\n"));
    if (copied) {
      showCopyFeedback(button, "Copy window", "Copied!");
      setPayloadMessage(
        `Spectrum window (${range}) copied to clipboard.`,
        "success",
        1800
      );
    }
  } catch (error) {
    const message = error?.message || "Copy failed";
    setPayloadMessage(`Copy failed: ${message}`, "error");
  }
}

async function handleSnippetSubmit(event) {
  event.preventDefault();
  if (!dom.snippetContent) return;
  const content = dom.snippetContent.value;
  if (!content || !content.trim()) {
    setSnippetMessage("Please enter code or shell script.", "error");
    dom.snippetContent.focus();
    return;
  }

  const body = { content };
  if (dom.snippetMime && dom.snippetMime.value) {
    body.mime = dom.snippetMime.value;
  }
  if (dom.snippetFilename) {
    const name = dom.snippetFilename.value.trim();
    if (name) body.filename = name;
  }

  const submitBtn = dom.snippetForm?.querySelector('button[type="submit"]');
  if (submitBtn) setLoading(submitBtn, true, "Creating…");
  setSnippetMessage("Creating snippet…", "info");

  try {
    const resp = await fetch("/api/snippets", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    if (!resp.ok) {
      const text = await resp.text();
      throw new Error(text || `HTTP ${resp.status}`);
    }
    const data = await resp.json();
    renderSnippetResultCard(data);
    setSnippetMessage("Snippet link created.", "success", SNIPPET_SUCCESS_TTL);
  } catch (error) {
    const message = error?.message || "Snippet failed";
    setSnippetMessage(`Snippet failed: ${message}`, "error");
  } finally {
    if (submitBtn) setLoading(submitBtn, false);
  }
}

async function handleSnippetClick(event) {
  const target = event.target instanceof HTMLElement ? event.target : null;
  if (!target) return;
  const actionEl = target.closest("[data-action]");
  if (!actionEl) return;
  if (actionEl.getAttribute("data-action") !== "copy-snippet-link") return;

  const url = actionEl.getAttribute("data-url") || "";
  if (!url) return;
  try {
    const copied = await copyTextToClipboard(url);
    if (copied) {
      showCopyFeedback(actionEl, "Copy", "Copied!");
      const kind = (actionEl.getAttribute("data-kind") || "raw").toUpperCase();
      setSnippetMessage(`${kind} link copied.`, "success", SNIPPET_SUCCESS_TTL);
    }
  } catch (error) {
    const message = error?.message || "Copy failed";
    setSnippetMessage(`Copy failed: ${message}`, "error");
  }
}

function handleRealtimePayloadList(payload) {
  const items = Array.isArray(payload)
    ? payload
    : Array.isArray(payload?.items)
    ? payload.items
    : [];
  rememberPayloadItems(items);
  rerenderPayloadList();
}

function rememberPayloadItems(items) {
  payloadItemsCache = Array.isArray(items) ? items.slice() : [];
}

function rerenderPayloadList() {
  if (!payloadLoadedOnce) return;
  renderPayloadList(payloadItemsCache);
}

function renderPayloadList(items) {
  if (!dom.list) return;
  dom.list.textContent = "";
  if (!Array.isArray(items) || items.length === 0) {
    const empty = document.createElement("div");
    empty.className = "payload-empty";
    empty.textContent = "No payloads yet. Upload a file to get started.";
    dom.list.appendChild(empty);
    return;
  }

  for (const entry of items) {
    const card = createPayloadCard(entry);
    if (card) dom.list.appendChild(card);
  }
}

function createPayloadCard(entry) {
  if (!entry || typeof entry !== "object") return null;
  const meta = entry.payload || {};
  const variants = Array.isArray(entry.variants) ? entry.variants : [];

  const card = document.createElement("article");
  card.className = "payload-card";
  if (meta.id) card.dataset.payloadId = meta.id;

  const head = document.createElement("header");
  head.className = "payload-card-head";
  card.appendChild(head);

  const titleWrap = document.createElement("div");
  titleWrap.className = "payload-card-title";
  head.appendChild(titleWrap);

  const nameEl = document.createElement("div");
  nameEl.className = "payload-name";
  nameEl.textContent =
    meta.name || meta.original_filename || meta.id || "Payload";
  titleWrap.appendChild(nameEl);

  const infoParts = [];
  if (meta.category) infoParts.push(`#${meta.category}`);
  if (meta.original_filename) infoParts.push(meta.original_filename);
  if (meta.mime_type) infoParts.push(meta.mime_type);
  infoParts.push(formatBytes(meta.size || 0));
  if (infoParts.length) {
    const infoEl = document.createElement("div");
    infoEl.className = "payload-meta";
    infoEl.textContent = infoParts.join(" · ");
    titleWrap.appendChild(infoEl);
  }

  if (meta.created_at) {
    const timeEl = document.createElement("div");
    timeEl.className = "payload-time";
    timeEl.textContent = `Added ${formatTimestamp(meta.created_at)}`;
    titleWrap.appendChild(timeEl);
  }

  const deleteBtn = document.createElement("button");
  deleteBtn.className = "btn sm warn";
  deleteBtn.type = "button";
  deleteBtn.textContent = "Delete";
  deleteBtn.dataset.action = "delete-payload";
  if (meta.id) deleteBtn.dataset.payload = meta.id;
  head.appendChild(deleteBtn);

  const variantsBox = document.createElement("div");
  variantsBox.className = "payload-variants";
  card.appendChild(variantsBox);

  if (variants.length === 0) {
    const emptyVar = document.createElement("div");
    emptyVar.className = "payload-variant empty";
    emptyVar.textContent = "Variants will appear after upload.";
    variantsBox.appendChild(emptyVar);
  } else {
    for (const variant of variants) {
      const row = document.createElement("div");
      row.className = "payload-variant";
      if (variant?.key) row.dataset.variant = variant.key;

      const keySpan = document.createElement("span");
      keySpan.className = "variant-key";
      keySpan.textContent = variant?.key || "variant";
      row.appendChild(keySpan);

      const variantURL = buildVariantURL(variant?.path);

      const openLink = document.createElement("a");
      openLink.className = "btn sm ghost";
      openLink.target = "_blank";
      openLink.rel = "noopener";
      openLink.textContent = "Open";
      openLink.href = variantURL || "#";
      row.appendChild(openLink);

      const copyBtn = document.createElement("button");
      copyBtn.className = "btn sm ghost";
      copyBtn.type = "button";
      copyBtn.textContent = "Copy";
      copyBtn.dataset.action = "copy-variant";
      if (variantURL) copyBtn.dataset.url = variantURL;
      row.appendChild(copyBtn);

      variantsBox.appendChild(row);
    }
  }

  const spectrumSection = createSpectrumSection(meta);
  if (spectrumSection) {
    card.appendChild(spectrumSection);
  }

  return card;
}

function createSpectrumSection(meta) {
  if (!meta || !meta.id) return null;
  const state = loadSpectrumState(meta.id);
  const wrapper = document.createElement("div");
  wrapper.className = "payload-spectrum";
  wrapper.dataset.payloadId = meta.id;

  const toolbar = document.createElement("div");
  toolbar.className = "spectrum-toolbar";
  wrapper.appendChild(toolbar);

  const title = document.createElement("span");
  title.className = "spectrum-title";
  title.textContent = "Spectrum window";
  toolbar.appendChild(title);

  const prevBtn = document.createElement("button");
  prevBtn.type = "button";
  prevBtn.className = "btn sm ghost";
  prevBtn.textContent = "Prev";
  toolbar.appendChild(prevBtn);

  const nextBtn = document.createElement("button");
  nextBtn.type = "button";
  nextBtn.className = "btn sm ghost";
  nextBtn.textContent = "Next";
  toolbar.appendChild(nextBtn);

  const startWrap = document.createElement("label");
  startWrap.className = "spectrum-start-wrap";
  startWrap.textContent = "Start";
  const startInput = document.createElement("input");
  startInput.type = "number";
  startInput.min = "0";
  startInput.step = "1";
  startInput.value = state.start || 0;
  startInput.className = "spectrum-start";
  startWrap.appendChild(startInput);
  toolbar.appendChild(startWrap);

  const goBtn = document.createElement("button");
  goBtn.type = "button";
  goBtn.className = "btn sm ghost";
  goBtn.textContent = "Go";
  toolbar.appendChild(goBtn);

  const rangeLabel = document.createElement("span");
  rangeLabel.className = "spectrum-range";
  toolbar.appendChild(rangeLabel);

  const copyWindowBtn = document.createElement("button");
  copyWindowBtn.type = "button";
  copyWindowBtn.className = "btn sm ghost";
  copyWindowBtn.textContent = "Copy window";
  copyWindowBtn.dataset.action = "copy-spectrum-window";
  copyWindowBtn.dataset.payload = meta.id;
  toolbar.appendChild(copyWindowBtn);

  const list = document.createElement("div");
  list.className = "spectrum-list";
  wrapper.appendChild(list);

  const render = () => {
    let base = Number.parseInt(startInput.value, 10);
    if (!Number.isFinite(base) || base < 0) base = 0;
    startInput.value = base;
    state.start = base;
    saveSpectrumState(meta.id, state);
    wrapper.dataset.start = base;
    wrapper.dataset.end = base + SPECTRUM_WINDOW - 1;
    copyWindowBtn.dataset.range = `${base}-${base + SPECTRUM_WINDOW - 1}`;
    rangeLabel.textContent = `Seeds ${base}–${base + SPECTRUM_WINDOW - 1}`;
    list.textContent = "";

    const createWrapperGroup = (label, urlValue, kind, seedValue) => {
      const group = document.createElement("div");
      group.className = "spectrum-wrapper";

      const tag = document.createElement("span");
      tag.className = "spectrum-wrapper-name";
      tag.textContent = label;
      group.appendChild(tag);

      const open = document.createElement("a");
      open.className = "btn sm ghost";
      open.target = "_blank";
      open.rel = "noopener";
      open.textContent = "Open";
      open.href = urlValue || "#";
      group.appendChild(open);

      const copy = document.createElement("button");
      copy.type = "button";
      copy.className = "btn sm ghost";
      copy.textContent = "Copy";
      copy.dataset.action = "copy-spectrum-wrapper";
      copy.dataset.kind = kind;
      copy.dataset.seed = String(seedValue);
      if (urlValue) copy.dataset.url = urlValue;
      group.appendChild(copy);

      if (!urlValue) {
        open.setAttribute("aria-disabled", "true");
        open.classList.add("is-disabled");
        copy.disabled = true;
      }

      return group;
    };

    for (let i = 0; i < SPECTRUM_WINDOW; i += 1) {
      const seed = base + i;
      const row = document.createElement("div");
      row.className = "payload-variant spectrum";
      row.dataset.seed = String(seed);

      const keySpan = document.createElement("span");
      keySpan.className = "variant-key";
      keySpan.textContent = `spec ${seed}`;
      row.appendChild(keySpan);

      const url = buildVariantURL(`/payload/spec/${meta.id}/${seed}`);

      const openLink = document.createElement("a");
      openLink.className = "btn sm ghost";
      openLink.target = "_blank";
      openLink.rel = "noopener";
      openLink.textContent = "Open";
      openLink.href = url || "#";
      row.appendChild(openLink);

      const copyBtn = document.createElement("button");
      copyBtn.type = "button";
      copyBtn.className = "btn sm ghost";
      copyBtn.textContent = "Copy";
      copyBtn.dataset.action = "copy-spectrum";
      if (url) copyBtn.dataset.url = url;
      copyBtn.dataset.seed = String(seed);
      row.appendChild(copyBtn);

      const htmlURL = buildVariantURL(`/payload/spec/html/${meta.id}/${seed}`);
      const ogURL = buildVariantURL(`/payload/spec/og/${meta.id}/${seed}`);

      const wrappers = document.createElement("div");
      wrappers.className = "spectrum-wrappers";
      wrappers.appendChild(createWrapperGroup("HTML", htmlURL, "html", seed));
      wrappers.appendChild(createWrapperGroup("OG", ogURL, "og", seed));
      row.appendChild(wrappers);

      list.appendChild(row);
    }
  };

  prevBtn.addEventListener("click", () => {
    const current = Number.parseInt(startInput.value, 10) || 0;
    startInput.value = Math.max(0, current - SPECTRUM_WINDOW);
    render();
  });

  nextBtn.addEventListener("click", () => {
    const current = Number.parseInt(startInput.value, 10) || 0;
    startInput.value = current + SPECTRUM_WINDOW;
    render();
  });

  goBtn.addEventListener("click", render);
  startInput.addEventListener("keydown", (ev) => {
    if (ev.key === "Enter") {
      ev.preventDefault();
      render();
    }
  });
  startInput.addEventListener("blur", render);

  render();
  return wrapper;
}

function spectrumKey(id) {
  return `payload-spectrum:${id}`;
}

function loadSpectrumState(id) {
  try {
    const raw = localStorage.getItem(spectrumKey(id));
    if (!raw) return { start: 0 };
    const parsed = JSON.parse(raw);
    if (parsed && typeof parsed.start === "number" && parsed.start >= 0) {
      return { start: Math.floor(parsed.start) };
    }
  } catch (error) {
    console.warn("Failed to load spectrum state", error);
  }
  return { start: 0 };
}

function saveSpectrumState(id, state) {
  try {
    localStorage.setItem(spectrumKey(id), JSON.stringify(state));
  } catch (error) {
    console.warn("Failed to persist spectrum state", error);
  }
}

function renderSnippetResultCard(data) {
  if (!dom.snippetResult) return;
  dom.snippetResult.textContent = "";
  if (!data || typeof data !== "object") return;

  const card = document.createElement("article");
  card.className = "snippet-card";

  const header = document.createElement("header");
  const title = document.createElement("strong");
  title.textContent = data.filename || data.id || "Snippet";
  header.appendChild(title);

  const meta = document.createElement("div");
  meta.className = "snippet-meta";
  if (data.mime) {
    const mimeEl = document.createElement("span");
    mimeEl.textContent = data.mime;
    meta.appendChild(mimeEl);
  }
  if (data.size != null) {
    const sizeEl = document.createElement("span");
    sizeEl.textContent = formatBytes(data.size);
    meta.appendChild(sizeEl);
  }
  if (data.id) {
    const idEl = document.createElement("span");
    idEl.textContent = `ID ${data.id}`;
    meta.appendChild(idEl);
  }
  header.appendChild(meta);
  card.appendChild(header);

  const linkList = document.createElement("div");
  linkList.className = "snippet-links";
  const urls = data.urls || {};
  for (const key of ["raw", "html", "og"]) {
    const rel = urls[key];
    const full = rel ? buildVariantURL(rel) : "";
    const block = document.createElement("div");
    block.className = "snippet-link";

    const label = document.createElement("span");
    label.className = "snippet-link-label";
    label.textContent = key.toUpperCase();
    block.appendChild(label);

    const open = document.createElement("a");
    open.className = "btn sm ghost";
    open.target = "_blank";
    open.rel = "noopener";
    open.textContent = "Open";
    open.href = full || "#";
    if (!full) open.classList.add("is-disabled");
    block.appendChild(open);

    const copy = document.createElement("button");
    copy.type = "button";
    copy.className = "btn sm ghost";
    copy.textContent = "Copy";
    copy.dataset.action = "copy-snippet-link";
    copy.dataset.kind = key;
    if (full) copy.dataset.url = full;
    else copy.disabled = true;
    block.appendChild(copy);

    linkList.appendChild(block);
  }

  card.appendChild(linkList);
  dom.snippetResult.appendChild(card);
}

function setPayloadMessage(text = "", level = "info", ttlMs = 0) {
  if (!dom.message) return;
  if (payloadMessageTimeout) {
    clearTimeout(payloadMessageTimeout);
    payloadMessageTimeout = null;
  }

  dom.message.classList.remove("show", "is-info", "is-success", "is-error");
  if (!text) {
    dom.message.textContent = "";
    return;
  }

  const normalized = ["info", "success", "error"].includes(level)
    ? level
    : "info";
  dom.message.textContent = text;
  dom.message.classList.add("show", `is-${normalized}`);

  if (ttlMs > 0) {
    payloadMessageTimeout = setTimeout(() => {
      setPayloadMessage("");
    }, ttlMs);
  }
}

function setSnippetMessage(text = "", level = "info", ttlMs = 0) {
  if (!dom.snippetMessage) return;
  if (snippetMessageTimeout) {
    clearTimeout(snippetMessageTimeout);
    snippetMessageTimeout = null;
  }

  dom.snippetMessage.classList.remove(
    "show",
    "is-info",
    "is-success",
    "is-error"
  );

  if (!text) {
    dom.snippetMessage.textContent = "";
    return;
  }

  const normalized = ["info", "success", "error"].includes(level)
    ? level
    : "info";
  dom.snippetMessage.textContent = text;
  dom.snippetMessage.classList.add("show", `is-${normalized}`);

  if (ttlMs > 0) {
    snippetMessageTimeout = setTimeout(() => {
      setSnippetMessage("");
    }, ttlMs);
  }
}

function setPayloadTunnelBanner(text = "", level = "info", ttlMs = 0) {
  if (!dom.tunnelBanner) return;
  if (tunnelBannerTimeout) {
    clearTimeout(tunnelBannerTimeout);
    tunnelBannerTimeout = null;
  }

  dom.tunnelBanner.classList.remove("show", "is-info", "is-warn", "is-success");

  if (!text) {
    dom.tunnelBanner.textContent = "";
    dom.tunnelBanner.setAttribute("aria-hidden", "true");
    return;
  }

  const variant = ["info", "warn", "success"].includes(level) ? level : "info";
  dom.tunnelBanner.textContent = text;
  dom.tunnelBanner.setAttribute("aria-hidden", "false");
  dom.tunnelBanner.classList.add("show", `is-${variant}`);

  if (ttlMs > 0) {
    tunnelBannerTimeout = setTimeout(() => {
      setPayloadTunnelBanner("");
    }, ttlMs);
  }
}

async function syncTunnelStatus(announce) {
  try {
    const status = await jget("/api/tunnel");
    if (status && typeof status === "object") {
      handleTunnelStatus(status, { announce });
    }
  } catch (error) {
    console.warn("Failed to fetch tunnel status", error);
  }
}

function handleTunnelStatus(status, options = {}) {
  if (!status) return;
  const active = Boolean(status.active);
  const sanitized = setCurrentTunnelURL(status.url, {
    announce: Boolean(options.announce),
  });

  if (!active) {
    setPayloadTunnelBanner(
      "Cloudflared tunnel is inactive. Restart it on the Tunnel tab so payload links work externally.",
      "warn"
    );
    lastAnnouncedTunnelURL = "";
  } else {
    if (!sanitized) {
      setPayloadTunnelBanner(
        "Tunnel active, awaiting public URL from Cloudflared…",
        "warn"
      );
    } else if (!tunnelInitialized) {
      setPayloadTunnelBanner("");
      lastAnnouncedTunnelURL = sanitized;
    } else {
      if (tunnelActive === false) {
        const host = safeTunnelHost(sanitized);
        if (host) {
          setPayloadTunnelBanner(
            `Tunnel back online on ${host}. Links refreshed automatically.`,
            "info",
            6000
          );
        } else {
          setPayloadTunnelBanner("");
        }
      } else if (sanitized && sanitized !== lastAnnouncedTunnelURL) {
        const host = safeTunnelHost(sanitized);
        if (host) {
          setPayloadTunnelBanner(
            `Tunnel switched to ${host}. Links refreshed automatically.`,
            "info",
            6000
          );
        }
      } else {
        setPayloadTunnelBanner("");
      }
      if (sanitized) {
        lastAnnouncedTunnelURL = sanitized;
      }
    }
  }

  tunnelActive = active;
  tunnelInitialized = true;
  if (appState) {
    appState.tunnelActive = active;
  }
}

function setCurrentTunnelURL(url, opts = {}) {
  const sanitized = sanitizeTunnelURL(url);
  const previous = appState?.currentTunnelURL || "";
  if (appState) {
    appState.currentTunnelURL = sanitized;
  }

  if (sanitized !== previous && payloadLoadedOnce) {
    rerenderPayloadList();
  }

  if (opts.announce && sanitized && previous && sanitized !== previous) {
    const host = safeTunnelHost(sanitized);
    if (host) {
      setPayloadTunnelBanner(
        `Tunnel switched to ${host}. Links refreshed automatically.`,
        "info",
        6000
      );
    }
  }

  return sanitized;
}

function sanitizeTunnelURL(url) {
  if (!url || typeof url !== "string") return "";
  try {
    const parsed = new URL(url, window.location.origin);
    return parsed.origin.replace(/\/$/, "");
  } catch (error) {
    console.warn("Failed to sanitize tunnel URL", error);
    return "";
  }
}

function safeTunnelHost(url) {
  try {
    const parsed = new URL(url);
    return parsed.host;
  } catch (error) {
    return url || "";
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

function buildVariantURL(path) {
  if (!path) return "";
  if (/^https?:\/\//i.test(path)) return path;
  const base = getTunnelBaseURL();
  if (!base) return path;
  const cleanPath = path.startsWith("/") ? path : `/${path}`;
  return `${base}${cleanPath}`;
}
