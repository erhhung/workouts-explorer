import { dateTimeFormatter } from "./formatters";

export type DateFormat = "mm/dd/yyyy" | "dd/mm/yyyy" | "yyyy/mm/dd";
export const DEFAULT_DATE_FORMAT: DateFormat = "mm/dd/yyyy";
export const DATE_FORMATS: readonly DateFormat[] = ["mm/dd/yyyy", "dd/mm/yyyy", "yyyy/mm/dd"];
export function normalizeDateFormat(value: unknown): DateFormat {
  return DATE_FORMATS.includes(value as DateFormat) ? (value as DateFormat) : DEFAULT_DATE_FORMAT;
}
export function formatEnglishDate(
  value: Date | number,
  options: Intl.DateTimeFormatOptions = {},
  order: DateFormat = DEFAULT_DATE_FORMAT,
) {
  const formatter = dateTimeFormatter("en-US", options),
    original = formatter.format(value);
  const parts = formatter.formatToParts(value);
  const fields = parts
    .map((part, index) => ({ part, index }))
    .filter(({ part }) => ["year", "month", "day"].includes(part.type));
  if (fields.length < 2) return original;
  const byType = new Map(fields.map(({ part }) => [part.type, part.value]));
  if (options.year === "numeric" || ["medium", "long", "full"].includes(options.dateStyle ?? "")) {
    const year = byType.get("year");
    if (year && /^\d+$/.test(year)) byType.set("year", year.padStart(4, "0"));
  }
  const numeric = byType.get("month") !== undefined && /^\d+$/.test(byType.get("month")!);
  if (numeric) {
    for (const type of ["month", "day"] as const) {
      const part = byType.get(type);
      if (part) byType.set(type, String(Number(part)));
    }
  }
  const types = order === "yyyy/mm/dd" ? ["year", "month", "day"] : ["day", "month", "year"];
  const reordered = types
    .map((type) => byType.get(type as Intl.DateTimeFormatPartTypes))
    .filter((value) => value !== undefined)
    .join(numeric ? "/" : " ");
  const datePart = parts
    .slice(fields[0].index, fields[fields.length - 1].index + 1)
    .map((part) => part.value)
    .join("");
  if (order === DEFAULT_DATE_FORMAT) {
    const padded = parts
      .slice(fields[0].index, fields[fields.length - 1].index + 1)
      .map((part) => byType.get(part.type) ?? part.value)
      .join("");
    return original.replace(datePart, padded);
  }
  return original.replace(datePart, reordered);
}
export function validISODate(value: string) {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!match || Number(match[1]) < 1) return false;
  const date = new Date(value + "T00:00:00Z");
  return Number.isFinite(date.getTime()) && date.toISOString().slice(0, 10) === value;
}
export function formatDateEntry(value: string, order: DateFormat = DEFAULT_DATE_FORMAT) {
  if (!validISODate(value)) return value;
  const [year, rawMonth, rawDay] = value.split("-");
  const month = String(Number(rawMonth)),
    day = String(Number(rawDay));
  return order === "dd/mm/yyyy"
    ? `${day}/${month}/${year}`
    : order === "yyyy/mm/dd"
      ? `${year}/${month}/${day}`
      : `${month}/${day}/${year}`;
}
export function parseDateEntry(value: string, order: DateFormat = DEFAULT_DATE_FORMAT) {
  let trimmed = value.trim();
  if (/^\d{8}$/.test(trimmed))
    trimmed =
      order === "yyyy/mm/dd"
        ? `${trimmed.slice(0, 4)}/${trimmed.slice(4, 6)}/${trimmed.slice(6)}`
        : `${trimmed.slice(0, 2)}/${trimmed.slice(2, 4)}/${trimmed.slice(4)}`;
  if (validISODate(trimmed)) return trimmed;
  const match = (
    order === "yyyy/mm/dd" ? /^(\d{4})\/(\d{1,2})\/(\d{1,2})$/ : /^(\d{1,2})\/(\d{1,2})\/(\d{4})$/
  ).exec(trimmed);
  if (!match) return "";
  const year = order === "yyyy/mm/dd" ? match[1] : match[3],
    month = order === "dd/mm/yyyy" ? match[2] : order === "yyyy/mm/dd" ? match[2] : match[1],
    day = order === "dd/mm/yyyy" ? match[1] : order === "yyyy/mm/dd" ? match[3] : match[2];
  const iso = `${year}-${month.padStart(2, "0")}-${day.padStart(2, "0")}`;
  return validISODate(iso) ? iso : "";
}

export function formatDateOnly(value: string, order: DateFormat = DEFAULT_DATE_FORMAT) {
  if (!validISODate(value)) return value;
  const date = new Date(value + "T00:00:00Z");
  return formatEnglishDate(
    date,
    {
      month: "short",
      day: "numeric",
      year: "numeric",
      timeZone: "UTC",
    },
    order,
  );
}
