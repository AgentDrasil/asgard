// Applies the persisted theme before the app bundle loads, so the first paint
// already uses the right palette. Served as an external file (not inline) so the
// site-wide Content-Security-Policy can forbid inline scripts.
try {
  var theme = localStorage.getItem("theme");
  if (theme && /^[a-z0-9-_]+$/.test(theme)) {
    document.documentElement.setAttribute("data-theme", theme);
  }
} catch {
  /* localStorage can be unavailable (private mode); the default theme applies */
}
