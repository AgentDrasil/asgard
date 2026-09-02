import { i18n } from "./index";

interface ServerMessagePattern {
  regex: RegExp;
  translate: (match: RegExpExecArray, lang: string) => string;
}

// Exact match dictionary mapping raw server error/status strings to translation key or custom translated strings
const EXACT_MATCHES: Record<string, { en: string; "zh-CN": string }> = {
  "server restart initiated": {
    en: "Server restart initiated",
    "zh-CN": "服务器重启已触发",
  },
  "agents reloaded": {
    en: "Agents reloaded successfully",
    "zh-CN": "Agent 配置已成功重载",
  },
  "config saved": {
    en: "Configuration saved successfully",
    "zh-CN": "配置已成功保存",
  },
  "agent not found": {
    en: "Agent not found",
    "zh-CN": "未找到指定的 Agent",
  },
  "invalid request body": {
    en: "Invalid request body",
    "zh-CN": "无效的请求内容格式",
  },
  "missing session_id": {
    en: "Session ID is required",
    "zh-CN": "缺少会话 ID 参数",
  },
  "missing host": {
    en: "Host is required in configuration",
    "zh-CN": "配置中缺少 host",
  },
  "missing dsn": {
    en: "DSN is required in configuration",
    "zh-CN": "配置中缺少 dsn",
  },
  "missing agent_dir": {
    en: "Agent directory is required in configuration",
    "zh-CN": "配置中缺少 agent_dir",
  },
  "missing gemini_api_key": {
    en: "Gemini API key is required in configuration",
    "zh-CN": "配置中缺少 gemini_api_key",
  },
  "missing gemini_model_for_chat_title": {
    en: "Gemini model for chat title is required in configuration",
    "zh-CN": "配置中缺少 gemini_model_for_chat_title",
  },
  "cross-origin manage request rejected": {
    en: "Cross-origin manage request rejected",
    "zh-CN": "跨域管理请求已被拒绝",
  },
  "Failed to fetch quota info": {
    en: "Failed to fetch quota info",
    "zh-CN": "获取配额信息失败",
  },
  "Failed to reload agents": {
    en: "Failed to reload agents",
    "zh-CN": "重载 Agent 配置失败",
  },
  "Failed to save configuration": {
    en: "Failed to save configuration",
    "zh-CN": "保存配置失败",
  },
  "Database unavailable in degraded mode": {
    en: "Database unavailable in degraded mode",
    "zh-CN": "降级模式下数据库暂不可用",
  },
  "Network Error": {
    en: "Network connection error",
    "zh-CN": "网络连接异常",
  },
  "Failed to fetch": {
    en: "Failed to connect to backend server",
    "zh-CN": "连接后端服务失败",
  },
};

// Regex patterns for dynamic messages
const PATTERNS: ServerMessagePattern[] = [
  {
    // invalid db: mysql, must be 'pg' or 'sqlite'
    regex: /^invalid db: (.*?), must be 'pg' or 'sqlite'$/i,
    translate: (match, lang) => {
      const db = match[1];
      return lang === "zh-CN"
        ? `无效的数据库类型 "${db}"，必须为 "pg" 或 "sqlite"`
        : `Invalid db "${db}", must be 'pg' or 'sqlite'`;
    },
  },
  {
    // invalid OS "xyz", must be linux, windows, or mac
    regex: /^invalid OS "([^"]+)", must be linux, windows, or mac$/i,
    translate: (match, lang) => {
      const osName = match[1];
      return lang === "zh-CN"
        ? `无效的操作系统类型 "${osName}"，必须为 linux、windows 或 mac`
        : `Invalid OS "${osName}", must be linux, windows, or mac`;
    },
  },
  {
    // unsupported provider "xxx", must be one of [...]
    regex: /^unsupported provider "([^"]+)"/i,
    translate: (match, lang) => {
      const provider = match[1];
      return lang === "zh-CN"
        ? `不支持的 Provider "${provider}"`
        : `Unsupported provider "${provider}"`;
    },
  },
  {
    // invalid ui_lang "xxx", must be 'en' or 'zh-CN'
    regex: /^invalid ui_lang "([^"]+)", must be 'en' or 'zh-CN'/i,
    translate: (match, lang) => {
      const uiLang = match[1];
      return lang === "zh-CN"
        ? `无效的 UI 语言 "${uiLang}"，必须为 'en' 或 'zh-CN'`
        : `Invalid ui_lang "${uiLang}", must be 'en' or 'zh-CN'`;
    },
  },
  {
    // invalid configuration: ...
    regex: /^invalid configuration: (.*)$/i,
    translate: (match, lang) => {
      const inner = match[1];
      const translatedInner = translateServerMessage(inner);
      return lang === "zh-CN"
        ? `配置无效: ${translatedInner}`
        : `Invalid configuration: ${translatedInner}`;
    },
  },
  {
    // agent_father is required as the initial root agent...
    regex: /^agent_father is required as the initial root agent/i,
    translate: (_match, lang) => {
      return lang === "zh-CN"
        ? "缺少必需的根 Agent (agent_father)，请检查 agents 目录"
        : "agent_father is required as the initial root agent, but was not found in the agents directory";
    },
  },
  {
    // file size exceeds limit / file size exceeds 20MB limit
    regex: /file size exceeds (\d+(?:MB|GB|KB)?) limit/i,
    translate: (match, lang) => {
      const limit = match[1];
      return lang === "zh-CN" ? `文件大小超出 ${limit} 限制` : `File size exceeds ${limit} limit`;
    },
  },
];

/**
 * Translates a backend server error or status message into the currently active locale.
 * If no translation mapping is found, it safely falls back to returning the original string.
 */
export function translateServerMessage(rawMessage: string | null | undefined): string {
  if (!rawMessage || typeof rawMessage !== "string") {
    return "";
  }

  const trimmed = rawMessage.trim();
  if (!trimmed) {
    return rawMessage;
  }

  const currentLocale = (i18n.global.locale as any).value || "en";
  const langKey = currentLocale === "zh-CN" ? "zh-CN" : "en";

  // 1. Check exact matches (case-insensitive key comparison)
  const lowerTrimmed = trimmed.toLowerCase();
  for (const [key, translation] of Object.entries(EXACT_MATCHES)) {
    if (key.toLowerCase() === lowerTrimmed) {
      return translation[langKey];
    }
  }

  // 2. Check regex patterns
  for (const pattern of PATTERNS) {
    const match = pattern.regex.exec(trimmed);
    if (match) {
      return pattern.translate(match, langKey);
    }
  }

  // 3. Fallback to original message verbatim
  return rawMessage;
}
