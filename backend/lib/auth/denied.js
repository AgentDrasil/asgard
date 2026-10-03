// Runs on the access-denied page. It clears any stored session so that a later
// attempt (for example after an administrator grants the required role) starts
// from a clean login instead of reusing a token that still lacks the role.
try {
  localStorage.removeItem("asgard_auth");
} catch (e) {
  /* localStorage can be unavailable (e.g. private mode); nothing to clear then */
}
