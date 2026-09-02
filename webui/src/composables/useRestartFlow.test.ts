// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from "vitest";
import { useRestartFlow } from "./useRestartFlow";
import { useToast } from "./useToast";
import { setLocale } from "../i18n";
import * as api from "../lib/api";

describe("useRestartFlow", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    setLocale("en", false);
    const { clear, clearToastHistory } = useToast();
    clear();
    clearToastHistory();
  });

  it("manages confirmation modal open and close states", () => {
    const flow = useRestartFlow();
    expect(flow.isRestartConfirmOpen.value).toBe(false);

    flow.openRestartConfirm();
    expect(flow.isRestartConfirmOpen.value).toBe(true);

    flow.closeRestartConfirm();
    expect(flow.isRestartConfirmOpen.value).toBe(false);
  });

  it("handles restart rejection gracefully with localized toast", async () => {
    vi.spyOn(api, "restartServer").mockResolvedValue(false);

    const toast = useToast();
    const flow = useRestartFlow();
    flow.openRestartConfirm();

    await flow.triggerRestartWorkflow();

    expect(flow.isRestartConfirmOpen.value).toBe(false);
    expect(flow.isRestarting.value).toBe(false);
    expect(toast.toasts.value).toHaveLength(1);
    expect(toast.toasts.value[0].title).toBe("Restart Failed");
    expect(toast.toasts.value[0].message).toContain("Restart request rejected");
  });
});
