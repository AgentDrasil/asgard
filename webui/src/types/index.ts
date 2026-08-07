export interface AgentInfo {
  id: string;
  name: string;
  description: string;
  icon?: string;
  run_dirs: string[];
  main_agent?: boolean;
  models?: string[];
  run_mode?: string;
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
    | "ask_user";
  content: string;
  agentName?: string;
  timestamp?: number;
  activityType?: string;
  stepIndex?: number;
  inputTokens?: number;
  maxTokens?: number;
  replied?: boolean;
  replyText?: string;
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
}

export interface DirInfo {
  subdirs: string[];
  gitRoot?: string;
}

export interface GitDiffFile {
  oldPath: string;
  newPath: string;
  oldContent: string;
  newContent: string;
  hunks: string[];
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
