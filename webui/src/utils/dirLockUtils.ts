import type { DirLockStatus } from "../types";

export type I18nTranslateFn = (key: string, values?: Record<string, any>) => string;

export function formatLockReason(reason?: string, t?: I18nTranslateFn): string {
  if (reason === "running") {
    return t ? t("dirlock.reasonRunning") : "running";
  }
  if (reason === "uncommitted") {
    return t ? t("dirlock.reasonUncommitted") : "uncommitted changes";
  }
  return reason || "";
}

export function formatLockTooltip(dirLock?: DirLockStatus | null, t?: I18nTranslateFn): string {
  if (!dirLock) return "";

  if (dirLock.isWaitingForLock) {
    const holder =
      dirLock.lockedByTitle ||
      dirLock.lockedByChatId ||
      (t ? t("dirlock.anotherSession") : "another session");
    const reason = formatLockReason(dirLock.blockingReason, t);
    const dir = dirLock.lockedDir;

    if (t) {
      return dir
        ? t("dirlock.waitingTooltip", { dir, holder, reason })
        : t("dirlock.waitingTooltipNoDir", { holder, reason });
    }
    return dir
      ? `Queued: Waiting for lock on ${dir} held by ${holder} (${reason})`
      : `Queued: Waiting for lock held by ${holder} (${reason})`;
  }

  if (dirLock.isLocked) {
    const reason = formatLockReason(dirLock.lockReason, t);
    const dir = dirLock.lockedDir;

    if (t) {
      return dir
        ? t("dirlock.lockedTooltip", { reason, dir })
        : t("dirlock.lockedTooltipNoDir", { reason });
    }
    return dir ? `Locked: ${reason} (${dir})` : `Locked: ${reason}`;
  }

  return "";
}

export function formatWaitingBannerText(
  dirLock?: DirLockStatus | null,
  t?: I18nTranslateFn,
): string {
  if (!dirLock || !dirLock.isWaitingForLock) return "";

  const holder =
    dirLock.lockedByTitle ||
    dirLock.lockedByChatId ||
    (t ? t("dirlock.anotherSession") : "another session");
  const reason = formatLockReason(dirLock.blockingReason, t);
  const dir = dirLock.lockedDir;

  if (t) {
    return dir
      ? t("dirlock.waitingBannerText", { dir, holder, reason })
      : t("dirlock.waitingBannerTextNoDir", { holder, reason });
  }
  return dir
    ? `Waiting for directory lock on ${dir} held by ${holder} (${reason})`
    : `Waiting for directory lock held by ${holder} (${reason})`;
}
