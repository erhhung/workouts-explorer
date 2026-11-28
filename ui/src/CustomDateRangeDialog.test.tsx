import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { CustomDateRangeDialog } from "./CustomDateRangeDialog";
import { DateFormatProvider } from "./DateFormat";

describe("custom range validity", () => {
  test("does not leave Apply enabled while a submitted date-order error marks the fields invalid", async () => {
    render(
      <CustomDateRangeDialog
        open
        onOpenChange={vi.fn()}
        range="2026-05-15/2026-05-14"
        onApply={vi.fn()}
      />,
    );
    const button = screen.getByRole("button", { name: "Apply" });
    await userEvent.click(button);
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Start date must be on or before end date.",
    );
    expect(button).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Start date"), { target: { value: "5/13/2026" } });
    expect(button).toBeEnabled();
  });
  test.each([
    ["mm/dd/yyyy", "13/5/2026", "5/13/2026"],
    ["dd/mm/yyyy", "5/13/2026", "13/5/2026"],
    ["yyyy/mm/dd", "2026/13/5", "2026/5/13"],
  ] as const)(
    "disables Apply for invalid start/end values in %s and recovers after correction",
    async (order, bad, good) => {
      const apply = vi.fn();
      render(
        <DateFormatProvider value={order}>
          <CustomDateRangeDialog
            open
            onOpenChange={vi.fn()}
            range="2026-05-13/2026-05-14"
            onApply={apply}
          />
        </DateFormatProvider>,
      );
      const button = screen.getByRole("button", { name: "Apply" });
      expect(button).toBeEnabled();
      for (const name of ["Start date", "End date"]) {
        const field = screen.getByLabelText(name);
        fireEvent.change(field, { target: { value: bad } });
        expect(button).toBeDisabled();
        expect(field).toHaveAttribute("aria-invalid", "true");
        fireEvent.blur(field);
        expect(field).toHaveAttribute("aria-invalid", "true");
        await userEvent.click(button);
        expect(apply).not.toHaveBeenCalled();
        fireEvent.change(field, { target: { value: good } });
        expect(button).toBeEnabled();
        expect(field).not.toHaveAttribute("aria-invalid", "true");
      }
      await userEvent.click(button);
      expect(apply).toHaveBeenCalledWith("2026-05-13/2026-05-13");
    },
  );
  test("requires both calendar values and follows native picker/range-completion changes", () => {
    render(<CustomDateRangeDialog open onOpenChange={vi.fn()} range="allTime" onApply={vi.fn()} />);
    const button = screen.getByRole("button", { name: "Apply" }),
      start = screen.getByLabelText("Start date"),
      end = screen.getByLabelText("End date");
    expect(button).toBeDisabled();
    fireEvent.change(start, { target: { value: "5/13/2026" } });
    expect(button).toBeDisabled();
    fireEvent.blur(start);
    expect(button).toBeEnabled();
    expect(end).toHaveValue("5/13/2026");
    fireEvent.change(end, { target: { value: "2/30/2026" } });
    expect(button).toBeDisabled();
    const native = end
      .closest(".date-input-control")!
      .querySelector<HTMLInputElement>("[data-date-canonical]")!;
    native.value = "2026-05-14";
    fireEvent.input(native);
    expect(button).toBeEnabled();
    expect(end).toHaveValue("5/14/2026");
  });
});
