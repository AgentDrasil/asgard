export interface AgentInfo {
  id: string;
  type?: "agent" | "workflow" | (string & {});
  name: string;
  description: string;
  icon?: string;
  run_dirs: string[];
  main_agent?: boolean;
  models?: string[];
}

export interface MessagePart {
  type: "text";
  text: string;
}

export interface Attachment {
  name: string;
  path: string;
  size: number;
  mimeType?: string;
}

export interface ChatMessage {
  id: string;
  role:
    | "user"
    | "assistant"
    | "system"
    | "developer"
    | "reasoning"
    | "activity"
    | "tool_call"
    | "tool_result"
    | "ask_user"
    | "error";
  content: string;
  agentName?: string;
  timestamp?: number;
  activityType?: string;
  stepIndex?: number;
  inputTokens?: number;
  maxTokens?: number;
  replied?: boolean;
  replyText?: string;
  targetFiles?: string[];
  artifactFiles?: string[];
  attachments?: Attachment[];
}

export interface QueuedMessage {
  id: string;
  chatId: string;
  prompt: string;
  model?: string;
  createdAt: string;
  updatedAt: string;
}

export interface ChatSession {
  chatID: string;
  title: string;
  currentAgent: string;
  runDir: string;
  gitRoot?: string;
  isRunning?: boolean;
  isWaitingForUser?: boolean;
  isArchived?: boolean;
  createdAt?: string;
  updatedAt?: string;
  messages?: ChatMessage[];
  artifacts?: string[];
  queuedMessages?: QueuedMessage[];
}

export interface DirInfo {
  subdirs: string[];
  gitRoot?: string;
}

export interface GitDiffFile {
  oldPath: string;
  newPath: string;
  /** Change type from git: "A" | "M" | "D" | "R" */
  status?: string;
  oldContent: string;
  newContent: string;
  hunks: string[];
}

export interface GitCommit {
  hash: string;
  shortHash: string;
  author: string;
  authorEmail: string;
  date: string;
  relativeDate: string;
  refs?: string;
  message: string;
}

export interface GitLogResponse {
  commits: GitCommit[];
  currentBranch: string;
  trackingBranch?: string;
  ahead: number;
  behind: number;
  unstashedCount: number;
}

export interface GitActionResult {
  success: boolean;
  output?: string;
  error?: string;
}

export interface FirebaseWebpushWebConfig {
  apiKey?: string;
  authDomain?: string;
  projectId?: string;
  storageBucket?: string;
  messagingSenderId?: string;
  appId?: string;
  vapidKey?: string;
}

export interface SessionEvent {
  eventId: number;
  chatId: string;
  type: "message" | "status" | "title" | "artifact" | "done" | "resync" | "auth_expired" | "queue";
  message?: ChatMessage;
  payload?: Record<string, any>;
  timestamp: number;
}

export interface TriggerAgentMessageParams {
  prompt: string;
  chatId?: string;
  runDir?: string;
  model?: string;
  metadata?: Record<string, any>;
  attachments?: Attachment[];
}

export interface FileTreeEntry {
  name: string;
  path: string;
  isDir: boolean;
  size?: number;
  ext?: string;
  children?: FileTreeEntry[];
  isLoaded?: boolean;
  isExpanded?: boolean;
}

export interface WorkspaceFileContent {
  path: string;
  name: string;
  ext: string;
  size: number;
  content: string;
  isBinary?: boolean;
  updatedAt: string;
}

export type FileScope = "workspace" | "tmp" | "session";

export interface FileSearchResult {
  path: string;
  name: string;
  ext: string;
  size: number;
  scope?: FileScope;
}

export interface CommentEntry {
  filePath: string;
  lineNumber: number;
  lineContent: string;
  comment: string;
  side?: "old" | "new";
}

export type ActiveView = "chat" | "vcs" | "file";

export interface CommandItem {
  id: string;
  title: string;
  category?: string;
  icon?: string;
  shortcut?: string;
  action: () => void | Promise<void>;
}

export interface SystemStatusResponse {
  status: "ok" | "degraded";
  errors?: string[];
  warnings?: string[];
}

export interface ConfigFileResponse {
  path: string;
  content: string;
  exists: boolean;
}

export interface ConfigSaveResponse {
  status?: string;
  message?: string;
  error?: string;
}

export interface SystemLogEntry {
  id: number;
  timestamp: string;
  level: "warn" | "error" | "info";
  source: string;
  message: string;
  details?: string;
}

export interface SystemLogsResponse {
  logs: SystemLogEntry[];
}

export interface ToastItem {
  id: string;
  type: "info" | "success" | "warning" | "error";
  title?: string;
  message: string;
  duration?: number;
  timestamp?: number;
}

export type SupportedOS = "linux" | "windows" | "mac";

export type KeybindingCategory = "navigation" | "panel" | "chat" | "general";

export interface KeybindingActionDef {
  id: string;
  title: string;
  description: string;
  category: KeybindingCategory;
  defaultKeys: Record<SupportedOS, string | string[]>;
}

export type KeybindingsOverrides = Partial<Record<SupportedOS, Record<string, string | string[]>>>;

export interface KeybindingsApiResponse {
  overrides: KeybindingsOverrides;
  exists?: boolean;
  error?: string;
}

export interface VoiceTokenResponse {
  token: string;
  expireTime: string;
  model: string;
}

export interface TranscriptionEvent {
  type: "interim" | "final";
  text: string;
}

export type VoiceInputState = "idle" | "connecting" | "recording" | "stopping" | "error";

export type VoiceErrorCode = "micDenied" | "voiceUnavailable" | "sessionTimeout" | "network";
