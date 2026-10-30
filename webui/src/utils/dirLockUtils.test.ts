import { describe, it, expect } from "vitest";
import { formatLockReason, formatLockTooltip, formatWaitingBannerText } from "./dirLockUtils";
import type { DirLockStatus } from "../types";

describe("dirLockUtils", () => {
  describe("formatLockReason", () => {
    it("formats running and uncommitted without t", () => {
      expect(formatLockReason("running")).toBe("running");
      expect(formatLockReason("uncommitted")).toBe("uncommitted changes");
      expect(formatLockReason("other")).toBe("other");
      expect(formatLockReason(undefined)).toBe("");
    });

    it("formats with translation function", () => {
      const mockT = (key: string) => {
        if (key === "dirlock.reasonRunning") return "运行中";
        if (key === "dirlock.reasonUncommitted") return "未提交更改";
        return key;
      };
      expect(formatLockReason("running", mockT)).toBe("运行中");
      expect(formatLockReason("uncommitted", mockT)).toBe("未提交更改");
    });
  });

  describe("formatLockTooltip", () => {
    it("returns empty string if dirLock is null or undefined", () => {
      expect(formatLockTooltip(null)).toBe("");
      expect(formatLockTooltip(undefined)).toBe("");
    });

    it("formats waiting for lock with dir and holder without t", () => {
      const lock: DirLockStatus = {
        isWaitingForLock: true,
        lockedDir: "/home/user/project",
        lockedByTitle: "Feature Chat",
        blockingReason: "uncommitted",
      };
      expect(formatLockTooltip(lock)).toBe(
        "Queued: Waiting for lock on /home/user/project held by Feature Chat (uncommitted changes)",
      );
    });

    it("formats waiting for lock falling back to chatId", () => {
      const lock: DirLockStatus = {
        isWaitingForLock: true,
        lockedDir: "/home/user/project",
        lockedByChatId: "chat-xyz",
        blockingReason: "running",
      };
      expect(formatLockTooltip(lock)).toBe(
        "Queued: Waiting for lock on /home/user/project held by chat-xyz (running)",
      );
    });

    it("formats waiting for lock without dir", () => {
      const lock: DirLockStatus = {
        isWaitingForLock: true,
        lockedByTitle: "Feature Chat",
        blockingReason: "running",
      };
      expect(formatLockTooltip(lock)).toBe(
        "Queued: Waiting for lock held by Feature Chat (running)",
      );
    });

    it("formats isLocked without t", () => {
      const lock: DirLockStatus = {
        isLocked: true,
        lockedDir: "/repo",
        lockReason: "running",
      };
      expect(formatLockTooltip(lock)).toBe("Locked: running (/repo)");
    });

    it("formats isLocked without dir", () => {
      const lock: DirLockStatus = {
        isLocked: true,
        lockReason: "uncommitted",
      };
      expect(formatLockTooltip(lock)).toBe("Locked: uncommitted changes");
    });

    it("formats with custom t function", () => {
      const mockT = (key: string, values?: Record<string, any>) => {
        if (key === "dirlock.waitingTooltip") {
          return `排队中：等待 ${values?.holder} 释放目录 ${values?.dir} 的锁（${values?.reason}）`;
        }
        if (key === "dirlock.lockedTooltip") {
          return `已锁定：${values?.reason}（${values?.dir}）`;
        }
        if (key === "dirlock.reasonRunning") return "运行中";
        return key;
      };

      const waitingLock: DirLockStatus = {
        isWaitingForLock: true,
        lockedDir: "/my-dir",
        lockedByTitle: "Other Task",
        blockingReason: "running",
      };
      expect(formatLockTooltip(waitingLock, mockT)).toBe(
        "排队中：等待 Other Task 释放目录 /my-dir 的锁（运行中）",
      );

      const heldLock: DirLockStatus = {
        isLocked: true,
        lockedDir: "/my-dir",
        lockReason: "running",
      };
      expect(formatLockTooltip(heldLock, mockT)).toBe("已锁定：运行中（/my-dir）");
    });
  });

  describe("formatWaitingBannerText", () => {
    it("returns empty string when not waiting for lock", () => {
      expect(formatWaitingBannerText(null)).toBe("");
      expect(formatWaitingBannerText({ isLocked: true })).toBe("");
    });

    it("formats waiting banner text without t", () => {
      const lock: DirLockStatus = {
        isWaitingForLock: true,
        lockedDir: "/home/user/project",
        lockedByTitle: "Feature Chat",
        blockingReason: "running",
      };
      expect(formatWaitingBannerText(lock)).toBe(
        "Waiting for directory lock on /home/user/project held by Feature Chat (running)",
      );
    });

    it("formats waiting banner text without dir", () => {
      const lock: DirLockStatus = {
        isWaitingForLock: true,
        lockedByTitle: "Feature Chat",
        blockingReason: "running",
      };
      expect(formatWaitingBannerText(lock)).toBe(
        "Waiting for directory lock held by Feature Chat (running)",
      );
    });
  });
});
