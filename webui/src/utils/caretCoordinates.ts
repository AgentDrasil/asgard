// Calculates coordinates (left, top, height) of the caret in an HTMLInputElement or HTMLTextAreaElement.
// Modeled after textarea-caret-position algorithms.

export interface CaretCoordinates {
  top: number;
  left: number;
  height: number;
}

const propertiesToCopy = [
  "direction",
  "boxSizing",
  "width",
  "overflowX",
  "overflowY",
  "borderTopWidth",
  "borderRightWidth",
  "borderBottomWidth",
  "borderLeftWidth",
  "borderStyle",
  "paddingTop",
  "paddingRight",
  "paddingBottom",
  "paddingLeft",
  "fontStyle",
  "fontVariant",
  "fontWeight",
  "fontStretch",
  "fontSize",
  "fontSizeAdjust",
  "lineHeight",
  "fontFamily",
  "textAlign",
  "textTransform",
  "textIndent",
  "textDecoration",
  "letterSpacing",
  "wordSpacing",
  "tabSize",
  "MozTabSize",
] as const;

export function getCaretCoordinates(
  element: HTMLTextAreaElement | HTMLInputElement,
  position: number,
): CaretCoordinates {
  if (typeof window === "undefined" || typeof document === "undefined") {
    return { top: 0, left: 0, height: 20 };
  }

  const isInput = element.tagName === "INPUT";

  const div = document.createElement("div");
  div.id = "input-textarea-caret-position-mirror-div";
  document.body.appendChild(div);

  const style = div.style;
  const computed = window.getComputedStyle(element);

  style.whiteSpace = isInput ? "nowrap" : "pre-wrap";
  if (!isInput) {
    style.wordWrap = "break-word";
  }

  style.position = "absolute";
  style.visibility = "hidden";
  style.overflow = "hidden";

  propertiesToCopy.forEach((prop) => {
    // @ts-expect-error computed style string indexing
    style[prop] = computed[prop];
  });

  style.top = "0px";
  style.left = "-9999px";

  div.textContent = element.value.substring(0, position);

  if (isInput) {
    div.textContent = div.textContent.replace(/\s/g, "\u00a0");
  }

  const span = document.createElement("span");
  span.textContent = element.value.substring(position) || ".";
  div.appendChild(span);

  const lineHeight =
    parseInt(computed.lineHeight, 10) || parseInt(computed.fontSize, 10) * 1.2 || 20;

  const coordinates: CaretCoordinates = {
    top: span.offsetTop + parseInt(computed.borderTopWidth, 10) - element.scrollTop,
    left: span.offsetLeft + parseInt(computed.borderLeftWidth, 10) - element.scrollLeft,
    height: lineHeight,
  };

  document.body.removeChild(div);
  return coordinates;
}
