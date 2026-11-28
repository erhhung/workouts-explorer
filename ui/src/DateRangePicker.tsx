import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import type { DateRangeEnum, DateRangePreference } from "./api";
import { formatDateOnly } from "./date";
import { useDateFormat } from "./DateFormat";
import { RecentDateRangeItems } from "./RecentDateRanges";

export const DATE_RANGE_SHORTCUTS: ReadonlyArray<[DateRangeEnum, string]> = [
  ["thisWeek", "This week"],
  ["lastWeek", "Last week"],
  ["last7Days", "Last 7 days"],
  ["last30Days", "Last 30 days"],
  ["thisMonth", "This month"],
  ["lastMonth", "Last month"],
  ["thisYear", "This year"],
  ["lastYear", "Last year"],
  ["allTime", "All time"],
];

export function dateRangeRequest(range: DateRangePreference, timezone: string) {
  const explicit = /^(\d{4}-\d{2}-\d{2})\/(\d{4}-\d{2}-\d{2})$/.exec(range);
  return explicit
    ? { startDate: explicit[1], endDate: explicit[2] }
    : { dateRangeEnum: range, tz: timezone };
}

export function DateRangePicker({
  range,
  recent,
  onSelect,
  onCustom,
}: {
  range: DateRangePreference;
  recent: string[];
  onSelect: (range: DateRangePreference) => void;
  onCustom: () => void;
}) {
  const dateFormat = useDateFormat();
  const explicit = /^(\d{4}-\d{2}-\d{2})\/(\d{4}-\d{2}-\d{2})$/.exec(range);
  const label =
    DATE_RANGE_SHORTCUTS.find(([value]) => value === range)?.[1] ??
    (explicit
      ? `${formatDateOnly(explicit[1], dateFormat)} to ${formatDateOnly(explicit[2], dateFormat)}`
      : "Last 30 days");
  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger className="range-trigger" aria-label="Select date range">
        <span>Date range</span>
        <strong>{label}</strong>
        <span aria-hidden="true">v</span>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content
          className="menu-content range-menu"
          align="start"
          sideOffset={12}
          avoidCollisions={false}
          collisionPadding={12}
        >
          {DATE_RANGE_SHORTCUTS.map(([value, title]) => (
            <DropdownMenu.Item key={value} onSelect={() => onSelect(value)}>
              {title}
              {range === value && <span aria-label="selected">&#10003;</span>}
            </DropdownMenu.Item>
          ))}
          <DropdownMenu.Separator />
          <DropdownMenu.Item onSelect={onCustom}>
            Custom...{explicit && <span aria-label="selected">&#10003;</span>}
          </DropdownMenu.Item>
          <RecentDateRangeItems ranges={recent} current={range} onSelect={onSelect} />
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}
