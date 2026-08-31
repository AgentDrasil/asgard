/**
 * Table sorting utilities supporting string and numeric comparisons,
 * ascending/descending/natural cycle, and attaching interactive click handlers to table headers.
 */

export type SortDirection = "asc" | "desc" | null;

/**
 * Compare two string or numeric values smartly (detecting numbers).
 */
export function compareValues(a: string, b: string, direction: "asc" | "desc"): number {
  const cleanA = a.trim();
  const cleanB = b.trim();

  // Try numeric comparison if both look like numbers (e.g. "123", "45.6", "-7", "1,000", "$50", "20%")
  const numRegex = /^[$€¥£]?\s*[-+]?[0-9]*\.?[0-9]+([eE][-+]?[0-9]+)?%?$/;
  const parseCleanNum = (val: string): number => {
    const stripped = val.replace(/[$€¥£,%]/g, "").trim();
    return Number(stripped);
  };

  const isANum = numRegex.test(cleanA.replace(/,/g, ""));
  const isBNum = numRegex.test(cleanB.replace(/,/g, ""));

  let result = 0;
  if (isANum && isBNum) {
    const valA = parseCleanNum(cleanA);
    const valB = parseCleanNum(cleanB);
    result = valA - valB;
  } else {
    result = cleanA.localeCompare(cleanB, undefined, { numeric: true, sensitivity: "base" });
  }

  return direction === "asc" ? result : -result;
}

const SVG_UNSORTED = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="inline-block shrink-0"><path d="m7 15 5 5 5-5"/><path d="m7 9 5-5 5 5"/></svg>`;
const SVG_ASC = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" class="inline-block shrink-0 text-primary"><path d="m18 15-6-6-6 6"/></svg>`;
const SVG_DESC = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" class="inline-block shrink-0 text-primary"><path d="m6 9 6 6 6-6"/></svg>`;

/**
 * Attach interactive sort handlers to all <table> elements inside a container.
 * Returns a cleanup function that removes all event listeners and injected indicators.
 */
export function setupSortableTables(container: HTMLElement): () => void {
  if (!container) return () => {};

  const cleanups: (() => void)[] = [];
  const tables = container.querySelectorAll<HTMLTableElement>("table");

  tables.forEach((table) => {
    const thead = table.querySelector("thead");
    const tbody = table.querySelector("tbody");
    if (!tbody) return;

    // If no thead, check if first row in table/tbody contains th elements
    let headerRow: HTMLTableRowElement | null = null;
    if (thead) {
      headerRow = thead.querySelector("tr");
    } else {
      const firstRow = table.querySelector("tr");
      if (firstRow && firstRow.querySelector("th")) {
        headerRow = firstRow;
      }
    }

    if (!headerRow) return;

    const ths = Array.from(headerRow.querySelectorAll<HTMLTableCellElement>("th"));
    if (ths.length === 0) return;

    // Store original row order for resetting or initial sorting
    let originalRows = Array.from(tbody.querySelectorAll<HTMLTableRowElement>("tr"));
    // If the header row was in the tbody, exclude it from rows
    if (!thead && originalRows.includes(headerRow)) {
      originalRows = originalRows.filter((r) => r !== headerRow);
    }

    let currentSortCol = -1;
    let currentDirection: SortDirection = null;

    const updateHeaderUI = () => {
      ths.forEach((th, idx) => {
        th.style.cursor = "pointer";
        th.style.userSelect = "none";
        th.setAttribute("title", "Click to sort table by column");

        let iconSpan = th.querySelector<HTMLSpanElement>(".sort-indicator");
        if (!iconSpan) {
          iconSpan = document.createElement("span");
          iconSpan.className =
            "sort-indicator inline-flex items-center align-middle ml-1.5 opacity-40 hover:opacity-100 transition-opacity";
          th.appendChild(iconSpan);
        }

        if (idx === currentSortCol && currentDirection !== null) {
          iconSpan.className =
            "sort-indicator inline-flex items-center align-middle ml-1.5 opacity-100 text-primary transition-opacity";
          iconSpan.innerHTML = currentDirection === "asc" ? SVG_ASC : SVG_DESC;
          th.setAttribute("aria-sort", currentDirection === "asc" ? "ascending" : "descending");
        } else {
          iconSpan.className =
            "sort-indicator inline-flex items-center align-middle ml-1.5 opacity-40 hover:opacity-80 transition-opacity";
          iconSpan.innerHTML = SVG_UNSORTED;
          th.removeAttribute("aria-sort");
        }
      });
    };

    updateHeaderUI();

    ths.forEach((th, colIdx) => {
      const onClick = () => {
        if (currentSortCol === colIdx) {
          if (currentDirection === "asc") {
            currentDirection = "desc";
          } else if (currentDirection === "desc") {
            currentDirection = null;
            currentSortCol = -1;
          } else {
            currentDirection = "asc";
          }
        } else {
          currentSortCol = colIdx;
          currentDirection = "asc";
        }

        updateHeaderUI();

        if (currentDirection === null) {
          // Restore original order
          originalRows.forEach((row) => tbody.appendChild(row));
        } else {
          const sortedRows = [...originalRows].sort((rowA, rowB) => {
            const cellA = rowA.children[colIdx]?.textContent || "";
            const cellB = rowB.children[colIdx]?.textContent || "";
            return compareValues(cellA, cellB, currentDirection!);
          });
          sortedRows.forEach((row) => tbody.appendChild(row));
        }
      };

      th.addEventListener("click", onClick);
      cleanups.push(() => {
        th.removeEventListener("click", onClick);
        const iconSpan = th.querySelector(".sort-indicator");
        if (iconSpan) {
          iconSpan.remove();
        }
      });
    });
  });

  return () => {
    cleanups.forEach((c) => c());
  };
}
