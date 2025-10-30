// LinkPeek Main Application Module
import { $ } from "./lib/utils.js";
import { router } from "./lib/router.js";
import { realtimeClient } from "./lib/realtime.js";
import * as home from "./features/home.js";
import * as tunnel from "./features/tunnel.js";
import * as payload from "./features/payload.js";
import * as scanner from "./features/scanner.js";
import * as retry from "./features/retry.js";
import * as capture from "./features/capture.js";

// Application state
export const state = {
  currentTunnelURL: "",
  tunnelActive: null,
  isConnected: false,
};

// Theme management
const THEME_KEY = "linkpeek:theme";
const root = document.documentElement;

function readStoredTheme() {
  try {
    return localStorage.getItem(THEME_KEY);
  } catch (error) {
    console.warn("Unable to read stored theme", error);
    return null;
  }
}

function persistTheme(theme) {
  try {
    localStorage.setItem(THEME_KEY, theme);
  } catch (error) {
    console.warn("Unable to persist theme", error);
  }
}

function applyTheme(theme, { persist = true } = {}) {
  const toggle = document.getElementById("themeToggle");
  if (theme === "light") {
    root.dataset.theme = "light";
    if (toggle) {
      toggle.setAttribute("aria-label", "Switch to dark mode");
      toggle.title = "Switch to dark mode";
      toggle.textContent = "Dark";
    }
  } else {
    delete root.dataset.theme;
    if (toggle) {
      toggle.setAttribute("aria-label", "Switch to light mode");
      toggle.title = "Switch to light mode";
      toggle.textContent = "Light";
    }
  }
  if (persist) {
    persistTheme(theme);
  }
}

function preferredTheme() {
  const stored = readStoredTheme();
  if (stored === "light" || stored === "dark") {
    return stored;
  }
  const mediaQuery = window.matchMedia?.("(prefers-color-scheme: light)");
  return mediaQuery?.matches ? "light" : "dark";
}

function setupThemeToggle() {
  const toggle = document.getElementById("themeToggle");
  if (!toggle) {
    return;
  }

  let currentTheme = preferredTheme();
  applyTheme(currentTheme, { persist: false });

  const mediaQuery = window.matchMedia?.("(prefers-color-scheme: light)");
  if (!readStoredTheme() && mediaQuery) {
    mediaQuery.addEventListener("change", (event) => {
      currentTheme = event.matches ? "light" : "dark";
      applyTheme(currentTheme, { persist: false });
    });
  }

  toggle.addEventListener("click", () => {
    currentTheme = currentTheme === "light" ? "dark" : "light";
    applyTheme(currentTheme);
  });
}

// Initialize application
export function init() {
  console.log("LinkPeek initializing...");

  // Initialize all feature modules
  const context = { state, realtimeClient };

  home.init?.(context);
  tunnel.init?.(context);
  payload.init?.(context);
  scanner.init?.(context);
  retry.init?.(context);
  capture.init?.(context);

  setupThemeToggle();

  // Register routes
  registerRoutes();

  // Set up navigation
  router.setupNavigation();

  // Start with home page
  router.navigate("home");

  // Connect to realtime updates
  if (realtimeClient) {
    realtimeClient.connect();
  }

  console.log("LinkPeek initialized");
}

// Register all application routes
function registerRoutes() {
  router.register("home", $("#pageHome"), $("#navHome"), home.activate);
  router.register("tunnel", $("#pageTunnel"), $("#navTunnel"), tunnel.activate);
  router.register(
    "payload",
    $("#pagePayload"),
    $("#navPayload"),
    payload.activate
  );
  router.register(
    "scanner",
    $("#pageScanner"),
    $("#navScanner"),
    scanner.activate
  );
  router.register("retry", $("#pageRetry"), $("#navRetry"), retry.activate);
  router.register(
    "capture",
    $("#pageCapture"),
    $("#navCapture"),
    capture.activate
  );
}

// Start the application when DOM is ready
if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", init);
} else {
  init();
}
