"use strict";

// 在样式表加载前恢复偏好，避免已选暗色的页面先闪过亮色。
(() => {
  const key = "novaemby.playback-history.theme";
  let theme = "light";

  function apply(value, save = false) {
    theme = value === "dark" ? "dark" : "light";
    document.documentElement.dataset.theme = theme;
    const meta = document.querySelector('meta[name="theme-color"]');
    if (meta) meta.setAttribute("content", theme === "dark" ? "#10141d" : "#f6f7f9");
    const button = document.getElementById("themeButton");
    if (button) {
      const next = theme === "dark" ? "亮色" : "暗色";
      button.setAttribute("aria-label", `切换到${next}主题`);
      button.setAttribute("aria-pressed", String(theme === "dark"));
      button.title = `切换到${next}主题`;
      button.querySelector("[data-theme-label]").textContent = next;
    }
    if (save) {
      try { window.localStorage.setItem(key, theme); } catch (_) { /* 禁用存储时仍可切换本页。 */ }
    }
  }

  try { theme = window.localStorage.getItem(key) === "dark" ? "dark" : "light"; } catch (_) {}
  apply(theme);
  window.NovaTheme = {
    get: () => theme,
    toggle: () => apply(theme === "dark" ? "light" : "dark", true),
    sync: () => apply(theme)
  };
  window.addEventListener("storage", (event) => {
    if (event.key === key || event.key === null) apply(event.newValue);
  });
})();
