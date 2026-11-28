import { type CSSProperties, type ReactNode, type RefObject, useLayoutEffect, useRef } from "react";
import { DIALOG_PIXEL_ALIGNMENT_EVENT } from "./dialogPixelAlignment";

export function DialogTableViewport({
  className,
  rowSlots,
  scrollRef,
  rowSample,
  rowClassName,
  message,
  busy,
  stickyHeader = false,
  scrollLabel,
  style,
  children,
}: {
  className: string;
  rowSlots: number;
  scrollRef: RefObject<HTMLDivElement | null>;
  rowSample: ReactNode;
  rowClassName?: string;
  message?: string;
  busy: boolean;
  stickyHeader?: boolean;
  scrollLabel?: string;
  style?: CSSProperties;
  children: ReactNode;
}) {
  const frame = useRef<HTMLDivElement>(null);
  const sample = useRef<HTMLTableRowElement>(null);
  useLayoutEffect(() => {
    const root = frame.current,
      scroll = scrollRef.current,
      row = sample.current;
    const header = scroll?.querySelector("thead");
    if (!root || !scroll || !row || !header) return;
    const dialog = root.closest<HTMLElement>('[role="dialog"]');
    let active = true;
    let waitingForAnimation = false;
    let scheduled: number | undefined;
    const update = () => {
      if (!active) return;
      // Dialog entry transforms must not scale the reserved layout dimensions.
      const headerHeight = parseFloat(getComputedStyle(header).height) || header.offsetHeight,
        rowHeight = parseFloat(getComputedStyle(row).height) || row.offsetHeight;
      if (!headerHeight || !rowHeight) return;
      const css = getComputedStyle(scroll),
        px = (name: string) => parseFloat(css.getPropertyValue(name)) || 0,
        frameCSS = getComputedStyle(root),
        frameTop = parseFloat(frameCSS.paddingTop) || 0,
        frameBottom = parseFloat(frameCSS.paddingBottom) || 0;
      const top = frameTop + px("border-top-width") + px("padding-top"),
        bottom = frameBottom + px("border-bottom-width") + px("padding-bottom");
      const values: Record<string, number> = {
        "--dialog-table-height": headerHeight + rowSlots * rowHeight + top + bottom,
        "--dialog-table-header": headerHeight + top,
        "--dialog-table-left": px("border-left-width"),
        "--dialog-table-right": scroll.offsetWidth - scroll.clientWidth - px("border-left-width"),
        "--dialog-table-bottom":
          frameBottom + scroll.offsetHeight - scroll.clientHeight - px("border-top-width"),
      };
      for (const [name, value] of Object.entries(values)) {
        const next = `${value}px`;
        if (root.style.getPropertyValue(name) !== next) root.style.setProperty(name, next);
      }
      if (stickyHeader) {
        const animations = dialog
          ?.getAnimations?.()
          .filter((animation) => animation.playState === "running");
        if (animations?.length) {
          if (!waitingForAnimation) {
            waitingForAnimation = true;
            void Promise.all(
              animations.map((animation) => animation.finished.catch(() => {})),
            ).then(() => {
              waitingForAnimation = false;
              schedule();
            });
          }
          return;
        }
        // The shared dialog aligns its own layer; only correct the table's residual offset.
        const scale = window.devicePixelRatio || 1;
        const previous =
            parseFloat(root.style.getPropertyValue("--dialog-table-pixel-offset-y")) || 0,
          unsnapped = root.getBoundingClientRect().top - previous,
          offset = Math.round(unsnapped * scale) / scale - unsnapped;
        const next = `${offset}px`;
        if (root.style.getPropertyValue("--dialog-table-pixel-offset-y") !== next)
          root.style.setProperty("--dialog-table-pixel-offset-y", next);
      }
    };
    const schedule = () => {
      if (!active || scheduled !== undefined) return;
      scheduled = requestAnimationFrame(() => {
        scheduled = undefined;
        update();
      });
    };
    update();
    const observer =
      typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(schedule);
    observer?.observe(header);
    observer?.observe(row);
    observer?.observe(scroll);
    window.addEventListener("resize", schedule);
    dialog?.addEventListener(DIALOG_PIXEL_ALIGNMENT_EVENT, schedule);
    void document.fonts?.ready.then(update);
    return () => {
      active = false;
      if (scheduled !== undefined) cancelAnimationFrame(scheduled);
      observer?.disconnect();
      window.removeEventListener("resize", schedule);
      dialog?.removeEventListener(DIALOG_PIXEL_ALIGNMENT_EVENT, schedule);
    };
  }, [rowSlots, rowClassName, scrollRef, stickyHeader]);
  return (
    <div
      ref={frame}
      className={`dialog-table-frame${stickyHeader ? " dialog-table-frame--sticky" : ""}`}
      style={{ "--dialog-table-rows": rowSlots } as CSSProperties}
    >
      <div
        ref={scrollRef}
        data-dialog-table-scroll=""
        className={className}
        aria-busy={busy}
        role={scrollLabel ? "region" : undefined}
        aria-label={scrollLabel}
        tabIndex={scrollLabel ? 0 : undefined}
        style={style}
      >
        {children}
      </div>
      <table className="workout-table dialog-table-measure" aria-hidden="true" inert>
        <tbody>
          <tr ref={sample} className={rowClassName}>
            <td>{rowSample}</td>
          </tr>
        </tbody>
      </table>
      {message && (
        <div className="dialog-table-empty content-state-message" role="status">
          <span>{message}</span>
        </div>
      )}
    </div>
  );
}
