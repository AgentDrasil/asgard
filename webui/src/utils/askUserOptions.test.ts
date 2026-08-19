import { describe, it, expect } from "vitest";
import { parseOptions } from "./askUserOptions";

describe("askUserOptions", () => {
  describe("parseOptions", () => {
    it("parses standard slash-separated options", () => {
      const content = "Please approve the plan.\nOptions: Proceed / Request Changes / Cancel";
      expect(parseOptions(content)).toEqual(["Proceed", "Request Changes", "Cancel"]);
    });

    it("parses options case-insensitively", () => {
      const content = "options: Yes / No";
      expect(parseOptions(content)).toEqual(["Yes", "No"]);
    });

    it("handles whitespace and extra slashes gracefully", () => {
      const content = "Options:   Approve   /   Reject  /   /  ";
      expect(parseOptions(content)).toEqual(["Approve", "Reject"]);
    });

    it("returns empty array when no options are present", () => {
      expect(parseOptions("What is your name?")).toEqual([]);
      expect(parseOptions("")).toEqual([]);
    });

    it("ignores subsequent lines after Options line", () => {
      const content = "Options: Option A / Option B\nAdditional notes here";
      expect(parseOptions(content)).toEqual(["Option A", "Option B"]);
    });
  });
});
