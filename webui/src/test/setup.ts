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

// jsdom does not implement HTMLDialogElement.showModal()/close(). Provide
// minimal versions that toggle `open` and fire `close`, like the real element.
if (typeof HTMLDialogElement !== "undefined") {
  HTMLDialogElement.prototype.showModal = function (this: HTMLDialogElement) {
    this.setAttribute("open", "");
  };
  HTMLDialogElement.prototype.show = function (this: HTMLDialogElement) {
    this.setAttribute("open", "");
  };
  HTMLDialogElement.prototype.close = function (this: HTMLDialogElement) {
    if (!this.hasAttribute("open")) return;
    this.removeAttribute("open");
    this.dispatchEvent(new Event("close"));
  };
}

// jsdom does not implement the Popover API. Provide minimal versions that track
// open state via a data attribute and fire beforetoggle/toggle, and make
// `:popover-open` matching work by patching `matches`.
if (typeof HTMLElement !== "undefined" && typeof HTMLElement.prototype.showPopover !== "function") {
  const fire = (el: HTMLElement, type: string, oldState: string, newState: string) => {
    const ev = new Event(type) as Event & { oldState: string; newState: string };
    ev.oldState = oldState;
    ev.newState = newState;
    el.dispatchEvent(ev);
  };
  HTMLElement.prototype.showPopover = function (this: HTMLElement) {
    if (this.hasAttribute("data-popover-open")) return;
    fire(this, "beforetoggle", "closed", "open");
    this.setAttribute("data-popover-open", "");
    fire(this, "toggle", "closed", "open");
  };
  HTMLElement.prototype.hidePopover = function (this: HTMLElement) {
    if (!this.hasAttribute("data-popover-open")) return;
    fire(this, "beforetoggle", "open", "closed");
    this.removeAttribute("data-popover-open");
    fire(this, "toggle", "open", "closed");
  };
  HTMLElement.prototype.togglePopover = function (this: HTMLElement) {
    if (this.hasAttribute("data-popover-open")) this.hidePopover();
    else this.showPopover();
    return this.hasAttribute("data-popover-open");
  };
  // Invoker behavior: clicking a [popovertarget] button toggles its target.
  document.addEventListener("click", (e) => {
    const t = e.target;
    if (!(t instanceof Element)) return;
    const invoker = t.closest("[popovertarget]");
    if (!invoker) return;
    const target = document.getElementById(invoker.getAttribute("popovertarget") ?? "");
    if (target) target.togglePopover();
  });
  const origMatches = Element.prototype.matches;
  Element.prototype.matches = function (this: Element, selector: string) {
    if (selector === ":popover-open") return this.hasAttribute("data-popover-open");
    return origMatches.call(this, selector);
  } as typeof Element.prototype.matches;
}
