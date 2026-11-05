import * as Select from "@radix-ui/react-select";
import { useRef, useState } from "react";

export function PreferenceSelect({
  label,
  id,
  name,
  defaultValue,
  options,
}: {
  label: string;
  id: string;
  name: string;
  defaultValue: string;
  options: Array<{ value: string; label: string }>;
}) {
  const trigger = useRef<HTMLButtonElement>(null);
  const [height, setHeight] = useState(0);
  return (
    <div className="field">
      <label id={`${id}-label`} htmlFor={id}>
        {label}
      </label>
      <Select.Root
        name={name}
        defaultValue={defaultValue}
        onOpenChange={(open) => {
          if (open) setHeight(trigger.current?.getBoundingClientRect().height ?? 0);
        }}
      >
        <Select.Trigger
          ref={trigger}
          id={id}
          aria-labelledby={`${id}-label`}
          className="preference-select-trigger"
        >
          <Select.Value />
          <Select.Icon className="preference-select-icon" aria-hidden="true">
            v
          </Select.Icon>
        </Select.Trigger>
        <Select.Portal>
          <Select.Content
            className="preference-select-content"
            position="popper"
            side="bottom"
            align="start"
            sideOffset={-height}
            collisionPadding={12}
            avoidCollisions={false}
            aria-labelledby={`${id}-label`}
          >
            <Select.ScrollUpButton
              className="preference-select-scroll"
              aria-label="Scroll options up"
            >
              &#9650;
            </Select.ScrollUpButton>
            <Select.Viewport className="preference-select-options">
              {options.map((option) => (
                <Select.Item
                  key={option.value}
                  value={option.value}
                  className="preference-select-option"
                >
                  <Select.ItemText>{option.label}</Select.ItemText>
                  <Select.ItemIndicator aria-hidden="true">&#10003;</Select.ItemIndicator>
                </Select.Item>
              ))}
            </Select.Viewport>
            <Select.ScrollDownButton
              className="preference-select-scroll"
              aria-label="Scroll options down"
            >
              &#9660;
            </Select.ScrollDownButton>
          </Select.Content>
        </Select.Portal>
      </Select.Root>
    </div>
  );
}
