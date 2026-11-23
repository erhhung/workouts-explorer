import { type RefObject, useLayoutEffect } from "react";
import { DIALOG_PIXEL_ALIGNMENT_EVENT } from "./dialogPixelAlignment";

export function followOSMProgress(viewport: HTMLElement, current: number, total?: number) {
  if (current <= 20) {
    viewport.scrollTop = 0;
  } else if (total !== undefined && current >= total) {
    viewport.scrollTop = viewport.scrollHeight;
    return;
  } else {
    const first = viewport.querySelector<HTMLElement>(`[data-task-order="${current - 19}"]`);
    const header = viewport.querySelector("thead");
    if (!first || !header) return;
    const top = viewport.getBoundingClientRect().top + viewport.clientTop;
    viewport.scrollTop = Math.max(
      0,
      viewport.scrollTop +
        first.getBoundingClientRect().top -
        top -
        header.getBoundingClientRect().height,
    );
  }
  // A short/mobile viewport may fit fewer than twenty rows.
  const row = viewport.querySelector<HTMLElement>(`[data-task-order="${current}"]`);
  if (row && viewport.clientHeight > 0) {
    const bottom =
      viewport.getBoundingClientRect().top + viewport.clientTop + viewport.clientHeight;
    if (row.getBoundingClientRect().bottom > bottom)
      viewport.scrollTop += row.getBoundingClientRect().bottom - bottom;
  }
}

export function useOSMProgressFollow(
  scroll: RefObject<HTMLDivElement | null>,
  jobId: string | undefined,
  current: number,
  total: number | undefined,
  state: string | undefined,
) {
  useLayoutEffect(() => {
    if (!jobId) return;
    let active = true,
      frame: number | undefined,
      waitingForAnimation = false;
    let observed: HTMLDivElement | null = null,
      dialog: HTMLElement | null = null,
      sizeSignature = "";
    const sizes = () => {
      const table = observed?.querySelector("table");
      return `${observed?.clientWidth}:${observed?.clientHeight}:${table?.getBoundingClientRect().width}:${table?.getBoundingClientRect().height}`;
    };
    const update = () => {
      if (!active) return;
      const viewport = scroll.current;
      if (!viewport) {
        schedule();
        return;
      }
      if (viewport !== observed) {
        observer?.disconnect();
        dialog?.removeEventListener(DIALOG_PIXEL_ALIGNMENT_EVENT, schedule);
        observed = viewport;
        dialog = viewport.closest<HTMLElement>('[role="dialog"]');
        sizeSignature = sizes();
        observer?.observe(viewport);
        const table = viewport.querySelector("table");
        if (table) observer?.observe(table);
        dialog?.addEventListener(DIALOG_PIXEL_ALIGNMENT_EVENT, schedule);
      }
      const animations = dialog
        ?.getAnimations?.()
        .filter((animation) => animation.playState === "running");
      if (animations?.length) {
        if (!waitingForAnimation) {
          waitingForAnimation = true;
          void Promise.all(animations.map((animation) => animation.finished.catch(() => {}))).then(
            () => {
              waitingForAnimation = false;
              schedule();
            },
          );
        }
        return;
      }
      followOSMProgress(viewport, current, total);
    };
    const schedule = () => {
      if (!active || frame !== undefined) return;
      frame = requestAnimationFrame(() => {
        frame = undefined;
        update();
      });
    };
    const observer =
      typeof ResizeObserver === "undefined"
        ? undefined
        : new ResizeObserver(() => {
            const next = sizes();
            if (next !== sizeSignature) {
              sizeSignature = next;
              schedule();
            }
          });
    window.addEventListener("resize", schedule);
    document.fonts?.addEventListener("loadingdone", schedule);
    void document.fonts?.ready.then(schedule);
    schedule();
    return () => {
      active = false;
      if (frame !== undefined) cancelAnimationFrame(frame);
      observer?.disconnect();
      dialog?.removeEventListener(DIALOG_PIXEL_ALIGNMENT_EVENT, schedule);
      window.removeEventListener("resize", schedule);
      document.fonts?.removeEventListener("loadingdone", schedule);
    };
  }, [scroll, jobId, current, total, state]);
}
