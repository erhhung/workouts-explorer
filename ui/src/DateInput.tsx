import {
  forwardRef,
  useImperativeHandle,
  useLayoutEffect,
  useRef,
  useState,
  type InputHTMLAttributes,
} from "react";
import { Tooltip } from "./Tooltip";
import { useDateFormat } from "./DateFormat";
import { formatDateEntry, parseDateEntry } from "./date";

export const DateInput = forwardRef<
  HTMLInputElement,
  Omit<InputHTMLAttributes<HTMLInputElement>, "type"> & {
    onValidityChange?: (valid: boolean) => void;
  }
>(function DateInput({ onValidityChange, ...props }, forwardedRef) {
  const input = useRef<HTMLInputElement>(null);
  const canonical = useRef<HTMLInputElement>(null);
  const order = useDateFormat();
  const [text, setText] = useState(() =>
    formatDateEntry(String(props.value ?? props.defaultValue ?? ""), order),
  );
  const [invalid, setInvalid] = useState(false);
  useImperativeHandle(forwardedRef, () => input.current!);
  const unavailable = Boolean(props.disabled || props.readOnly);
  useLayoutEffect(() => {
    if (!canonical.current) return;
    if (props.value !== undefined) canonical.current.value = String(props.value);
    setText(formatDateEntry(canonical.current.value, order));
    setInvalid(false);
  }, [order, props.value]);
  useLayoutEffect(() => {
    const valid =
      props.disabled ||
      canonical.current?.matches(":disabled") ||
      ((!text.trim() ? !props.required : Boolean(parseDateEntry(text, order))) &&
        Boolean(canonical.current?.validity.valid));
    onValidityChange?.(Boolean(valid));
  }, [
    text,
    order,
    props.min,
    props.max,
    props.required,
    props.disabled,
    props.readOnly,
    onValidityChange,
  ]);
  function acceptPicker() {
    if (!canonical.current) return;
    const value = formatDateEntry(canonical.current.value, order);
    // Range completion runs during focus transfer, before React flushes state.
    if (input.current) input.current.value = value;
    setText(value);
    setInvalid(!canonical.current.validity.valid);
  }
  function showPicker() {
    const field = input.current;
    if (!field || unavailable || field.matches(":disabled")) return;
    field.focus({ preventScroll: true });
    // Browsers without showPicker retain ordinary native date entry/focus.
    try {
      canonical.current?.showPicker?.();
    } catch {
      /* Native picker availability varies by host/browser. */
    }
  }
  return (
    <span className="date-input-control">
      <input
        {...props}
        type="text"
        name={undefined}
        defaultValue={undefined}
        value={text}
        inputMode="numeric"
        placeholder={order}
        autoComplete="off"
        data-date-entry=""
        data-date-order={order}
        aria-invalid={props["aria-invalid"] ?? (invalid || undefined)}
        ref={input}
        onFocus={(event) => {
          event.currentTarget.select();
          props.onFocus?.(event);
        }}
        onChange={(event) => {
          const value = event.currentTarget.value;
          setText(value);
          const iso = parseDateEntry(value, order);
          if (canonical.current) canonical.current.value = iso;
          setInvalid((Boolean(value.trim()) && !iso) || !canonical.current?.validity.valid);
          props.onChange?.(event);
        }}
        onBlur={(event) => {
          const iso = parseDateEntry(event.currentTarget.value, order);
          if (canonical.current) canonical.current.value = iso;
          setInvalid(
            (Boolean(event.currentTarget.value.trim()) && !iso) ||
              !canonical.current?.validity.valid,
          );
          if (iso) setText(formatDateEntry(iso, order));
          props.onBlur?.(event);
        }}
        onKeyDown={(event) => {
          props.onKeyDown?.(event);
          if (
            !event.defaultPrevented &&
            event.altKey &&
            !event.ctrlKey &&
            !event.metaKey &&
            event.key === "ArrowDown"
          ) {
            event.preventDefault();
            showPicker();
          }
        }}
      />
      <input
        type="date"
        className="date-input-native"
        ref={canonical}
        data-date-canonical=""
        name={props.name}
        id={props.id ? `${props.id}-native` : undefined}
        defaultValue={props.defaultValue}
        value={props.value}
        min={props.min}
        max={props.max}
        required={props.required}
        disabled={props.disabled}
        readOnly={props.readOnly}
        tabIndex={-1}
        aria-hidden="true"
        onInput={acceptPicker}
        onChange={(event) => {
          acceptPicker();
          props.onChange?.(event);
          input.current?.focus({ preventScroll: true });
        }}
      />
      <Tooltip
        content="Show calendar picker"
        bubbleAriaLive="off"
        className="date-input-calendar"
        focusable={false}
        keyboardFocusOnly
        dismissOnPointerDown
        enabled={!unavailable}
      >
        <button
          type="button"
          className="date-input-calendar-trigger"
          aria-label="Show calendar picker"
          tabIndex={-1}
          aria-controls={props.id}
          disabled={unavailable}
          onMouseDown={(event) => event.preventDefault()}
          onClick={showPicker}
        >
          <svg viewBox="0 0 24 24" aria-hidden="true">
            <rect x="4" y="5" width="16" height="16" rx="2" />
            <path d="M8 3v4m8-4v4M4 10h16m-11 4h2m2 0h2m-6 3h2" />
          </svg>
        </button>
      </Tooltip>
    </span>
  );
});
