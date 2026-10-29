import {
  type FocusEvent,
  type KeyboardEvent,
  type ReactNode,
  useEffect,
  useId,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";

export function Tooltip({
  content,
  children,
  className = "",
  focusable = true,
  label,
  portal = false,
}: {
  content: string;
  children: ReactNode;
  className?: string;
  focusable?: boolean;
  label?: string;
  portal?: boolean;
}) {
  const tooltipId = useId();
  const triggerRef = useRef<HTMLSpanElement>(null);
  const bubbleRef = useRef<HTMLSpanElement>(null);
  const interaction = useRef({ hovered: false, focused: false });
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState({ top: 0, left: 0 });

  function place() {
    const trigger = triggerRef.current;
    if (!trigger || !bubbleRef.current) return;
    const anchor = trigger.getBoundingClientRect();
    const bubble = bubbleRef.current.getBoundingClientRect();
    let left = 0,
      top = 0,
      right = window.innerWidth,
      bottom = window.innerHeight;
    // A portal must not outlive its anchor's visible portion in a scrolling list.
    for (let parent = trigger.parentElement; parent; parent = parent.parentElement) {
      const style = getComputedStyle(parent);
      const clipX = /auto|scroll|hidden|clip/.test(style.overflowX || style.overflow);
      const clipY = /auto|scroll|hidden|clip/.test(style.overflowY || style.overflow);
      if (!clipX && !clipY) continue;
      const bounds = parent.getBoundingClientRect();
      if (clipX) {
        left = Math.max(left, bounds.left);
        right = Math.min(right, bounds.right);
      }
      if (clipY) {
        top = Math.max(top, bounds.top);
        bottom = Math.min(bottom, bounds.bottom);
      }
    }
    if (
      anchor.right <= left ||
      anchor.left >= right ||
      anchor.bottom <= top ||
      anchor.top >= bottom
    ) {
      setOpen(false);
      return;
    }
    setPosition({
      left: Math.max(
        8,
        Math.min(
          anchor.left + (anchor.width - bubble.width) / 2,
          window.innerWidth - bubble.width - 8,
        ),
      ),
      top: Math.max(
        8,
        Math.min(
          anchor.top - bubble.height - 6 >= 8 ? anchor.top - bubble.height - 6 : anchor.bottom + 6,
          window.innerHeight - bubble.height - 8,
        ),
      ),
    });
    setOpen(true);
  }

  useEffect(() => {
    if (!portal || !open) return;
    function escape(event: globalThis.KeyboardEvent) {
      if (event.key === "Escape") setOpen(false);
    }
    window.addEventListener("scroll", place, true);
    window.addEventListener("resize", place);
    window.addEventListener("keydown", escape);
    return () => {
      window.removeEventListener("scroll", place, true);
      window.removeEventListener("resize", place);
      window.removeEventListener("keydown", escape);
    };
  }, [portal, open]);

  const bubble = (
    <span
      ref={portal ? bubbleRef : undefined}
      className={
        portal
          ? `tooltip-content tooltip-content--portal${open ? " is-open" : ""}`
          : "tooltip-content"
      }
      style={portal ? position : undefined}
      id={tooltipId}
      role="tooltip"
    >
      {content}
    </span>
  );
  return (
    <span className={`tooltip ${className}`.trim()}>
      <span
        className="tooltip-trigger"
        tabIndex={focusable ? 0 : undefined}
        aria-describedby={tooltipId}
        aria-label={label}
        ref={portal ? triggerRef : undefined}
        {...(portal
          ? {
              onPointerEnter: () => {
                interaction.current.hovered = true;
                place();
              },
              onPointerLeave: () => {
                interaction.current.hovered = false;
                if (!interaction.current.focused) setOpen(false);
              },
              onFocus: () => {
                interaction.current.focused = true;
                place();
              },
              onBlur: (event: FocusEvent<HTMLSpanElement>) => {
                if (event.currentTarget.contains(event.relatedTarget)) return;
                interaction.current.focused = false;
                if (!interaction.current.hovered) setOpen(false);
              },
            }
          : {})}
      >
        {children}
      </span>
      {portal ? createPortal(bubble, document.body) : bubble}
    </span>
  );
}

export function InfoTip({ content, label }: { content: string; label: string }) {
  const tooltipId = useId();
  const containerRef = useRef<HTMLSpanElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const [open, setOpen] = useState(false);
  const [dismissed, setDismissed] = useState(false);

  useEffect(() => {
    if (!open) return;
    function outside(event: PointerEvent) {
      if (!containerRef.current?.contains(event.target as Node)) {
        setOpen(false);
        setDismissed(true);
      }
    }
    document.addEventListener("pointerdown", outside);
    return () => document.removeEventListener("pointerdown", outside);
  }, [open]);

  function escape(event: KeyboardEvent<HTMLButtonElement>) {
    if (event.key !== "Escape" || !open) return;
    event.preventDefault();
    setOpen(false);
    setDismissed(true);
    triggerRef.current?.focus();
  }

  function blur(event: FocusEvent<HTMLSpanElement>) {
    if (event.relatedTarget instanceof Node && event.currentTarget.contains(event.relatedTarget))
      return;
    setOpen(false);
    setDismissed(false);
  }

  return (
    <span
      ref={containerRef}
      className={`tooltip info-tip${open ? " is-open" : ""}${dismissed ? " is-dismissed" : ""}`}
      onBlur={blur}
    >
      <button
        ref={triggerRef}
        type="button"
        className="tooltip-trigger"
        aria-label={label}
        aria-describedby={tooltipId}
        aria-expanded={open}
        onClick={() => {
          setOpen((value) => !value);
          setDismissed(open);
        }}
        onKeyDown={escape}
      >
        <span aria-hidden="true">?</span>
      </button>
      <span className="tooltip-content" id={tooltipId} role="tooltip">
        {content}
      </span>
    </span>
  );
}

export function offsetLabel(offset: number, compact = false) {
  const sign = offset < 0 ? "-" : "+";
  const absolute = Math.abs(offset);
  const hours = Math.floor(absolute / 60);
  const minutes = absolute % 60;
  return `UTC${sign}${hours}${compact && minutes === 0 ? "" : `:${String(minutes).padStart(2, "0")}`}`;
}

export function zoneOffsetAt(timeZone: string, instant: Date) {
  try {
    const name = new Intl.DateTimeFormat("en-US", { timeZone, timeZoneName: "longOffset" })
      .formatToParts(instant)
      .find((part) => part.type === "timeZoneName")?.value;
    const match = /^(?:GMT|UTC)(?:([+-])(\d{1,2})(?::?(\d{2}))?)?$/.exec(name ?? "");
    if (!match) return null;
    if (!match[1]) return 0;
    const minutes = Number(match[2]) * 60 + Number(match[3] ?? 0);
    return match[1] === "-" ? -minutes : minutes;
  } catch {
    return null;
  }
}

export function instantZone(timeZone: string, instant: Date) {
  const offset = zoneOffsetAt(timeZone, instant);
  if (offset == null) return { label: "TZ unavailable", title: `${timeZone} (offset unavailable)` };
  return {
    label: zoneAbbreviation(timeZone, instant),
    title: `${timeZone} (${offsetLabel(offset)})`,
  };
}

export function zoneAbbreviation(timeZone: string, instant: Date) {
  return (
    new Intl.DateTimeFormat("en-US", { timeZone, timeZoneName: "short" })
      .formatToParts(instant)
      .find((part) => part.type === "timeZoneName")?.value ?? timeZone
  );
}

export function ZoneBadge({
  label,
  title,
  focusable = true,
}: {
  label: string;
  title: string;
  focusable?: boolean;
}) {
  return (
    <Tooltip content={title} className="timezone-badge" focusable={focusable} label={title}>
      {label}
    </Tooltip>
  );
}
