// @vitest-environment jsdom
import { describe, it, expect, beforeEach } from "vitest";
import { setLocale } from "./index";
import { translateServerMessage } from "./serverMessages";

describe("translateServerMessage", () => {
  beforeEach(() => {
    setLocale("en", false);
  });

  describe("English environment", () => {
    it("translates exact match server messages", () => {
      expect(translateServerMessage("server restart initiated")).toBe("Server restart initiated");
      expect(translateServerMessage("agents reloaded")).toBe("Agents reloaded successfully");
      expect(translateServerMessage("config saved")).toBe("Configuration saved successfully");
      expect(translateServerMessage("agent not found")).toBe("Agent not found");
      expect(translateServerMessage("cross-origin manage request rejected")).toBe(
        "Cross-origin manage request rejected",
      );
    });

    it("translates dynamic regex pattern messages", () => {
      expect(translateServerMessage("invalid db: mysql, must be 'pg' or 'sqlite'")).toBe(
        `Invalid db "mysql", must be 'pg' or 'sqlite'`,
      );
      expect(translateServerMessage('invalid OS "xyz", must be linux, windows, or mac')).toBe(
        `Invalid OS "xyz", must be linux, windows, or mac`,
      );
      expect(
        translateServerMessage(
          'unsupported provider "invalid-cli", must be one of [agy opencode simplest]',
        ),
      ).toBe('Unsupported provider "invalid-cli"');
      expect(translateServerMessage("file size exceeds 20MB limit")).toBe(
        "File size exceeds 20MB limit",
      );
    });

    it("safely falls back to raw message for unknown server messages", () => {
      const unknownMsg = "Custom internal system error code 50012";
      expect(translateServerMessage(unknownMsg)).toBe(unknownMsg);
    });

    it("handles null, undefined and empty strings safely", () => {
      expect(translateServerMessage("")).toBe("");
      expect(translateServerMessage(null as any)).toBe("");
      expect(translateServerMessage(undefined as any)).toBe("");
    });
  });

  describe("Simplified Chinese environment", () => {
    beforeEach(() => {
      setLocale("zh-CN", false);
    });

    it("translates exact match server messages to Chinese", () => {
      expect(translateServerMessage("server restart initiated")).toBe("服务器重启已触发");
      expect(translateServerMessage("agents reloaded")).toBe("Agent 配置已成功重载");
      expect(translateServerMessage("config saved")).toBe("配置已成功保存");
      expect(translateServerMessage("agent not found")).toBe("未找到指定的 Agent");
      expect(translateServerMessage("cross-origin manage request rejected")).toBe(
        "跨域管理请求已被拒绝",
      );
      expect(translateServerMessage("Failed to fetch quota info")).toBe("获取配额信息失败");
      expect(translateServerMessage("Database unavailable in degraded mode")).toBe(
        "降级模式下数据库暂不可用",
      );
    });

    it("translates dynamic regex patterns to Chinese", () => {
      expect(translateServerMessage("invalid db: mysql, must be 'pg' or 'sqlite'")).toBe(
        `无效的数据库类型 "mysql"，必须为 "pg" 或 "sqlite"`,
      );
      expect(translateServerMessage('invalid OS "solaris", must be linux, windows, or mac')).toBe(
        `无效的操作系统类型 "solaris"，必须为 linux、windows 或 mac`,
      );
      expect(
        translateServerMessage(
          'unsupported provider "invalid-cli", must be one of [agy opencode simplest]',
        ),
      ).toBe('不支持的 Provider "invalid-cli"');
      expect(translateServerMessage("file size exceeds 20MB limit")).toBe("文件大小超出 20MB 限制");
      expect(
        translateServerMessage(
          "agent_father is required as the initial root agent, but was not found in the agents directory",
        ),
      ).toBe("缺少必需的根 Agent (agent_father)，请检查 agents 目录");
    });

    it("safely falls back to raw string when no match found in Chinese mode", () => {
      const unknownMsg = "Unexpected low-level socket fault 0x8001";
      expect(translateServerMessage(unknownMsg)).toBe(unknownMsg);
    });
  });
});
