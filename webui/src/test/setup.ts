// Global Vitest setup, loaded for every test file before its imports run.

// jsdom does not implement the Canvas API. Libraries such as @xterm/xterm and
// chart.js call `canvas.getContext(...)` at import or mount time, which makes
// jsdom log "Error: Not implemented: HTMLCanvasElement.prototype.getContext".
// Stub it with a permissive no-op context so imports and mounts stay quiet.
// In plain node environments HTMLCanvasElement does not exist, so skip there.
if (typeof HTMLCanvasElement !== "undefined") {
  const noop: () => undefined = () => undefined;

  HTMLCanvasElement.prototype.getContext = function (this: HTMLCanvasElement) {
    return new Proxy(
      { canvas: this },
      {
        get: (target, property) =>
          property in target ? target[property as keyof typeof target] : noop,
        set: () => true,
      },
    );
  } as unknown as typeof HTMLCanvasElement.prototype.getContext;
}
