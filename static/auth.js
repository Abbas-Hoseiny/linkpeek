(function () {
  const overlay = document.getElementById("authOverlay");
  if (!overlay) {
    return;
  }

  const form = overlay.querySelector("#authForm");
  const passwordInput = overlay.querySelector("#authPassword");
  const toggle = overlay.querySelector(".auth-toggle");
  const submit = overlay.querySelector(".auth-submit");
  const errorEl = overlay.querySelector("#authError");
  const timestampEl = overlay.querySelector("#authTimestamp");
  const originalSubmitText = submit ? submit.textContent : "Unlock";
  let closing = false;

  if (!overlay.dataset.state) {
    overlay.dataset.state = "locked";
  }

  const url = new URL(window.location.href);
  const params = url.searchParams;
  const requestedNext = (() => {
    const candidate = params.get("next");
    if (!candidate) {
      return "";
    }
    if (!candidate.startsWith("/") || candidate.startsWith("//")) {
      return "";
    }
    return candidate;
  })();
  const initialHash = window.location.hash || "";

  const updateTimestamp = () => {
    if (!timestampEl) return;
    const stamp = new Date().toUTCString();
    timestampEl.textContent = `Protected access · ${stamp}`;
  };

  const setError = (message) => {
    if (!errorEl) return;
    if (!message) {
      errorEl.textContent = "";
      errorEl.classList.remove("show");
      overlay.classList.remove("has-error");
      return;
    }
    errorEl.textContent = message;
    errorEl.classList.add("show");
    overlay.classList.add("has-error");
  };

  const setBusy = (busy) => {
    if (!submit) return;
    submit.disabled = busy;
    if (busy) {
      submit.textContent = "Unlocking…";
      submit.classList.add("is-busy");
    } else {
      submit.textContent = originalSubmitText;
      submit.classList.remove("is-busy");
    }
  };

  const closeOverlay = (immediate) => {
    if (closing) {
      return;
    }
    closing = true;
    document.body.classList.remove("auth-lock");
    document.body.classList.add("auth-complete");
    if (immediate) {
      overlay.remove();
      return;
    }
    overlay.classList.add("is-leaving");
    setTimeout(() => {
      overlay.remove();
    }, 720);
  };

  const showOverlay = () => {
    document.body.classList.add("auth-lock");
    overlay.classList.add("is-active");
    updateTimestamp();
    if (passwordInput) {
      setTimeout(() => passwordInput.focus(), 50);
    }
  };

  const currentNext = () => {
    if (requestedNext) {
      return requestedNext;
    }
    const path = window.location.pathname + window.location.search;
    return path || "/";
  };

  const checkStatus = async () => {
    try {
      const res = await fetch("/api/auth/status", { cache: "no-store" });
      if (res.ok) {
        let data = null;
        try {
          data = await res.json();
        } catch (_) {}
        if (data && data.mustChange) {
          window.location.replace("/access?must_change=1");
          return;
        }
        if (requestedNext) {
          const currentPath = window.location.pathname + window.location.search;
          if (requestedNext !== currentPath) {
            window.location.replace(requestedNext + initialHash);
            return;
          }
        }
        closeOverlay(true);
        return;
      }
    } catch (err) {
      // Network errors fall through; overlay will stay visible.
    }
    showOverlay();
  };

  if (toggle && passwordInput) {
    toggle.addEventListener("click", () => {
      const isText = passwordInput.type === "text";
      passwordInput.type = isText ? "password" : "text";
      toggle.setAttribute("aria-pressed", String(!isText));
      passwordInput.focus();
      passwordInput.setSelectionRange(
        passwordInput.value.length,
        passwordInput.value.length
      );
    });
  }

  if (passwordInput) {
    passwordInput.addEventListener("focus", () => {
      overlay.dataset.state = "focused";
    });
    passwordInput.addEventListener("blur", () => {
      if (!passwordInput.value) {
        overlay.dataset.state = "locked";
      }
    });
    passwordInput.addEventListener("input", () => {
      if (passwordInput.value) {
        overlay.dataset.state = "ready";
      }
    });
  }

  if (form && passwordInput) {
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      const value = passwordInput.value;
      if (!value) {
        setError("Password required");
        return;
      }
      setBusy(true);
      setError("");
      try {
        const res = await fetch("/login", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ password: value, next: currentNext() }),
        });
        if (res.ok) {
          let redirect = requestedNext || "/";
          let data = null;
          try {
            data = await res.json();
          } catch (_) {}
          if (data) {
            if (typeof data.redirect === "string") {
              const trimmed = data.redirect.trim();
              if (trimmed) {
                redirect = trimmed;
              } else if (data.mustChange) {
                redirect = "/access?must_change=1";
              }
            } else if (data.mustChange) {
              redirect = "/access?must_change=1";
            }
          }
          overlay.dataset.state = "open";
          setBusy(false);
          setTimeout(() => {
            closeOverlay(false);
            setTimeout(() => {
              window.location.assign(redirect + initialHash);
            }, 640);
          }, 40);
          return;
        }
        let message = "Invalid password";
        try {
          const data = await res.json();
          if (data && typeof data.error === "string" && data.error.trim()) {
            message = data.error.trim();
          }
        } catch (_) {}
        setBusy(false);
        setError(message);
        passwordInput.focus();
        passwordInput.select();
      } catch (err) {
        setBusy(false);
        setError("Network error. Please retry.");
      }
    });
  }

  checkStatus();
})();
