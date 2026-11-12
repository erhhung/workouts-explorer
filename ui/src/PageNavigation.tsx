import { type ReactNode, type Ref, useCallback, useLayoutEffect, useRef } from "react";
import type { Pagination } from "./api";
import { useKeyboardShortcut } from "./keyboardShortcuts";

type Step = "first" | "jump" | "previous";

function PageSymbol({ step, forward = false }: { step: Step; forward?: boolean }) {
  const paths: Record<Step, string> = {
    first: "M7.5 3V15 M18.5 3 10.5 9 18.5 15",
    jump: "M12 3 5 9 12 15 M21 3 14 9 21 15",
    previous: "M16.5 3 9.5 9 16.5 15",
  };
  return (
    <svg
      className="pagination-symbol"
      viewBox="0 0 26 18"
      aria-hidden="true"
      focusable="false"
      fill="none"
      stroke="currentColor"
      strokeWidth={3}
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <path
        d={paths[step]}
        transform={forward ? "translate(26 0) scale(-1 1)" : undefined}
        vectorEffect="non-scaling-stroke"
      />
    </svg>
  );
}

export function PageNavigation({
  value,
  label,
  busy = false,
  unavailable = false,
  reserveTotalPages,
  keyboardDefault = false,
  onPage,
  className = "",
  children,
  ref,
}: {
  value: Pick<Pagination, "page" | "totalPages">;
  label: string;
  busy?: boolean;
  unavailable?: boolean;
  reserveTotalPages?: number;
  keyboardDefault?: boolean;
  onPage: (page: number) => void;
  className?: string;
  children?: ReactNode;
  ref?: Ref<HTMLElement>;
}) {
  const last = Math.max(1, value.totalPages);
  const page = Math.max(1, Math.min(last, value.page));
  const inFlight = useRef(false);
  const navigation = useRef<HTMLElement>(null);
  const attach = useCallback(
    (node: HTMLElement | null) => {
      navigation.current = node;
      if (typeof ref === "function") return ref(node);
      if (ref) ref.current = node;
    },
    [ref],
  );
  useLayoutEffect(() => {
    if (!busy) inFlight.current = false;
  }, [busy, page, last]);
  function changePage(target: number) {
    if (busy || unavailable || inFlight.current) return;
    const next = Math.max(1, Math.min(last, target));
    if (next === page) return;
    inFlight.current = true;
    onPage(next);
  }
  useKeyboardShortcut(
    "page",
    navigation,
    (shortcut) => {
      if (shortcut.kind !== "page") return false;
      changePage(page + shortcut.delta);
      return true;
    },
    keyboardDefault,
  );
  const button = (name: string, step: Step, target: number, forward = false) => (
    <button
      type="button"
      className="secondary pagination-button"
      aria-label={name}
      disabled={unavailable || (forward ? page >= last : page <= 1)}
      onClick={() => {
        changePage(target);
      }}
    >
      <PageSymbol step={step} forward={forward} />
    </button>
  );
  return (
    <nav className={`pagination ${className}`.trim()} aria-label={label} ref={attach}>
      {children}
      <div className="pagination-controls">
        <div className="pagination-group">
          {button("First page", "first", 1)}
          {button("Back 5 pages", "jump", page - 5)}
          {button("Previous", "previous", page - 1)}
        </div>
        <span
          className="pagination-position"
          aria-live="polite"
          aria-atomic="true"
          data-width-sample={
            reserveTotalPages === undefined
              ? undefined
              : `Page ${Math.max(1, reserveTotalPages)} of ${Math.max(1, reserveTotalPages)}`
          }
        >
          {reserveTotalPages === undefined ? (
            <>
              Page {page} of {last}
            </>
          ) : (
            <span>
              Page {page} of {last}
            </span>
          )}
        </span>
        <div className="pagination-group">
          {button("Next", "previous", page + 1, true)}
          {button("Forward 5 pages", "jump", page + 5, true)}
          {button("Last page", "first", last, true)}
        </div>
      </div>
    </nav>
  );
}
