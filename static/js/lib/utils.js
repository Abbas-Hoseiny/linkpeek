// DOM utility - querySelector shorthand
export const $ = (s, r = document) => r.querySelector(s);

// Fetch JSON with error handling
export async function jget(url) {
  const response = await fetch(url, { cache: "no-store" });
  const text = await response.text();
  try {
    return JSON.parse(text);
  } catch (e) {
    const err = new Error(`HTTP ${response.status}: ${text.slice(0, 140)}`);
    err.cause = e;
    throw err;
  }
}

// Show feedback on button after action (e.g., "Copied!")
export function showCopyFeedback(
  btn,
  originalText = "Copy",
  feedbackText = "Copied!",
  durationMs = 1500
) {
  if (!btn || !(btn instanceof HTMLElement)) return;
  const original = originalText || btn.textContent;
  btn.textContent = feedbackText;
  btn.disabled = true;
  setTimeout(() => {
    btn.textContent = original;
    btn.disabled = false;
  }, durationMs);
}

// Set loading state on button
export function setLoading(btn, loading = true, loadingText = "Loading…") {
  if (!btn || !(btn instanceof HTMLElement)) return;
  if (loading) {
    if (!btn.dataset.originalText) {
      btn.dataset.originalText = btn.textContent;
    }
    btn.disabled = true;
    btn.innerHTML = `<span class="spinner"></span> ${loadingText}`;
  } else {
    btn.disabled = false;
    if (btn.dataset.originalText) {
      btn.textContent = btn.dataset.originalText;
      delete btn.dataset.originalText;
    }
  }
}

// Format date/time
export function formatDateTime(date) {
  if (!date) return "—";
  const d = date instanceof Date ? date : new Date(date);
  return d.toLocaleString();
}

// Format file size
export function formatFileSize(bytes) {
  if (bytes === 0) return "0 B";
  const k = 1024;
  const sizes = ["B", "KB", "MB", "GB"];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return Math.round((bytes / Math.pow(k, i)) * 100) / 100 + " " + sizes[i];
}

// Show error message
export function showError(message, container) {
  if (!container) return;
  container.textContent = message;
  container.className = "message error";
  container.style.display = "block";
}

// Show success message
export function showSuccess(message, container) {
  if (!container) return;
  container.textContent = message;
  container.className = "message success";
  container.style.display = "block";
}

// Hide message
export function hideMessage(container) {
  if (!container) return;
  container.style.display = "none";
}

export function formatTimestamp(value) {
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

export function formatBytes(value) {
  const bytes = Number(value || 0);
  if (!Number.isFinite(bytes) || bytes < 0) return "0 B";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  if (bytes < 1024 * 1024 * 1024)
    return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
  return `${(bytes / 1024 / 1024 / 1024).toFixed(1)} GB`;
}

export async function copyTextToClipboard(text) {
  if (typeof text !== "string" || text.length === 0) {
    return false;
  }
  if (navigator.clipboard && navigator.clipboard.writeText) {
    await navigator.clipboard.writeText(text);
    return true;
  }
  window.prompt("Copy to clipboard:", text);
  return false;
}

export function escapeHTML(value) {
  return String(value ?? "")
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

export function escapeAttr(value) {
  return escapeHTML(value).replace(/`/g, "&#96;");
}

export function extractItems(payload) {
  if (Array.isArray(payload)) return payload.slice();
  if (payload && Array.isArray(payload.items)) return payload.items.slice();
  return [];
}
