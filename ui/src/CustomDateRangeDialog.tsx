import * as Dialog from "./Dialog";
import { type FormEvent, useEffect, useId, useRef, useState } from "react";
import { type DateRangePreference } from "./api";
import { completeDateRangeOnBlur } from "./dateRange";
import { DateInput } from "./DateInput";
import { validISODate as validDate } from "./date";

const EXPLICIT_RANGE = /^(\d{4}-\d{2}-\d{2})\/(\d{4}-\d{2}-\d{2})$/;

export function CustomDateRangeDialog({
  open,
  onOpenChange,
  range,
  onApply,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  range: DateRangePreference;
  onApply: (range: DateRangePreference) => void;
}) {
  const id = useId();
  const errorRef = useRef<HTMLParagraphElement>(null);
  const [error, setError] = useState<{ message: string; fields: Array<"start" | "end"> }>();
  const explicit = EXPLICIT_RANGE.exec(range);
  const [startValid, setStartValid] = useState(() => validDate(explicit?.[1] ?? ""));
  const [endValid, setEndValid] = useState(() => validDate(explicit?.[2] ?? ""));
  useEffect(() => {
    if (!open) return;
    const selected = EXPLICIT_RANGE.exec(range);
    setStartValid(validDate(selected?.[1] ?? ""));
    setEndValid(validDate(selected?.[2] ?? ""));
    setError(undefined);
  }, [open, range]);
  const changeOpen = (next: boolean) => {
    if (!next) setError(undefined);
    onOpenChange(next);
  };
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const values = new FormData(event.currentTarget);
    const start = String(values.get("startDate"));
    const end = String(values.get("endDate"));
    const invalidFields: Array<"start" | "end"> = [];
    if (!validDate(start)) invalidFields.push("start");
    if (!validDate(end)) invalidFields.push("end");
    if (invalidFields.length) {
      setError({ message: "Enter valid start and end dates.", fields: invalidFields });
      setTimeout(() => errorRef.current?.focus());
      return;
    }
    if (start > end) {
      setError({ message: "Start date must be on or before end date.", fields: ["start", "end"] });
      setTimeout(() => errorRef.current?.focus());
      return;
    }
    setError(undefined);
    changeOpen(false);
    onApply(`${start}/${end}`);
  };

  return (
    <Dialog.Root open={open} onOpenChange={changeOpen}>
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <Dialog.Content className="dialog-content compact-dialog custom-range-dialog">
          <div className="dialog-heading">
            <div>
              <Dialog.Title>Custom date range</Dialog.Title>
              <Dialog.Description>Choose inclusive calendar dates.</Dialog.Description>
            </div>
            <Dialog.Close className="icon-button" aria-label="Close Custom date range">
              &times;
            </Dialog.Close>
          </div>
          <form className="custom-range-form" onSubmit={submit} noValidate>
            {error && (
              <p
                className="error-summary"
                id={`${id}-error`}
                role="alert"
                tabIndex={-1}
                ref={errorRef}
              >
                {error.message}
              </p>
            )}
            <div className="field-pair">
              <div className="field">
                <label htmlFor={`${id}-start`}>Start date</label>
                <DateInput
                  id={`${id}-start`}
                  name="startDate"
                  defaultValue={explicit?.[1]}
                  required
                  onValidityChange={setStartValid}
                  onChange={() => setError(undefined)}
                  aria-invalid={error?.fields.includes("start") || undefined}
                  aria-describedby={error?.fields.includes("start") ? `${id}-error` : undefined}
                  onBlur={completeDateRangeOnBlur}
                />
              </div>
              <div className="field">
                <label htmlFor={`${id}-end`}>End date</label>
                <DateInput
                  id={`${id}-end`}
                  name="endDate"
                  defaultValue={explicit?.[2]}
                  required
                  onValidityChange={setEndValid}
                  onChange={() => setError(undefined)}
                  aria-invalid={error?.fields.includes("end") || undefined}
                  aria-describedby={error?.fields.includes("end") ? `${id}-error` : undefined}
                />
              </div>
            </div>
            <div className="dialog-actions">
              <Dialog.Close type="button" className="secondary range-dialog-action">
                Cancel
              </Dialog.Close>
              <button
                className="primary range-dialog-action"
                disabled={!startValid || !endValid || Boolean(error)}
              >
                Apply
              </button>
            </div>
          </form>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
