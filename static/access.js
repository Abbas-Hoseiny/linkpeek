(function () {
  const forms = [
    {
      form: document.getElementById("changeForm"),
      status: document.getElementById("changeStatus"),
    },
  ].filter((entry) => entry.form);

  const supportsFetch = typeof window.fetch === "function";

  const setBusy = (button, busy) => {
    if (!button) return;
    if (busy) {
      if (!button.dataset.originalText) {
        button.dataset.originalText = button.textContent;
      }
      button.disabled = true;
      button.textContent = "Saving…";
    } else {
      button.disabled = false;
      if (button.dataset.originalText) {
        button.textContent = button.dataset.originalText;
        delete button.dataset.originalText;
      }
    }
  };

  const setStatus = (el, message, type) => {
    if (!el) return;
    el.textContent = message || "";
    el.classList.remove("is-error", "is-success");
    if (!message) {
      return;
    }
    if (type === "error") {
      el.classList.add("is-error");
    } else if (type === "success") {
      el.classList.add("is-success");
    }
  };

  const handleSubmit = (entry) => {
    const { form, status } = entry;
    if (!form) return;
    form.addEventListener("submit", async (event) => {
      if (!supportsFetch) {
        return;
      }
      event.preventDefault();
      const submitBtn = form.querySelector("button[type='submit']");
      setStatus(status, "", "");
      setBusy(submitBtn, true);
      const payload = {};
      const formData = new FormData(form);
      for (const [key, value] of formData.entries()) {
        payload[key] = value;
      }
      try {
        const res = await fetch(form.action, {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
          },
          body: JSON.stringify(payload),
        });
        const isJSON = res.headers
          .get("Content-Type")
          ?.includes("application/json");
        if (res.ok) {
          if (isJSON) {
            const data = await res.json();
            if (data && typeof data.redirect === "string" && data.redirect) {
              window.location.assign(data.redirect);
              return;
            }
          }
          window.location.reload();
          return;
        }
        let message = "Request failed";
        if (isJSON) {
          try {
            const data = await res.json();
            if (data && typeof data.error === "string" && data.error.trim()) {
              message = data.error.trim();
            }
          } catch (_) {}
        } else {
          const text = await res.text();
          if (text && text.trim()) {
            message = text.trim().slice(0, 140);
          }
        }
        setStatus(status, message, "error");
        if (submitBtn) {
          submitBtn.focus();
        }
      } catch (err) {
        setStatus(status, "Network error. Please retry.", "error");
      } finally {
        setBusy(submitBtn, false);
      }
    });
  };

  forms.forEach(handleSubmit);
})();
