import { createRef } from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { DateInput } from "./DateInput";
import { DateFormatProvider } from "./DateFormat";
const pickerInput = (input: HTMLElement) =>
  input.closest(".date-input-control")!.querySelector<HTMLInputElement>("[data-date-canonical]")!;

describe("shared native date input", () => {
  test("reports invalid drafts immediately and clears invalid styling after a valid correction", () => {
    const validity = vi.fn();
    render(
      <DateInput
        aria-label="Date"
        defaultValue="2026-05-13"
        required
        onValidityChange={validity}
      />,
    );
    const field = screen.getByLabelText("Date");
    field.focus();
    expect(validity).toHaveBeenLastCalledWith(true);
    fireEvent.change(field, { target: { value: "13/5/2026" } });
    expect(validity).toHaveBeenLastCalledWith(false);
    expect(field).toHaveAttribute("aria-invalid", "true");
    expect(field).toHaveFocus();
    fireEvent.blur(field);
    expect(field).toHaveAttribute("aria-invalid", "true");
    expect(pickerInput(field)).toHaveValue("");
    fireEvent.change(field, { target: { value: "5/13/2026" } });
    expect(validity).toHaveBeenLastCalledWith(true);
    expect(field).not.toHaveAttribute("aria-invalid", "true");
    pickerInput(field).value = "";
    fireEvent.input(pickerInput(field));
    expect(validity).toHaveBeenLastCalledWith(false);
    expect(field).toHaveAttribute("aria-invalid", "true");
    fireEvent.change(field, { target: { value: "" } });
    expect(validity).toHaveBeenLastCalledWith(false);
    expect(field).toHaveAttribute("aria-invalid", "true");
    pickerInput(field).value = "2026-05-14";
    fireEvent.input(pickerInput(field));
    expect(validity).toHaveBeenLastCalledWith(true);
    expect(field).not.toHaveAttribute("aria-invalid", "true");
  });
  test("preserves native input attributes, labels, form values and forwarded refs", () => {
    const ref = createRef<HTMLInputElement>();
    render(
      <form aria-label="Range">
        <label htmlFor="start">Start date</label>
        <DateInput
          ref={ref}
          id="start"
          name="startDate"
          defaultValue="2026-11-27"
          min="2026-01-01"
          max="2026-12-31"
          required
          aria-invalid="true"
          aria-describedby="problem"
        />
      </form>,
    );
    const input = screen.getByLabelText("Start date");
    expect(input).toBe(ref.current);
    expect(input).toHaveAttribute("type", "text");
    expect(input).toHaveValue("11/27/2026");
    expect(input).toHaveAttribute("placeholder", "mm/dd/yyyy");
    expect(input).toHaveAttribute("min", "2026-01-01");
    expect(input).toHaveAttribute("max", "2026-12-31");
    expect(input).toBeRequired();
    expect(input).toHaveAttribute("aria-describedby", "problem");
    expect(new FormData(screen.getByRole("form") as HTMLFormElement).get("startDate")).toBe(
      "2026-11-27",
    );
    const button = screen.getByRole("button", { name: "Show calendar picker" });
    expect(button).toHaveAttribute("type", "button");
    expect(button).toHaveAttribute("aria-controls", "start");
    expect(button).not.toHaveAttribute("title");
  });
  test("opens the correct native picker without submitting or modifying the date", async () => {
    const submit = vi.fn((event) => event.preventDefault());
    render(
      <form onSubmit={submit}>
        <DateInput id="start" aria-label="Start date" defaultValue="2026-11-27" />
      </form>,
    );
    const input = screen.getByLabelText("Start date") as HTMLInputElement,
      picker = vi.fn();
    Object.defineProperty(pickerInput(input), "showPicker", { value: picker });
    await userEvent.click(screen.getByRole("button", { name: "Show calendar picker" }));
    expect(picker).toHaveBeenCalledTimes(1);
    expect(input).toHaveFocus();
    expect(input).toHaveValue("11/27/2026");
    expect(submit).not.toHaveBeenCalled();
  });
  test("keeps one Tab stop per field and supports native Alt+Down calendar activation", async () => {
    render(
      <>
        <DateInput aria-label="Date" />
        <button>Next control</button>
      </>,
    );
    const input = screen.getByLabelText("Date"),
      picker = vi.fn();
    Object.defineProperty(pickerInput(input), "showPicker", { value: picker });
    await userEvent.tab();
    expect(input).toHaveFocus();
    await userEvent.keyboard("{Alt>}{ArrowDown}{/Alt}");
    expect(picker).toHaveBeenCalledTimes(1);
    await userEvent.tab();
    expect(screen.getByRole("button", { name: "Next control" })).toHaveFocus();
    expect(screen.getByRole("tooltip")).toHaveTextContent("Show calendar picker");
  });
  test.each(["disabled", "readOnly"] as const)(
    "does not offer an active picker for %s fields",
    async (property) => {
      render(<DateInput aria-label="Date" {...{ [property]: true }} />);
      const picker = vi.fn();
      Object.defineProperty(pickerInput(screen.getByLabelText("Date")), "showPicker", {
        value: picker,
      });
      await userEvent.click(screen.getByRole("button", { name: "Show calendar picker" }));
      expect(picker).not.toHaveBeenCalled();
      expect(screen.getByRole("button", { name: "Show calendar picker" })).toBeDisabled();
    },
  );
  test("keeps native editing available when the browser cannot open a picker", () => {
    render(<DateInput aria-label="Date" />);
    const input = screen.getByLabelText("Date"),
      picker = vi.fn(() => {
        throw new DOMException("Unavailable", "NotAllowedError");
      });
    Object.defineProperty(pickerInput(input), "showPicker", { value: picker });
    fireEvent.click(screen.getByRole("button", { name: "Show calendar picker" }));
    expect(input).toHaveFocus();
    fireEvent.change(input, { target: { value: "2026-11-28" } });
    expect(input).toHaveValue("2026-11-28");
  });
  test.each([
    ["mm/dd/yyyy", "10/5/2026", "10/6/2026"],
    ["dd/mm/yyyy", "5/10/2026", "6/10/2026"],
    ["yyyy/mm/dd", "2026/10/5", "2026/10/6"],
  ] as const)(
    "edits %s ordering while submitting ISO dates and accepting native picker changes",
    (format, initial, next) => {
      render(
        <DateFormatProvider value={format}>
          <form aria-label="Range">
            <DateInput aria-label="Date" name="startDate" defaultValue="2026-10-05" />
          </form>
        </DateFormatProvider>,
      );
      const input = screen.getByLabelText("Date"),
        native = pickerInput(input);
      expect(input).toHaveValue(initial);
      expect(input).toHaveAttribute("placeholder", format);
      fireEvent.change(input, { target: { value: next } });
      fireEvent.blur(input);
      expect(new FormData(screen.getByRole("form") as HTMLFormElement).get("startDate")).toBe(
        "2026-10-06",
      );
      native.value = "2026-10-05";
      fireEvent.input(native);
      expect(input).toHaveValue(initial);
    },
  );
});
