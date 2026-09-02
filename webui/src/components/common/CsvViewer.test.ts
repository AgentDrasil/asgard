// @vitest-environment jsdom
import { describe, it, expect, beforeEach } from "vitest";
import { createApp, nextTick, h } from "vue";
import CsvViewer from "./CsvViewer.vue";
import { i18n } from "../../i18n";

describe("CsvViewer.vue", () => {
  let root: HTMLElement;

  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
  });

  it("parses CSV and renders table headers and rows", async () => {
    const csvContent = `Name,Age,Country\nAlice,30,USA\nBob,25,UK\nCharlie,35,Canada`;
    const app = createApp({
      render() {
        return h(CsvViewer, {
          content: csvContent,
          fileName: "data.csv",
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    expect(root.textContent).toContain("3 cols × 3 rows");
    expect(root.textContent).toContain("Alice");
    expect(root.textContent).toContain("Bob");
    expect(root.textContent).toContain("Charlie");

    const ths = root.querySelectorAll("th");
    // th[0] is # gutter, th[1] is Name, th[2] is Age, th[3] is Country
    expect(ths[1].textContent).toContain("Name");
    expect(ths[2].textContent).toContain("Age");

    // Click Age header to sort asc (numeric)
    ths[2].dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await nextTick();

    let trs = root.querySelectorAll("tbody tr");
    expect(trs[0].textContent).toContain("Bob");
    expect(trs[0].textContent).toContain("25");
    expect(trs[1].textContent).toContain("Alice");
    expect(trs[1].textContent).toContain("30");
    expect(trs[2].textContent).toContain("Charlie");
    expect(trs[2].textContent).toContain("35");

    // Click Age header to sort desc
    ths[2].dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await nextTick();

    trs = root.querySelectorAll("tbody tr");
    expect(trs[0].textContent).toContain("Charlie");
    expect(trs[0].textContent).toContain("35");

    app.unmount();
  });

  it("filters rows when filter input is used", async () => {
    const csvContent = `Name,Age,Country\nAlice,30,USA\nBob,25,UK\nCharlie,35,Canada`;
    const app = createApp({
      render() {
        return h(CsvViewer, {
          content: csvContent,
          fileName: "data.csv",
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const input = root.querySelector("input") as HTMLInputElement;
    input.value = "Canada";
    input.dispatchEvent(new Event("input"));
    await nextTick();

    const trs = root.querySelectorAll("tbody tr");
    expect(trs).toHaveLength(1);
    expect(trs[0].textContent).toContain("Charlie");
    expect(trs[0].textContent).not.toContain("Alice");

    app.unmount();
  });
});
