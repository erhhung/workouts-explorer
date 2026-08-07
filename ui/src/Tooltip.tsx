import { type FocusEvent, type KeyboardEvent, type ReactNode, useEffect, useId, useRef, useState } from "react";

export function Tooltip({ content, children, className = "", focusable = true, label }: {
  content: string; children: ReactNode; className?: string; focusable?: boolean; label?: string;
}) {
  const tooltipId = useId();
  return <span className={`tooltip ${className}`.trim()}>
    <span className="tooltip-trigger" tabIndex={focusable ? 0 : undefined} aria-describedby={tooltipId} aria-label={label}>{children}</span>
    <span className="tooltip-content" id={tooltipId} role="tooltip">{content}</span>
  </span>;
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
      if (!containerRef.current?.contains(event.target as Node)) { setOpen(false); setDismissed(true); }
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
    if (event.relatedTarget instanceof Node && event.currentTarget.contains(event.relatedTarget)) return;
    setOpen(false);
    setDismissed(false);
  }

  return <span ref={containerRef} className={`tooltip info-tip${open ? " is-open" : ""}${dismissed ? " is-dismissed" : ""}`} onBlur={blur}>
    <button ref={triggerRef} type="button" className="tooltip-trigger" aria-label={label} aria-describedby={tooltipId} aria-expanded={open} onClick={() => { setOpen((value) => !value); setDismissed(open); }} onKeyDown={escape}><span aria-hidden="true">?</span></button>
    <span className="tooltip-content" id={tooltipId} role="tooltip">{content}</span>
  </span>;
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
      .formatToParts(instant).find((part) => part.type === "timeZoneName")?.value;
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
  return { label: zoneAbbreviation(timeZone, instant), title: `${timeZone} (${offsetLabel(offset)})` };
}

export function zoneAbbreviation(timeZone: string, instant: Date) {
  return new Intl.DateTimeFormat("en-US", { timeZone, timeZoneName: "short" })
    .formatToParts(instant).find((part) => part.type === "timeZoneName")?.value ?? timeZone;
}

export function ZoneBadge({ label, title, focusable = true }: { label: string; title: string; focusable?: boolean }) {
  return <Tooltip content={title} className="timezone-badge" focusable={focusable} label={title}>{label}</Tooltip>;
}
