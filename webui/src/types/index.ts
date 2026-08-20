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
}

export interface ChatSession {
  chatID: string;
  title: string;
  currentAgent: string;
  runDir: string;
  gitRoot?: string;
  isRunning?: boolean;
  createdAt?: string;
  updatedAt?: string;
  messages?: ChatMessage[];
  artifacts?: string[];
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
  type: "message" | "status" | "title" | "artifact" | "done" | "resync" | "auth_expired";
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

export interface FileSearchResult {
  path: string;
  name: string;
  ext: string;
  size: number;
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
