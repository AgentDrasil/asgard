import { inject, provide, type InjectionKey } from "vue";
import type { SessionStore } from "./useSessionStore";

export const sessionStoreKey: InjectionKey<SessionStore> = Symbol("sessionStore");

/** Makes the store available to every descendant component. Call once, in App. */
export function provideSessionStore(store: SessionStore): SessionStore {
  provide(sessionStoreKey, store);
  return store;
}

/** Injects the store provided by an ancestor; throws when there is none. */
export function useInjectedSessionStore(): SessionStore {
  const store = inject(sessionStoreKey);
  if (!store) {
    throw new Error("Session store not provided: call provideSessionStore() in an ancestor.");
  }
  return store;
}
