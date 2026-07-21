export interface AgentInfo {
  id: string;
  name: string;
  description: string;
  run_dirs: string[];
}

export interface MessagePart {
  type: "text";
  text: string;
}

export interface ChatMessage {
  id: string;
  role: "user" | "assistant" | "system" | "developer" | "reasoning" | "activity";
  content: string;
  timestamp?: number;
  activityType?: string;
  stepIndex?: number;
  isReasoning?: boolean;
}

export interface ChatSession {
  chatID: string;
  title: string;
  currentAgent: string;
  runDir: string;
  createdAt?: string;
  updatedAt?: string;
  messages?: ChatMessage[];
}
