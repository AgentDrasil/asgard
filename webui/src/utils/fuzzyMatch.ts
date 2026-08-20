import type { CommandItem } from "../types";

export interface FuzzyMatchResult {
  matches: boolean;
  score: number;
}

const WORD_BOUNDARY_REGEX = /[\s\-_/.:]/;

export function fuzzyMatch(query: string, target: string): FuzzyMatchResult {
  const q = query.trim().toLowerCase();
  const t = target.toLowerCase();

  if (!q) {
    return { matches: true, score: 0 };
  }

  if (!t) {
    return { matches: false, score: 0 };
  }

  // Exact match
  if (t === q) {
    return { matches: true, score: 1000 };
  }

  // Prefix match
  if (t.startsWith(q)) {
    return { matches: true, score: 500 + (100 - Math.min(t.length, 100)) };
  }

  let qIdx = 0;
  let tIdx = 0;
  let score = 0;
  let consecutiveCount = 0;
  let firstMatchIdx = -1;

  while (qIdx < q.length && tIdx < t.length) {
    const qChar = q[qIdx];
    const tChar = t[tIdx];

    if (qChar === tChar) {
      if (firstMatchIdx === -1) {
        firstMatchIdx = tIdx;
      }

      let charScore = 10;

      // Word boundary bonus
      const isWordStart =
        tIdx === 0 ||
        WORD_BOUNDARY_REGEX.test(target[tIdx - 1]) ||
        (target[tIdx] >= "A" &&
          target[tIdx] <= "Z" &&
          target[tIdx - 1] >= "a" &&
          target[tIdx - 1] <= "z");

      if (isWordStart) {
        charScore += 25;
      }

      // Consecutive bonus
      if (consecutiveCount > 0) {
        charScore += consecutiveCount * 15;
      }
      consecutiveCount++;

      score += charScore;
      qIdx++;
    } else {
      consecutiveCount = 0;
    }
    tIdx++;
  }

  const matches = qIdx === q.length;
  if (!matches) {
    return { matches: false, score: 0 };
  }

  // Substring bonus if target includes full query contiguously
  if (t.includes(q)) {
    score += 100;
  }

  // Penalty for target length and distance from start to prefer tighter/earlier matches
  score -= Math.min(target.length, 50);
  if (firstMatchIdx > 0) {
    score -= Math.min(firstMatchIdx * 2, 20);
  }

  return { matches: true, score };
}

export function filterCommands(commands: CommandItem[], query: string): CommandItem[] {
  const trimmed = query.trim();
  if (!trimmed) {
    return [...commands];
  }

  interface ScoredCommand {
    command: CommandItem;
    score: number;
  }

  const scored: ScoredCommand[] = [];

  for (const cmd of commands) {
    const titleResult = fuzzyMatch(trimmed, cmd.title);
    let maxScore = titleResult.matches ? titleResult.score : -Infinity;
    let matched = titleResult.matches;

    if (cmd.keywords && cmd.keywords.length > 0) {
      for (const kw of cmd.keywords) {
        const kwResult = fuzzyMatch(trimmed, kw);
        if (kwResult.matches) {
          matched = true;
          if (kwResult.score > maxScore) {
            maxScore = kwResult.score;
          }
        }
      }
    }

    if (matched) {
      scored.push({ command: cmd, score: maxScore });
    }
  }

  // Stable sort descending by score
  scored.sort((a, b) => b.score - a.score);

  return scored.map((item) => item.command);
}
