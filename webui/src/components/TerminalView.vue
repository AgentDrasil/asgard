<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from "vue";
import { Terminal } from "@xterm/xterm";
import type { ITheme } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import "@xterm/xterm/css/xterm.css";

const props = defineProps<{
  sessionId: string;
  terminalType: "session" | "sidebar";
  theme: ITheme;
}>();

// ttyd WebSocket protocol command bytes (see ttyd src/server.h).
// NOTE: JSON_DATA is documented for completeness but intentionally unused here.
// ttyd's first client message is sent as raw JSON (no command-byte prefix) to
// spawn the shell, so we send it directly via ws.send(JSON.stringify(...)).
const CMD = {
  // client -> server
  INPUT: 0x30, // '0'
  RESIZE: 0x31, // '1'
  JSON_DATA: 0x7b, // '{'  (first message: spawn shell + initial size)
  // server -> client
  OUTPUT: 0x30, // '0'
  SET_TITLE: 0x31, // '1'
  SET_PREFS: 0x32, // '2'
} as const;

// Host background tracks the active terminal theme so the .xterm padding area
// matches the rendered content (otherwise the host shows as a border).
const hostBg = computed(() => props.theme.background ?? "#000000");

const hostRef = ref<HTMLDivElement | null>(null);

let term: Terminal | null = null;
let fitAddon: FitAddon | null = null;
let ws: WebSocket | null = null;
let ro: ResizeObserver | null = null;
const textEncoder = new TextEncoder();

