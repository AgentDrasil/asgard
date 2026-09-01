import Fuse, { type IFuseOptions } from "fuse.js";
import type { CommandItem } from "../types";

const fuseOptions: IFuseOptions<CommandItem> = {
  includeScore: true,
  shouldSort: true,
  threshold: 0.35, // Balanced threshold for command palette matching
  ignoreLocation: true,
  keys: [
    { name: "title", weight: 3 },
    { name: "category", weight: 0.5 },
  ],
};

/**
 * Filter and rank commands using Fuse.js.
 */
export function filterCommands(commands: CommandItem[], query: string): CommandItem[] {
  const trimmed = query.trim();
  if (!trimmed) {
    return [...commands].sort((a, b) => a.title.localeCompare(b.title));
  }

  const fuse = new Fuse(commands, fuseOptions);
  const results = fuse.search(trimmed);
  return results.map((result) => result.item);
}
