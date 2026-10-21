import type { FocusEvent } from "react";

export function completeDateRangeOnBlur(event: FocusEvent<HTMLInputElement>) {
  const startDate = event.currentTarget;
  const endDate = startDate.form?.elements.namedItem("endDate");
  if (
    /^\d{4}-\d{2}-\d{2}$/.test(startDate.value) &&
    startDate.validity.valid &&
    endDate instanceof HTMLInputElement &&
    (!endDate.value || endDate.value < startDate.value)
  )
    endDate.value = startDate.value;
}
