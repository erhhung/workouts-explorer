import { type ReactNode, useRef, useState } from "react";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import { shortcutForKey, useKeyboardShortcut } from "./keyboardShortcuts";
import { PageNavigation } from "./PageNavigation";
import { installFocusVisibility } from "./focusVisibility";

function Commands({ run, children }: { run: (value: unknown) => void; children?: ReactNode }) {
  const ref = useRef<HTMLElement>(null);
  useKeyboardShortcut("tab", ref, (value) => {
    run(value);
    return true;
  });
  return (
    <main ref={ref}>
      <button>Ordinary control</button>
      {children}
    </main>
  );
}
function Pager({
  name,
  initial = 12,
  busy = false,
  primary = false,
}: {
  name: string;
  initial?: number;
  busy?: boolean;
  primary?: boolean;
}) {
  const [page, setPage] = useState(initial);
  return (
    <section data-paging-region aria-label={name}>
      <button>{name} row</button>
      <PageNavigation
        label={name + " pages"}
        value={{ page, totalPages: 58 }}
        onPage={setPage}
        busy={busy}
        keyboardDefault={primary}
      />
    </section>
  );
}

describe("keyboard shortcuts", () => {
  test("handled shortcuts do not activate focus rings, but preserve navigation already initiated by Tab", () => {
    const dispose = installFocusVisibility(),
      run = vi.fn();
    const view = render(<Commands run={run} />);
    try {
      fireEvent.keyDown(document, { key: "2" });
      expect(run).toHaveBeenCalledTimes(1);
      expect(document.documentElement.dataset.inputModality).toBe("pointer");
      fireEvent.keyDown(document, { key: "Tab" });
      fireEvent.keyDown(document, { key: "3" });
      expect(document.documentElement.dataset.inputModality).toBe("keyboard");
      fireEvent.pointerDown(screen.getByRole("button"));
      fireEvent.keyDown(document, { key: "4" });
      expect(document.documentElement.dataset.inputModality).toBe("pointer");
    } finally {
      view.unmount();
      dispose();
    }
  });
  test.each([
    ["1", false, { kind: "tab", path: "/" }],
    ["4", false, { kind: "tab", path: "/data-sync" }],
    ["ArrowLeft", false, { kind: "page", delta: -1 }],
    ["ArrowRight", true, { kind: "page", delta: 5 }],
    ["C", true, { kind: "map", action: "coverage" }],
    ["r", false, { kind: "map", action: "routes" }],
    ["f", false, { kind: "map", action: "fit" }],
    ["M", true, { kind: "stats", group: "month" }],
    ["y", false, { kind: "stats", group: "year" }],
  ])("recognizes %s with Shift=%s", (key, shiftKey, expected) => {
    expect(shortcutForKey(new KeyboardEvent("keydown", { key, shiftKey }))).toEqual(expected);
  });
  test.each([
    { ctrlKey: true },
    { metaKey: true },
    { altKey: true },
    { isComposing: true },
    { keyCode: 229 },
  ])("preserves browser/composition keys (%o)", (flags) => {
    expect(shortcutForKey(new KeyboardEvent("keydown", { key: "1", ...flags }))).toBeUndefined();
  });
  test.each(["search", "text", "email", "url", "number", "date", "time"])(
    "blocks shortcuts with a visible unfocused %s input",
    (type) => {
      const run = vi.fn();
      render(
        <Commands run={run}>
          <input type={type} />
        </Commands>,
      );
      const control = screen.getByRole("button");
      control.focus();
      expect(fireEvent.keyDown(control, { key: "2" })).toBe(true);
      expect(run).not.toHaveBeenCalled();
    },
  );
  test("ignores hidden fields and receives number keys while ordinary controls have focus", () => {
    const run = vi.fn();
    render(
      <Commands run={run}>
        <div hidden>
          <input type="search" />
        </div>
        <div style={{ display: "none" }}>
          <textarea />
        </div>
        <input type="checkbox" aria-label="Choice" />
      </Commands>,
    );
    for (const control of [screen.getByRole("button"), screen.getByRole("checkbox")]) {
      control.focus();
      expect(fireEvent.keyDown(control, { key: "3" })).toBe(false);
    }
    expect(run).toHaveBeenCalledTimes(2);
    expect(run).toHaveBeenLastCalledWith({ kind: "tab", path: "/stats" });
  });
  test("lets focused date controls and selects own their keyboard input", () => {
    const run = vi.fn();
    render(
      <Commands run={run}>
        <input type="date" aria-label="Date" />
        <select aria-label="Selection">
          <option>One</option>
        </select>
      </Commands>,
    );
    for (const control of [screen.getByLabelText("Date"), screen.getByLabelText("Selection")]) {
      control.focus();
      expect(fireEvent.keyDown(control, { key: "1" })).toBe(true);
    }
    expect(run).not.toHaveBeenCalled();
  });
  test("suppresses bare keys for displayed textareas and editable surfaces even when unfocused", () => {
    const run = vi.fn();
    const view = render(
      <Commands run={run}>
        <textarea aria-label="Notes" />
      </Commands>,
    );
    fireEvent.keyDown(screen.getByRole("button"), { key: "4" });
    expect(run).not.toHaveBeenCalled();
    view.rerender(
      <Commands run={run}>
        <div contentEditable role="textbox" suppressContentEditableWarning>
          Draft
        </div>
      </Commands>,
    );
    fireEvent.keyDown(screen.getByRole("button"), { key: "4" });
    expect(run).not.toHaveBeenCalled();
  });
  test("leaves visible menu typeahead and modal focus scopes alone", () => {
    const run = vi.fn();
    const view = render(
      <Commands run={run}>
        <div role="menu">
          <button role="menuitem">Item</button>
        </div>
      </Commands>,
    );
    fireEvent.keyDown(document, { key: "2" });
    expect(run).not.toHaveBeenCalled();
    view.rerender(
      <Commands run={run}>
        <div role="dialog" aria-label="Review">
          <button>Close</button>
        </div>
      </Commands>,
    );
    fireEvent.keyDown(screen.getByRole("button", { name: "Close" }), { key: "2" });
    expect(run).not.toHaveBeenCalled();
  });
  test("pages one owning table with clamped steps, using the main table by default", () => {
    render(
      <main>
        <button>Content</button>
        <Pager name="Files" />
        <Pager name="History" primary />
      </main>,
    );
    const history = screen.getByRole("navigation", { name: "History pages" }),
      files = screen.getByRole("navigation", { name: "Files pages" });
    fireEvent.keyDown(screen.getByRole("button", { name: "Content" }), {
      key: "ArrowRight",
      shiftKey: true,
    });
    expect(history).toHaveTextContent("Page 17 of 58");
    expect(files).toHaveTextContent("Page 12 of 58");
    const row = screen.getByRole("button", { name: "Files row" });
    row.focus();
    fireEvent.keyDown(row, { key: "ArrowLeft", shiftKey: true });
    expect(files).toHaveTextContent("Page 7 of 58");
    fireEvent.keyDown(row, { key: "ArrowLeft", shiftKey: true });
    expect(files).toHaveTextContent("Page 2 of 58");
    fireEvent.keyDown(row, { key: "ArrowLeft", shiftKey: true });
    expect(files).toHaveTextContent("Page 1 of 58");
    fireEvent.keyDown(row, { key: "ArrowLeft" });
    expect(files).toHaveTextContent("Page 1 of 58");
    expect(history).toHaveTextContent("Page 17 of 58");
    const content = screen.getByRole("button", { name: "Content" });
    content.focus();
    fireEvent.keyDown(content, { key: "ArrowRight" });
    expect(files).toHaveTextContent("Page 2 of 58");
  });
  test("keeps busy pager states steady while ignoring repeated keyboard activation", () => {
    render(<Pager name="Busy" busy />);
    const nav = screen.getByRole("navigation");
    for (const b of within(nav).getAllByRole("button")) expect(b).toBeEnabled();
    fireEvent.keyDown(screen.getByRole("button", { name: "Busy row" }), { key: "ArrowRight" });
    expect(nav).toHaveTextContent("Page 12 of 58");
  });
  test("chooses the deepest table region when a detail pager is nested inside another", () => {
    render(
      <section data-paging-region>
        <PageNavigation
          label="Outer"
          value={{ page: 1, totalPages: 8 }}
          onPage={vi.fn()}
          keyboardDefault
        />
        <Pager name="Nested" />
      </section>,
    );
    const row = screen.getByRole("button", { name: "Nested row" });
    row.focus();
    fireEvent.keyDown(row, { key: "ArrowRight" });
    expect(screen.getByRole("navigation", { name: "Nested pages" })).toHaveTextContent(
      "Page 13 of 58",
    );
    expect(screen.getByRole("navigation", { name: "Outer" })).toHaveTextContent("Page 1 of 8");
  });
  test("falls back to the primary table after the remembered region leaves the viewport", () => {
    render(
      <main>
        <button>Content</button>
        <Pager name="Files" />
        <Pager name="History" primary />
      </main>,
    );
    const row = screen.getByRole("button", { name: "Files row" });
    row.focus();
    fireEvent.keyDown(row, { key: "ArrowRight" });
    expect(screen.getByRole("navigation", { name: "Files pages" })).toHaveTextContent(
      "Page 13 of 58",
    );
    vi.spyOn(
      screen.getByRole("region", { name: "Files" }),
      "getBoundingClientRect",
    ).mockReturnValue({
      top: -100,
      bottom: -50,
      left: 0,
      right: 200,
      width: 200,
      height: 50,
    } as DOMRect);
    const content = screen.getByRole("button", { name: "Content" });
    content.focus();
    fireEvent.keyDown(content, { key: "ArrowRight" });
    expect(screen.getByRole("navigation", { name: "History pages" })).toHaveTextContent(
      "Page 13 of 58",
    );
    expect(screen.getByRole("navigation", { name: "Files pages" })).toHaveTextContent(
      "Page 13 of 58",
    );
  });
  test("pages only the modal outside its search field and preserves focused text editing", () => {
    const view = render(
      <>
        <Pager name="Background" primary />
        <div role="dialog" aria-label="Details">
          <Pager name="Dialog" />
        </div>
      </>,
    );
    fireEvent.keyDown(document, { key: "ArrowRight" });
    expect(screen.getByRole("navigation", { name: "Dialog pages" })).toHaveTextContent(
      "Page 13 of 58",
    );
    expect(screen.getByRole("navigation", { name: "Background pages" })).toHaveTextContent(
      "Page 12 of 58",
    );
    view.rerender(
      <>
        <Pager name="Background" primary />
        <div role="dialog" aria-label="Details">
          <input type="search" />
          <Pager name="Dialog" />
        </div>
      </>,
    );
    fireEvent.keyDown(document, { key: "ArrowRight" });
    expect(screen.getByRole("navigation", { name: "Dialog pages" })).toHaveTextContent(
      "Page 13 of 58",
    );
    expect(screen.getByRole("navigation", { name: "Background pages" })).toHaveTextContent(
      "Page 12 of 58",
    );
    const search = screen.getByRole("searchbox");
    search.focus();
    for (const shiftKey of [false, true])
      for (const key of ["ArrowLeft", "ArrowRight"]) {
        expect(fireEvent.keyDown(search, { key, shiftKey })).toBe(true);
        expect(screen.getByRole("navigation", { name: "Dialog pages" })).toHaveTextContent(
          "Page 13 of 58",
        );
      }
    const next = within(screen.getByRole("navigation", { name: "Dialog pages" })).getByRole(
      "button",
      { name: "Next" },
    );
    next.focus();
    expect(fireEvent.keyDown(next, { key: "ArrowRight", shiftKey: true })).toBe(false);
    expect(screen.getByRole("navigation", { name: "Dialog pages" })).toHaveTextContent(
      "Page 18 of 58",
    );
    expect(fireEvent.keyDown(next, { key: "ArrowLeft", shiftKey: true })).toBe(false);
    expect(screen.getByRole("navigation", { name: "Dialog pages" })).toHaveTextContent(
      "Page 13 of 58",
    );
    expect(screen.getByRole("navigation", { name: "Background pages" })).toHaveTextContent(
      "Page 12 of 58",
    );
  });
  test("removes document handlers and paging targets when their owners unmount", () => {
    const run = vi.fn();
    const view = render(<Commands run={run} />);
    fireEvent.keyDown(document, { key: "1" });
    expect(run).toHaveBeenCalledTimes(1);
    view.unmount();
    expect(fireEvent.keyDown(document, { key: "1" })).toBe(true);
    expect(run).toHaveBeenCalledTimes(1);
  });
});
