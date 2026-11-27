import { createApp, type Component } from "vue";
import { sessionStoreKey } from "../composables/sessionStoreContext";
import { useSessionStore, type SessionStore } from "../composables/useSessionStore";

/**
 * Mounts a component with a session store provided, for components that call
 * useInjectedSessionStore(). Pass an existing store to pre-seed its state;
 * otherwise a fresh one is created. Call `unmount()` when done.
 */
export function mountWithSessionStore(
  component: Component,
  props: Record<string, unknown> = {},
  store: SessionStore = useSessionStore(),
) {
  const root = document.createElement("div");
  const app = createApp(component, props);
  app.provide(sessionStoreKey, store);
  app.mount(root);
  return { store, root, unmount: () => app.unmount() };
}
