import {
  getStoredTokens,
  isAuthEnabled,
  isTabHidden,
  listenForTabActivationRedirect,
  redirectToDenied,
  redirectToLogin,
  refreshOnce,
  reloadForExternalSSO,
  sessionRefreshedElsewhere,
  sessionStamp,
} from "./auth";

// Centralized fetch wrapper.
//
// With auth disabled it keeps the original behaviour (a 401 triggers the
// external-SSO reload). With auth enabled it injects the bearer token, silently
// refreshes once on 401 and redirects to the denial page on 403.
export async function apiFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  // applyAuth returns init unchanged (including undefined) when there is nothing
  // to add, so callers observe the same fetch() arguments as before auth existed.
  const applyAuth = (options?: RequestInit): RequestInit | undefined => {
    if (!isAuthEnabled()) return options;
    const token = getStoredTokens()?.access_token;
    if (!token) return options;
    const headers = new Headers(options?.headers);
    headers.set("Authorization", `Bearer ${token}`);
    return { ...options, headers };
  };

  let response = await fetch(input, applyAuth(init));

  if (!isAuthEnabled()) {
    if (response.status === 401) {
      console.log("apiFetch: 401 received, redirecting to refresh session via SSO...");
      reloadForExternalSSO();
    }
    return response;
  }

  if (response.status === 403) {
    let isRoleForbidden = false;
    try {
      const resClone = typeof response.clone === "function" ? response.clone() : response;
      const data = await resClone.json();
      if (data && typeof data === "object" && data.error === "insufficient role") {
        isRoleForbidden = true;
      }
    } catch {
      // not JSON: treat as non-role forbidden
    }

    if (isRoleForbidden) {
      redirectToDenied();
      // Return an unresolved promise so callers neither retry nor start an
      // endless fetch loop while the browser navigates away.
      return new Promise<Response>(() => {});
    }

    return response;
  }

  if (response.status === 401) {
    const before = sessionStamp();
    const result = await refreshOnce();
    if (result === "ok") {
      response = await fetch(input, applyAuth(init));
    } else if (result === "expired") {
      if (await sessionRefreshedElsewhere(before)) {
        // Another tab exchanged the single-use refresh token first; reuse the
        // session it published instead of forcing a re-login.
        response = await fetch(input, applyAuth(init));
      } else {
        // When running in a background tab (e.g. background polling), do not
        // bounce the page to /auth/login. An inactive tab cannot complete the
        // OAuth flow and would cause concurrent callback code exchanges or
        // state invalidations with active tabs. Wait until the user activates
        // the tab instead.
        if (isTabHidden()) {
          // Listen for tab activation to re-evaluate the session: another tab
          // may have logged in or refreshed by the time the user returns. If
          // still expired, trigger the login redirect.
          listenForTabActivationRedirect();
          return new Promise<Response>(() => {});
        }
        redirectToLogin();
        return new Promise<Response>(() => {});
      }
    }
    // "unavailable": keep the session and surface the response to the caller.
  }

  return response;
}