const wsUrl = () => {
  const key =
    props.terminalType === "sidebar" || !props.sessionId ? "sidebar" : `agent-${props.sessionId}`;
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${window.location.host}/api/ttyd/${key}/ws`;
};

const send = (cmdByte: number, payload: Uint8Array | string) => {
  if (!ws || ws.readyState !== WebSocket.OPEN) return;
  const data = typeof payload === "string" ? textEncoder.encode(payload) : payload;
  const buf = new Uint8Array(data.length + 1);
  buf[0] = cmdByte;
  buf.set(data, 1);
  ws.send(buf);
};

const sendResize = (cols: number, rows: number) =>
  send(CMD.RESIZE, JSON.stringify({ columns: cols, rows: rows }));

const safeFit = () => {
  const el = hostRef.value;
  if (!el || !fitAddon || el.clientWidth === 0 || el.clientHeight === 0) return;
  try {
    fitAddon.fit();
  } catch {
    // ignore fit errors during teardown
  }
};

const connect = () => {
  ws = new WebSocket(wsUrl(), ["tty"]);
  ws.binaryType = "arraybuffer";
  ws.onopen = () => {
    if (!term) return;
    // First message: JSON_DATA -> server spawns the shell at the given size.
    ws?.send(JSON.stringify({ AuthToken: "", columns: term.cols, rows: term.rows }));
  };
  ws.onmessage = (ev: MessageEvent) => {
    if (!term) return;
    const raw = new Uint8Array(ev.data as ArrayBuffer);
    const cmd = raw[0];
    const rest = raw.subarray(1);
    switch (cmd) {
      case CMD.OUTPUT:
        term.write(rest);
        break;
      case CMD.SET_TITLE:
        // titles not surfaced in the embedded UI
        break;
      case CMD.SET_PREFS:
        // we control the theme/options locally; ignore server prefs
        break;
    }
  };
  ws.onclose = () => {
    if (term) term.write("\r\n\x1b[31m[connection closed]\x1b[0m\r\n");
  };
  ws.onerror = () => {};
};

onMounted(() => {
  term = new Terminal({
    fontFamily: '"JetBrains Mono", ui-monospace, SFMono-Regular, Menlo, monospace',
    fontSize: 14,
    cursorBlink: true,
    theme: props.theme,
    scrollback: 5000,
    allowProposedApi: true,
    overviewRuler: { width: 6 },
  });
  fitAddon = new FitAddon();
  term.loadAddon(fitAddon);
  term.loadAddon(new WebLinksAddon());
  term.open(hostRef.value!);
  safeFit();

  term.onData((data) => send(CMD.INPUT, data));
  term.onResize(({ cols, rows }) => sendResize(cols, rows));
  // Copy-on-select: browsers intercept the terminal's Ctrl/Cmd+C for text
  // copy, making an explicit shortcut unusable here. Select-to-copy is the
  // standard convention for embedded web terminals (e.g. ttyd, xterm.js).
  term.onSelectionChange(() => {
    const text = term!.getSelection();
    if (!text) return;
    navigator.clipboard?.writeText(text).catch(() => {});
  });

  ro = new ResizeObserver(() => safeFit());
  ro.observe(hostRef.value!);

  connect();

  // Don't auto-focus on touch devices to avoid popping the on-screen keyboard.
  if (!window.matchMedia("(pointer: coarse)").matches) term.focus();
});

watch(
  () => props.theme,
  (t) => {
    if (term) term.options.theme = t;
  },
);

onUnmounted(() => {
  ro?.disconnect();
  if (ws) {
    ws.onclose = null;
    ws.close();
  }
  term?.dispose();
  term = null;
  fitAddon = null;
  ws = null;
});
</script>

<template>
  <div
    class="relative h-full w-full overflow-hidden flex flex-col"
    :style="{ backgroundColor: hostBg }"
  >
    <div
      ref="hostRef"
      class="terminal-host flex-1 w-full"
      :style="{ backgroundColor: hostBg, '--term-bg': hostBg }"
    />
  </div>
</template>

<style scoped>
/* FitAddon subtracts the terminal element's own padding from the host size,
   so padding goes on .xterm (not the host) for correct row/col fitting. */
.terminal-host :deep(.xterm) {
  padding: 8px;
}
/* xterm only paints the theme background on the cell canvas + .xterm-viewport;
   the surrounding layers (.xterm-screen, .xterm-scrollable-element, the padding
   ring) default to #000 and show a black border. Drive them all from a single
   CSS var bound to the active theme so the whole host matches. */
.terminal-host :deep(.xterm),
.terminal-host :deep(.xterm-screen),
.terminal-host :deep(.xterm-scrollable-element),
.terminal-host :deep(.xterm-viewport) {
  background-color: var(--term-bg, #000) !important;
}
.terminal-host :deep(.xterm-viewport) {
  height: 100%;
  scrollbar-width: thin;
  scrollbar-color: color-mix(in oklab, var(--color-base-content) 20%, transparent) transparent;
}
.terminal-host :deep(.xterm-viewport::-webkit-scrollbar) {
  width: 6px;
  height: 6px;
}
.terminal-host :deep(.xterm-viewport::-webkit-scrollbar-track) {
  background: transparent;
}
.terminal-host :deep(.xterm-viewport::-webkit-scrollbar-thumb) {
  background: color-mix(in oklab, var(--color-base-content) 20%, transparent);
  border-radius: 4px;
}
.terminal-host :deep(.xterm-viewport::-webkit-scrollbar-thumb:hover) {
  background: color-mix(in oklab, var(--color-base-content) 35%, transparent);
}
.terminal-host :deep(.xterm-scrollable-element > .scrollbar) {
  width: 6px !important;
}
.terminal-host :deep(.xterm-scrollable-element > .scrollbar > .slider) {
  border-radius: 4px !important;
  background: color-mix(in oklab, var(--color-base-content) 20%, transparent) !important;
}
.terminal-host :deep(.xterm-scrollable-element > .scrollbar > .slider:hover) {
  background: color-mix(in oklab, var(--color-base-content) 35%, transparent) !important;
}
/* Hide xterm's overview ruler canvas (decoration minimap channel) on the right side,
   which presents as an unused white vertical line when decoration markers are inactive. */
.terminal-host :deep(.xterm-decoration-overview-ruler) {
  display: none !important;
}
</style>
