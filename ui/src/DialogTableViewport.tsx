import { type CSSProperties, type ReactNode, type RefObject, useLayoutEffect, useRef } from "react";

export function DialogTableViewport({
  className,
  rowSlots,
  scrollRef,
  rowSample,
  rowClassName,
  message,
  busy,
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
    let active = true;
    let scheduled: number | undefined;
    const update = () => {
      if (!active) return;
      // Dialog entry transforms must not scale the reserved layout dimensions.
      const headerHeight = parseFloat(getComputedStyle(header).height) || header.offsetHeight,
        rowHeight = parseFloat(getComputedStyle(row).height) || row.offsetHeight;
      if (!headerHeight || !rowHeight) return;
      const css = getComputedStyle(scroll),
        px = (name: string) => parseFloat(css.getPropertyValue(name)) || 0;
      const top = px("border-top-width") + px("padding-top"),
        bottom = px("border-bottom-width") + px("padding-bottom");
      const values: Record<string, number> = {
        "--dialog-table-height": headerHeight + rowSlots * rowHeight + top + bottom,
        "--dialog-table-header": headerHeight + top,
        "--dialog-table-left": px("border-left-width"),
        "--dialog-table-right": scroll.offsetWidth - scroll.clientWidth - px("border-left-width"),
        "--dialog-table-bottom": scroll.offsetHeight - scroll.clientHeight - px("border-top-width"),
      };
      for (const [name, value] of Object.entries(values)) {
        const next = `${value}px`;
        if (root.style.getPropertyValue(name) !== next) root.style.setProperty(name, next);
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
    void document.fonts?.ready.then(update);
    return () => {
      active = false;
      if (scheduled !== undefined) cancelAnimationFrame(scheduled);
      observer?.disconnect();
      window.removeEventListener("resize", schedule);
    };
  }, [rowSlots, rowClassName, scrollRef]);
  return (
    <div
      ref={frame}
      className="dialog-table-frame"
      style={{ "--dialog-table-rows": rowSlots } as CSSProperties}
    >
      <div ref={scrollRef} className={className} aria-busy={busy} style={style}>
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
        <div className="dialog-table-empty" role="status">
          <span>{message}</span>
        </div>
      )}
    </div>
  );
}
