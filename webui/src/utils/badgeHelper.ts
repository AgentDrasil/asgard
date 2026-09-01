function hasWord(text: string, words: string[]): boolean {
  for (const w of words) {
    const re = new RegExp(`(^|[^A-Z0-9])${w}([^A-Z0-9]|$)`, "i");
    if (re.test(text)) return true;
  }
  return false;
}

/**
 * Resolves a high-contrast, visually appealing badge class based on the text value and column configuration.
 */
export function resolveBadgeClass(val: string | number | undefined | null, col?: any): string {
  if (val === undefined || val === null || val === "") return "badge-tag-slate";

  const rawStr = String(val).trim();

  // If column has an explicit badgeColorMap matching raw or stripped text, use it
  if (col?.badgeColorMap) {
    if (col.badgeColorMap[rawStr]) return col.badgeColorMap[rawStr];
    if (col.badgeColorMap[rawStr.toUpperCase()]) return col.badgeColorMap[rawStr.toUpperCase()];
  }

  const clean = rawStr.toUpperCase();

  // 1. Exact Financial Ratings & Grades
  if (["A+", "AAA", "AA", "A"].includes(clean)) return "badge-tag-emerald font-bold";
  if (["A-", "BBB"].includes(clean)) return "badge-tag-teal font-semibold";
  if (["BB+", "BB", "BB-", "B+", "B", "B-", "CCC", "CC", "C"].includes(clean))
    return "badge-tag-amber font-semibold";
  if (["D", "F"].includes(clean)) return "badge-tag-rose font-bold";

  // 2. Tax Regimes / Accounts
  if (
    clean.includes("TAX-FREE") ||
    clean === "FREE" ||
    hasWord(clean, ["TFSA", "ROTH"])
  ) {
    return "badge-tag-emerald font-semibold";
  }
  if (
    clean.includes("TAX-DEFERRED") ||
    clean === "DEFERRED" ||
    hasWord(clean, ["RRSP", "401K", "IRA", "PENSION", "403B", "SEP"])
  ) {
    return "badge-tag-sky font-semibold";
  }
  if (clean.includes("TAXABLE") || clean.includes("NON-REG") || hasWord(clean, ["MARGIN"])) {
    return "badge-tag-amber font-semibold";
  }
  if (hasWord(clean, ["MASTER", "ADVISOR", "TRUST", "CORPORATE"])) {
    return "badge-tag-purple font-semibold";
  }

  // 3. Asset Classes
  if (
    clean.includes("US EQUIT") ||
    clean.includes("U.S. EQUIT") ||
    clean.includes("US STOCK") ||
    clean === "EQUITY" ||
    clean === "EQUITIES"
  ) {
    return "badge-tag-emerald font-semibold";
  }
  if (
    clean.includes("INTL") ||
    clean.includes("INTERNATIONAL") ||
    clean.includes("GLOBAL") ||
    clean.includes("EMERGING") ||
    clean.includes("DEVELOPED")
  ) {
    return "badge-tag-cyan font-semibold";
  }
  if (clean.includes("CANADIAN") || clean.includes("CANADA") || hasWord(clean, ["TSX"])) {
    return "badge-tag-rose font-semibold";
  }
  if (
    clean.includes("DIGITAL ASSET") ||
    clean.includes("CRYPTO") ||
    hasWord(clean, ["BITCOIN", "BTC", "ETH", "ETHEREUM", "SOL", "SOLANA"])
  ) {
    return "badge-tag-purple font-semibold";
  }
  if (
    clean.includes("MONEY MARKET") ||
    hasWord(clean, ["CASH", "LIQUIDITY", "SAVINGS", "USD", "CAD"])
  ) {
    return "badge-tag-amber font-semibold";
  }
  if (
    clean.includes("FIXED INCOME") ||
    clean.includes("TREASUR") ||
    hasWord(clean, ["BOND", "BONDS", "DEBT", "GIC"])
  ) {
    return "badge-tag-indigo font-semibold";
  }
  if (clean.includes("REAL ESTATE") || hasWord(clean, ["REIT", "REITS"])) {
    return "badge-tag-orange font-semibold";
  }
  if (clean.includes("COMMODIT") || hasWord(clean, ["GOLD", "SILVER", "OIL", "ENERGY"])) {
    return "badge-tag-yellow font-semibold";
  }

  // 4. Status / Action / Direction
  if (
    clean.includes("OVERWEIGHT") ||
    clean.includes("OPTIMAL") ||
    clean.includes("SUCCESS") ||
    hasWord(clean, ["BUY", "PASS", "ACTIVE", "APPROVED", "COMPLETED"])
  ) {
    return "badge-tag-emerald font-semibold";
  }
  if (
    clean.includes("NEUTRAL") ||
    clean.includes("BALANCED") ||
    hasWord(clean, ["HOLD", "PENDING", "IN PROGRESS", "STABLE"])
  ) {
    return "badge-tag-sky font-semibold";
  }
  if (
    clean.includes("UNDERWEIGHT") ||
    clean.includes("CAUTION") ||
    hasWord(clean, ["SELL", "WARN", "WARNING", "MEDIUM RISK"])
  ) {
    return "badge-tag-amber font-semibold";
  }
  if (
    clean.includes("HIGH RISK") ||
    hasWord(clean, ["CRITICAL", "FAIL", "FAILED", "DANGER", "ERROR"])
  ) {
    return "badge-tag-rose font-semibold";
  }

  // 5. Default Fallback
  return "badge-tag-slate font-medium";
}
