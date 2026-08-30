import { ref, readonly } from "vue";
import type { ToastItem } from "../types";

export type ToastType = "info" | "success" | "warning" | "error";

export interface ToastOptions {
  title?: string;
  duration?: number;
}

const toasts = ref<ToastItem[]>([]);
const toastHistory = ref<ToastItem[]>([]);
const timers = new Map<string, ReturnType<typeof setTimeout>>();

let nextId = 1;

function generateId(): string {
  if (typeof crypto !== "undefined" && crypto.randomUUID) {
    return crypto.randomUUID();
  }
  return `toast-${Date.now()}-${nextId++}`;
}

export function useToast() {
  const removeToast = (id: string) => {
    const timer = timers.get(id);
    if (timer) {
      clearTimeout(timer);
      timers.delete(id);
    }
    toasts.value = toasts.value.filter((t) => t.id !== id);
  };

  const clear = () => {
    timers.forEach((timer) => clearTimeout(timer));
    timers.clear();
    toasts.value = [];
  };

  const clearToastHistory = () => {
    toastHistory.value = [];
  };

  const addToast = (type: ToastType, message: string, options?: ToastOptions): string => {
    const id = generateId();
    const duration =
      options?.duration !== undefined ? options.duration : type === "error" ? 0 : 5000;

    const item: ToastItem = {
      id,
      type,
      message,
      title: options?.title,
      duration,
      timestamp: Date.now(),
    };

    toastHistory.value.push(item);
    if (toastHistory.value.length > 500) {
      toastHistory.value.shift();
    }

    // Avoid duplicate toast with identical type and message in active floating toasts
    const existing = toasts.value.find((t) => t.type === type && t.message === message);
    if (existing) {
      return existing.id;
    }

    toasts.value.push(item);

    if (duration > 0) {
      const timer = setTimeout(() => {
        removeToast(id);
      }, duration);
      timers.set(id, timer);
    }

    return id;
  };

  const info = (message: string, options?: ToastOptions) => addToast("info", message, options);
  const success = (message: string, options?: ToastOptions) =>
    addToast("success", message, options);
  const warning = (message: string, options?: ToastOptions) =>
    addToast("warning", message, options);
  const error = (message: string, options?: ToastOptions) => addToast("error", message, options);

  return {
    toasts: readonly(toasts),
    toastHistory: readonly(toastHistory),
    clearToastHistory,
    addToast,
    removeToast,
    clear,
    info,
    success,
    warning,
    error,
  };
}
