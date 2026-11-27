import type { Attachment } from "../../types";
import { appendAccessToken } from "./auth";
import { apiFetch } from "./client";

export async function uploadAttachment(sessionId: string, file: File): Promise<Attachment> {
  const formData = new FormData();
  formData.append("file", file);

  const res = await apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/attachments`, {
    method: "POST",
    body: formData,
  });

  if (!res.ok) {
    const body = await res.json().catch(() => null);
    throw new Error(body?.error || `Failed to upload attachment (${res.status})`);
  }

  const data: Attachment[] = await res.json();
  if (!Array.isArray(data) || data.length === 0) {
    throw new Error("Invalid attachment upload response");
  }
  return data[0];
}

export function getAttachmentUrl(sessionId: string, filename: string): string {
  return appendAccessToken(
    `/api/sessions/${encodeURIComponent(sessionId)}/attachments/${encodeURIComponent(filename)}`,
  );
}
