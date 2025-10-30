(function () {
  const card = document.querySelector(".login-card");
  const password = document.querySelector("#password");
  const reveal = document.querySelector(".reveal");

  if (!card || !password || !reveal) {
    return;
  }

  const open = () => card.classList.add("open");
  const close = () => {
    if (!password.value.trim() && card.dataset.state !== "error") {
      card.classList.remove("open");
    }
  };

  if (card.dataset.state === "error" || password.value.trim()) {
    card.classList.add("open");
  }

  password.addEventListener("focus", open);
  password.addEventListener("input", () => {
    if (password.value.trim()) {
      card.classList.add("open");
    }
  });
  password.addEventListener("blur", close);

  reveal.addEventListener("click", () => {
    const isText = password.type === "text";
    password.type = isText ? "password" : "text";
    reveal.setAttribute("aria-pressed", String(!isText));
    if (!isText) {
      password.focus();
    }
  });
})();
