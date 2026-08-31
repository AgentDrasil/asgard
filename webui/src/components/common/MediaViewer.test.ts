// @vitest-environment jsdom
import { describe, it, expect, beforeEach } from "vitest";
import { createApp, nextTick, h } from "vue";
import MediaViewer from "./MediaViewer.vue";

describe("MediaViewer.vue", () => {
  let root: HTMLElement;

  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
  });

  it("renders image view and handles zoom controls", async () => {
    const app = createApp({
      render() {
        return h(MediaViewer, {
          src: "/api/files/content?session_id=s1&path=test.png&raw=1",
          fileName: "test.png",
          fileExt: "png",
          fileSize: 1024,
          mediaCategory: "image",
        });
      },
    });
    app.mount(root);
    await nextTick();

    const img = root.querySelector("img");
    expect(img).not.toBeNull();
    expect(img?.getAttribute("src")).toBe("/api/files/content?session_id=s1&path=test.png&raw=1");
    expect(root.textContent).toContain("test.png");
    expect(root.textContent).toContain("100%");

    // Click Zoom In button
    const zoomInBtn = root.querySelector('button[title="Zoom In"]') as HTMLButtonElement;
    expect(zoomInBtn).not.toBeNull();
    zoomInBtn?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await nextTick();
    expect(root.textContent).toContain("125%");
    expect(img?.getAttribute("style")).toContain("scale(1.25)");

    // Click Zoom Out button
    const zoomOutBtn = root.querySelector('button[title="Zoom Out"]') as HTMLButtonElement;
    expect(zoomOutBtn).not.toBeNull();
    zoomOutBtn?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await nextTick();
    expect(root.textContent).toContain("100%");
    expect(img?.getAttribute("style")).toContain("scale(1)");

    // Click Rotate button
    const rotateBtn = root.querySelector('button[title="Rotate"]') as HTMLButtonElement;
    expect(rotateBtn).not.toBeNull();
    rotateBtn?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await nextTick();
    expect(img?.getAttribute("style")).toContain("rotate(90deg)");

    // Test Fit to Window
    const fitBtn = root.querySelector('button[title="Fit to Window"]') as HTMLButtonElement;
    expect(fitBtn).not.toBeNull();

    // Mock natural dimensions on img and client dimensions on container
    const container = root.querySelector(".checkerboard-bg") as HTMLElement;
    Object.defineProperty(container, "clientWidth", { value: 832, configurable: true });
    Object.defineProperty(container, "clientHeight", { value: 632, configurable: true });
    Object.defineProperty(img, "naturalWidth", { value: 1600, configurable: true });
    Object.defineProperty(img, "naturalHeight", { value: 1200, configurable: true });

    fitBtn?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await nextTick();
    // (832 - 32) / 1600 = 800 / 1600 = 0.5, (632 - 32) / 1200 = 600 / 1200 = 0.5 -> 50%
    expect(root.textContent).toContain("50%");
    expect(img?.getAttribute("style")).toContain("scale(0.5)");

    // Verify download anchor
    const downloadLink = root.querySelector('a[title="Download / Open Raw"]');
    expect(downloadLink?.getAttribute("download")).toBe("test.png");

    app.unmount();
  });

  it("renders video player and handles error state", async () => {
    const app = createApp({
      render() {
        return h(MediaViewer, {
          src: "/api/files/content?session_id=s1&path=test.mp4&raw=1",
          fileName: "test.mp4",
          fileExt: "mp4",
          fileSize: 1048576,
          mediaCategory: "video",
        });
      },
    });
    app.mount(root);
    await nextTick();

    const video = root.querySelector("video");
    expect(video).not.toBeNull();
    expect(video?.getAttribute("src")).toBe("/api/files/content?session_id=s1&path=test.mp4&raw=1");
    expect(video?.hasAttribute("controls")).toBe(true);
    expect(root.textContent).toContain("test.mp4");

    const downloadLink = root.querySelector('a[title="Download / Open Raw"]');
    expect(downloadLink?.getAttribute("download")).toBe("test.mp4");

    // Trigger video error
    video?.dispatchEvent(new Event("error"));
    await nextTick();
    expect(root.textContent).toContain("Failed to load video");
    expect(root.textContent).toContain("Open Directly");

    app.unmount();
  });

  it("renders audio player when mediaCategory is audio", async () => {
    const app = createApp({
      render() {
        return h(MediaViewer, {
          src: "/api/files/content?session_id=s1&path=test.mp3&raw=1",
          fileName: "test.mp3",
          fileExt: "mp3",
          fileSize: 2048,
          mediaCategory: "audio",
        });
      },
    });
    app.mount(root);
    await nextTick();

    const audio = root.querySelector("audio");
    expect(audio).not.toBeNull();
    expect(audio?.getAttribute("src")).toBe("/api/files/content?session_id=s1&path=test.mp3&raw=1");
    expect(audio?.hasAttribute("controls")).toBe(true);
    expect(root.textContent).toContain("test.mp3");
    expect(root.textContent).toContain("Download Audio");

    const downloadLink = root.querySelector("a.btn-outline");
    expect(downloadLink?.getAttribute("download")).toBe("test.mp3");

    app.unmount();
  });

  it("renders iframe when mediaCategory is pdf", async () => {
    const app = createApp({
      render() {
        return h(MediaViewer, {
          src: "/api/files/content?session_id=s1&path=doc.pdf&raw=1",
          fileName: "doc.pdf",
          fileExt: "pdf",
          fileSize: 50000,
          mediaCategory: "pdf",
        });
      },
    });
    app.mount(root);
    await nextTick();

    const iframe = root.querySelector("iframe");
    expect(iframe).not.toBeNull();
    expect(iframe?.getAttribute("src")).toBe("/api/files/content?session_id=s1&path=doc.pdf&raw=1");
    expect(root.textContent).toContain("doc.pdf");
    expect(root.textContent).toContain("Open in Tab");

    const downloadLink = root.querySelector('a[title="Download"]');
    expect(downloadLink?.getAttribute("download")).toBe("doc.pdf");

    app.unmount();
  });

  it("renders binary fallback card when mediaCategory is binary", async () => {
    const app = createApp({
      render() {
        return h(MediaViewer, {
          src: "/api/files/content?session_id=s1&path=app.bin&raw=1",
          fileName: "app.bin",
          fileExt: "bin",
          fileSize: 12345,
          mediaCategory: "binary",
        });
      },
    });
    app.mount(root);
    await nextTick();

    expect(root.querySelector("img")).toBeNull();
    expect(root.querySelector("video")).toBeNull();
    expect(root.querySelector("audio")).toBeNull();
    expect(root.querySelector("iframe")).toBeNull();
    expect(root.textContent).toContain("app.bin");
    expect(root.textContent).toContain("Binary file");
    expect(root.textContent).toContain("Download File");

    const downloadLink = root.querySelector("a.btn-primary");
    expect(downloadLink?.getAttribute("download")).toBe("app.bin");

    app.unmount();
  });

  it("renders binary fallback card without download CTA when src is empty", async () => {
    const app = createApp({
      render() {
        return h(MediaViewer, {
          src: "",
          fileName: "data.bin",
          fileExt: "bin",
          fileSize: 1024,
          mediaCategory: "binary",
        });
      },
    });
    app.mount(root);
    await nextTick();

    expect(root.textContent).toContain("data.bin");
    expect(root.textContent).toContain("Binary file");
    expect(root.textContent).not.toContain("Download File");
    expect(root.querySelector("a")).toBeNull();

    app.unmount();
  });
});
