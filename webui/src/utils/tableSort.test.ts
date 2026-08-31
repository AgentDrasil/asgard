// @vitest-environment jsdom
import { describe, it, expect } from "vitest";
import { compareValues, setupSortableTables } from "./tableSort";

describe("tableSort", () => {
  describe("compareValues", () => {
    it("compares string values ascending and descending", () => {
      expect(compareValues("apple", "banana", "asc")).toBeLessThan(0);
      expect(compareValues("apple", "banana", "desc")).toBeGreaterThan(0);
      expect(compareValues("same", "same", "asc")).toBe(0);
    });

    it("compares numeric strings naturally", () => {
      expect(compareValues("2", "10", "asc")).toBeLessThan(0);
      expect(compareValues("10", "2", "asc")).toBeGreaterThan(0);
      expect(compareValues("-5", "3", "asc")).toBeLessThan(0);
      expect(compareValues("3.14", "3.1415", "asc")).toBeLessThan(0);
      expect(compareValues("$100", "$20", "desc")).toBeLessThan(0);
      expect(compareValues("50%", "10%", "asc")).toBeGreaterThan(0);
    });
  });

  describe("setupSortableTables", () => {
    it("attaches click handlers and sorts table rows on header click", () => {
      const container = document.createElement("div");
      container.innerHTML = `
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Age</th>
            </tr>
          </thead>
          <tbody>
            <tr><td>Bob</td><td>30</td></tr>
            <tr><td>Alice</td><td>25</td></tr>
            <tr><td>Charlie</td><td>35</td></tr>
          </tbody>
        </table>
      `;
      document.body.appendChild(container);

      const cleanup = setupSortableTables(container);

      const ths = container.querySelectorAll("th");
      const tbody = container.querySelector("tbody")!;

      // Click Name (col 0) -> asc
      ths[0].dispatchEvent(new MouseEvent("click", { bubbles: true }));
      let rows = Array.from(tbody.querySelectorAll("tr"));
      expect(rows.map((r) => r.children[0].textContent)).toEqual(["Alice", "Bob", "Charlie"]);
      expect(ths[0].getAttribute("aria-sort")).toBe("ascending");

      // Click Name (col 0) -> desc
      ths[0].dispatchEvent(new MouseEvent("click", { bubbles: true }));
      rows = Array.from(tbody.querySelectorAll("tr"));
      expect(rows.map((r) => r.children[0].textContent)).toEqual(["Charlie", "Bob", "Alice"]);
      expect(ths[0].getAttribute("aria-sort")).toBe("descending");

      // Click Name (col 0) -> natural (original)
      ths[0].dispatchEvent(new MouseEvent("click", { bubbles: true }));
      rows = Array.from(tbody.querySelectorAll("tr"));
      expect(rows.map((r) => r.children[0].textContent)).toEqual(["Bob", "Alice", "Charlie"]);
      expect(ths[0].hasAttribute("aria-sort")).toBe(false);

      // Click Age (col 1) -> asc (numeric)
      ths[1].dispatchEvent(new MouseEvent("click", { bubbles: true }));
      rows = Array.from(tbody.querySelectorAll("tr"));
      expect(rows.map((r) => r.children[1].textContent)).toEqual(["25", "30", "35"]);

      cleanup();
      document.body.removeChild(container);
    });
  });
});
