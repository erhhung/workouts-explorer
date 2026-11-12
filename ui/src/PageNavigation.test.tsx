import { useState } from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { PageNavigation } from "./PageNavigation";

describe("PageNavigation", () => {
  test("reserves a dialog caption width without changing the announced page or destinations", async () => {
    const onPage = vi.fn();
    const view = render(
      <PageNavigation
        label="Dialog pages"
        value={{ page: 1, totalPages: 1 }}
        reserveTotalPages={58}
        onPage={onPage}
      />,
    );
    const nav = screen.getByRole("navigation");
    expect(nav).toHaveTextContent("Page 1 of 1");
    expect(nav).not.toHaveTextContent("58");
    expect(nav.querySelector(".pagination-position")).toHaveAttribute(
      "data-width-sample",
      "Page 58 of 58",
    );
    for (const button of screen.getAllByRole("button")) expect(button).toBeDisabled();
    view.rerender(
      <PageNavigation
        label="Dialog pages"
        value={{ page: 3, totalPages: 3 }}
        reserveTotalPages={58}
        onPage={onPage}
      />,
    );
    expect(nav).toHaveTextContent("Page 3 of 3");
    expect(nav).not.toHaveTextContent("58");
    await userEvent.click(screen.getByRole("button", { name: "Previous" }));
    expect(onPage).toHaveBeenCalledExactlyOnceWith(2);
  });

  test("supports repeated direct jumps, individual steps and first/last navigation", async () => {
    const onPage = vi.fn();
    function Pager() {
      const [page, setPage] = useState(12);
      return (
        <PageNavigation
          label="Fixture pages"
          value={{ page, totalPages: 58 }}
          onPage={(next) => {
            onPage(next);
            setPage(next);
          }}
        />
      );
    }
    render(<Pager />);
    const user = userEvent.setup();
    for (const [name, destination] of [
      ["Forward 5 pages", 17],
      ["Forward 5 pages", 22],
      ["Previous", 21],
      ["Next", 22],
      ["Back 5 pages", 17],
      ["Last page", 58],
      ["First page", 1],
    ] as const) {
      await user.click(screen.getByRole("button", { name }));
      expect(onPage).toHaveBeenLastCalledWith(destination);
      expect(screen.getByRole("navigation")).toHaveTextContent(`Page ${destination} of 58`);
    }
  });

  test.each([
    [3, "Back 5 pages", 1],
    [56, "Forward 5 pages", 58],
  ])("clamps a jump from page %i to the boundary", async (page, name, target) => {
    const onPage = vi.fn();
    render(<PageNavigation label="Pages" value={{ page, totalPages: 58 }} onPage={onPage} />);
    await userEvent.click(screen.getByRole("button", { name }));
    expect(onPage).toHaveBeenCalledExactlyOnceWith(target);
  });

  test.each([1, 58])("disables all three buttons towards the boundary at page %i", async (page) => {
    const onPage = vi.fn();
    render(<PageNavigation label="Pages" value={{ page, totalPages: 58 }} onPage={onPage} />);
    const buttons = screen.getAllByRole("button");
    const disabled = page === 1 ? buttons.slice(0, 3) : buttons.slice(3);
    const enabled = page === 1 ? buttons.slice(3) : buttons.slice(0, 3);
    for (const button of disabled) {
      expect(button).toBeDisabled();
      await userEvent.click(button);
    }
    for (const button of enabled) expect(button).toBeEnabled();
    expect(onPage).not.toHaveBeenCalled();
  });

  test.each([0, 1])(
    "keeps an empty/single-page result at page one (%i total pages)",
    (totalPages) => {
      render(<PageNavigation label="Pages" value={{ page: 1, totalPages }} onPage={vi.fn()} />);
      expect(screen.getByRole("navigation")).toHaveTextContent("Page 1 of 1");
      for (const button of screen.getAllByRole("button")) expect(button).toBeDisabled();
    },
  );

  test("ignores navigation while busy without changing the displayed enabled states", async () => {
    const onPage = vi.fn();
    render(
      <PageNavigation label="Pages" value={{ page: 12, totalPages: 58 }} busy onPage={onPage} />,
    );
    for (const button of screen.getAllByRole("button")) {
      expect(button).toBeEnabled();
      await userEvent.click(button);
    }
    expect(onPage).not.toHaveBeenCalled();
  });

  test("fences immediate repeat clicks before the owner can render a pending request", () => {
    const onPage = vi.fn();
    render(<PageNavigation label="Pages" value={{ page: 12, totalPages: 58 }} onPage={onPage} />);
    fireEvent.click(screen.getByRole("button", { name: "Next" }));
    fireEvent.click(screen.getByRole("button", { name: "Forward 5 pages" }));
    fireEvent.click(screen.getByRole("button", { name: "Last page" }));
    expect(onPage).toHaveBeenCalledExactlyOnceWith(13);
  });

  test("keeps boundary states stable through same-page refresh and resumes after it settles", async () => {
    const onPage = vi.fn();
    const props = { label: "Pages", value: { page: 1, totalPages: 58 }, onPage };
    const view = render(<PageNavigation {...props} />);
    const states = () =>
      screen.getAllByRole("button").map((button) => button.hasAttribute("disabled"));
    const before = states();
    view.rerender(<PageNavigation {...props} busy />);
    expect(states()).toEqual(before);
    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(onPage).not.toHaveBeenCalled();
    view.rerender(<PageNavigation {...props} />);
    expect(states()).toEqual(before);
    await userEvent.click(screen.getByRole("button", { name: "Last page" }));
    expect(onPage).toHaveBeenCalledExactlyOnceWith(58);
    view.rerender(<PageNavigation {...props} busy />);
    expect(states()).toEqual(before);
    view.rerender(<PageNavigation {...props} value={{ page: 58, totalPages: 58 }} />);
    expect(states()).toEqual([false, false, false, true, true, true]);
    await userEvent.click(screen.getByRole("button", { name: "First page" }));
    expect(onPage).toHaveBeenLastCalledWith(1);
  });

  test("still disables navigation when the displayed result is unavailable", () => {
    render(
      <PageNavigation
        label="Pages"
        value={{ page: 12, totalPages: 58 }}
        unavailable
        onPage={vi.fn()}
      />,
    );
    for (const button of screen.getAllByRole("button")) expect(button).toBeDisabled();
  });
});
