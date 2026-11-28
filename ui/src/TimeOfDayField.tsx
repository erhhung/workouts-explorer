import { PreferenceSelect } from "./PreferenceSelect";
import type { Preferences } from "./api";

const pad = (value: number) => String(value).padStart(2, "0");
const minutes = Array.from({ length: 60 }, (_, minute) => ({
  value: pad(minute),
  label: pad(minute),
}));
const periods = [
  { value: "am", label: "AM" },
  { value: "pm", label: "PM" },
];

export function TimeOfDayField({
  id,
  value,
  onChange,
  disabled,
  preferences,
}: {
  id: string;
  value: string;
  onChange: (value: string) => void;
  disabled: boolean;
  preferences: Pick<Preferences, "clockFormat" | "timezone">;
}) {
  const hour = Number(value.slice(0, 2)),
    minute = value.slice(3),
    twelveHour = preferences.clockFormat === "12h";
  const hours = Array.from({ length: twelveHour ? 12 : 24 }, (_, index) => {
    const hour = twelveHour ? index + 1 : index;
    return { value: String(hour), label: pad(hour) };
  });
  return (
    <div className="time-of-day-field" role="group" aria-labelledby={`${id}-label`}>
      <span id={`${id}-label`}>Time of day</span>
      <div className="time-of-day-controls" aria-description={`Daily in ${preferences.timezone}`}>
        <PreferenceSelect
          key={preferences.clockFormat}
          id={`${id}-hours`}
          name="syncHours"
          label="Hours"
          value={String(twelveHour ? hour % 12 || 12 : hour)}
          defaultValue={String(twelveHour ? hour % 12 || 12 : hour)}
          options={hours}
          compact
          disabled={disabled}
          onValueChange={(next) =>
            onChange(
              `${pad(twelveHour ? (Number(next) % 12) + (hour >= 12 ? 12 : 0) : Number(next))}:${minute}`,
            )
          }
        />
        <span aria-hidden="true">:</span>
        <PreferenceSelect
          id={`${id}-minutes`}
          name="syncMinutes"
          label="Minutes"
          value={minute}
          defaultValue={minute}
          options={minutes}
          compact
          disabled={disabled}
          onValueChange={(next) => onChange(`${pad(hour)}:${next}`)}
        />
        {twelveHour && (
          <PreferenceSelect
            id={`${id}-period`}
            name="syncPeriod"
            label="AM/PM"
            value={hour >= 12 ? "pm" : "am"}
            defaultValue={hour >= 12 ? "pm" : "am"}
            options={periods}
            compact
            disabled={disabled}
            onValueChange={(next) =>
              onChange(`${pad((hour % 12) + (next === "pm" ? 12 : 0))}:${minute}`)
            }
          />
        )}
      </div>
    </div>
  );
}
