import { render } from "pele";

let idCounter = 0;

export interface RenderResult {
  svg?: string;
  error?: string;
}

/**
 * Generate a unique ID prefix for SVG elements to prevent clipPath and marker collisions.
 */
export function generateUniqueId(prefix: string = "mermaid-svg"): string {
  idCounter += 1;
  return `${prefix}-${Date.now().toString(36)}-${idCounter.toString(36)}`;
}

/**
 * Safely render a Mermaid diagram into an SVG string using pele.
 * Catches syntax and rendering errors without throwing uncaught exceptions.
 */
export async function renderDiagram(code: string, idPrefix?: string): Promise<RenderResult> {
  const trimmed = (code || "").trim();
  if (!trimmed) {
    return {};
  }

  const id = generateUniqueId(idPrefix || "mermaid");

  try {
    const { svg } = render(trimmed, {
      idPrefix: id,
      responsive: false,
    });
    return { svg };
  } catch (err: unknown) {
    const errorMessage = err instanceof Error ? err.message : String(err);
    return { error: errorMessage };
  }
}

export function useMermaid() {
  return {
    renderDiagram,
  };
}
