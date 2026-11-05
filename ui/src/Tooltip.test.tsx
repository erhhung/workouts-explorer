import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";
import { Tooltip } from "./Tooltip";

const originalElementFromPoint = Object.getOwnPropertyDescriptor(document, "elementFromPoint");
afterEach(() => {
  if (originalElementFromPoint)
    Object.defineProperty(document, "elementFromPoint", originalElementFromPoint);
  else Reflect.deleteProperty(document, "elementFromPoint");
});

function rect(x: number, y: number, width: number, height: number) {
  return {
    x,
    y,
    left: x,
    top: y,
    right: x + width,
    bottom: y + height,
    width,
    height,
    toJSON() {},
  };
}

function setup(anchor = rect(140, 120, 20, 20)) {
  let currentAnchor = anchor;
  let bounds = rect(0, 0, 320, 240);
  vi.spyOn(window, "innerWidth", "get").mockReturnValue(320);
  vi.spyOn(window, "innerHeight", "get").mockReturnValue(240);
  const result = render(
    <div data-testid="scroller" style={{ overflow: "auto" }}>
      <Tooltip portal content="Route readiness" label="Route status">
        Ready
      </Tooltip>
    </div>,
  );
  const trigger = screen.getByLabelText("Route status");
  const bubble = screen.getByRole("tooltip");
  const scroller = screen.getByTestId("scroller");
  vi.spyOn(trigger, "getBoundingClientRect").mockImplementation(() => currentAnchor);
  vi.spyOn(bubble, "getBoundingClientRect").mockReturnValue(rect(0, 0, 100, 40));
  vi.spyOn(scroller, "getBoundingClientRect").mockImplementation(() => bounds);
  return {
    ...result,
    trigger,
    bubble,
    scroller,
    move: (next: DOMRect) => {
      currentAnchor = next;
    },
    clip: (next: DOMRect) => {
      bounds = next;
    },
  };
}

describe("Tooltip", () => {
  test("keeps default markup inline and leaves interaction to CSS", () => {
    const add = vi.spyOn(window, "addEventListener");
    const { container } = render(
      <Tooltip content="Inline help" className="custom" label="Help" focusable={false}>
        Label
      </Tooltip>,
    );
    const trigger = screen.getByLabelText("Help");
    const bubble = screen.getByRole("tooltip");
    expect(container.firstChild).toHaveClass("tooltip", "custom");
    expect(trigger).not.toHaveAttribute("tabindex");
    expect(trigger).toHaveAttribute("aria-describedby", bubble.id);
    expect(trigger.nextElementSibling).toBe(bubble);
    expect(bubble).toHaveAttribute("class", "tooltip-content");
    expect(bubble).not.toHaveAttribute("style");
    fireEvent.pointerEnter(trigger);
    fireEvent.focus(trigger);
    fireEvent.keyDown(trigger, { key: "Escape" });
    expect(bubble).toHaveAttribute("class", "tooltip-content");
    expect(add).not.toHaveBeenCalled();
  });

  test("mounts the portal under body, links its trigger, and measures above on entry", () => {
    const { container, trigger, bubble, scroller } = setup();
    expect(bubble.parentElement).toBe(document.body);
    expect(container).not.toContainElement(bubble);
    expect(scroller).not.toContainElement(bubble);
    expect(trigger).toHaveAttribute("aria-describedby", bubble.id);
    expect(trigger).toHaveAttribute("tabindex", "0");
    expect(bubble).toHaveClass("tooltip-content--portal");
    expect(bubble).not.toHaveClass("is-open");
    vi.spyOn(bubble, "getBoundingClientRect").mockImplementation(() => {
      expect(bubble).not.toHaveClass("is-open");
      return rect(0, 0, 100, 40);
    });
    fireEvent.pointerEnter(trigger);
    expect(bubble).toHaveClass("is-open");
    expect(bubble).toHaveStyle({ top: "74px", left: "100px" });
    fireEvent.pointerLeave(trigger);
    expect(bubble).not.toHaveClass("is-open");
    expect(screen.getByText("Route readiness")).toBe(bubble);
  });

  test("enables a single-line portal without remounting its focused child and removes disabled help", () => {
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (
      this: HTMLElement,
    ) {
      return this.classList.contains("tooltip-content")
        ? rect(0, 0, 500, 24)
        : rect(140, 120, 100, 20);
    });
    const content = "North Meadow Regional Preserve and Cedar Valley County Park";
    const renderTooltip = (enabled: boolean) => (
      <Tooltip portal singleLine enabled={enabled} focusable={false} content={content}>
        <button>Park</button>
      </Tooltip>
    );
    const view = render(renderTooltip(false));
    const button = screen.getByRole("button", { name: "Park" });
    const trigger = button.parentElement!;
    fireEvent.focus(button);
    fireEvent.pointerEnter(trigger);
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
    expect(trigger).not.toHaveAttribute("aria-describedby");
    expect(trigger).not.toHaveAttribute("tabindex");
    button.focus();
    view.rerender(renderTooltip(true));
    const bubble = screen.getByRole("tooltip");
    expect(bubble).toHaveClass("tooltip-content--single-line", "is-open");
    expect(bubble).toHaveTextContent(content);
    expect(trigger).toHaveAttribute("aria-describedby", bubble.id);
    expect(screen.getByRole("button", { name: "Park" })).toBe(button);
    expect(button).toHaveFocus();
    view.rerender(renderTooltip(false));
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
    expect(trigger).not.toHaveAttribute("aria-describedby");
    fireEvent.pointerLeave(trigger);
    fireEvent.blur(button);
    view.rerender(renderTooltip(true));
    expect(screen.getByRole("tooltip")).not.toHaveClass("is-open");
  });

  test("flips below a trigger near the viewport top", () => {
    const { trigger, bubble } = setup(rect(140, 10, 20, 20));
    fireEvent.focus(trigger);
    expect(bubble).toHaveStyle({ top: "36px", left: "100px" });
  });

  test.each([
    [0, "8px"],
    [300, "212px"],
  ])("clamps horizontal placement for an anchor at x=%s", (x, left) => {
    const { trigger, bubble } = setup(rect(x, 120, 20, 20));
    fireEvent.pointerEnter(trigger);
    expect(bubble).toHaveStyle({ left });
  });

  test("clamps vertical placement when neither side has enough room", () => {
    const { trigger, bubble } = setup(rect(140, 20, 20, 20));
    vi.spyOn(bubble, "getBoundingClientRect").mockReturnValue(rect(0, 0, 100, 220));
    fireEvent.pointerEnter(trigger);
    expect(bubble).toHaveStyle({ top: "12px" });
  });

  test("follows captured scroll and resize, then hides once clipped by a scroll ancestor", () => {
    const { trigger, bubble, scroller, move, clip } = setup();
    fireEvent.pointerEnter(trigger);
    move(rect(160, 90, 20, 20));
    fireEvent.scroll(scroller);
    expect(bubble).toHaveStyle({ top: "44px", left: "120px" });
    move(rect(200, 100, 20, 20));
    fireEvent.resize(window);
    expect(bubble).toHaveStyle({ top: "54px", left: "160px" });
    vi.spyOn(window, "innerWidth", "get").mockReturnValue(240);
    fireEvent.resize(window);
    expect(bubble).toHaveStyle({ left: "132px" });
    clip(rect(0, 115, 320, 125));
    fireEvent.scroll(scroller);
    expect(bubble).toHaveClass("is-open");
    move(rect(200, 90, 20, 20));
    fireEvent.scroll(scroller);
    expect(bubble).not.toHaveClass("is-open");
    const measure = vi.spyOn(trigger, "getBoundingClientRect");
    measure.mockClear();
    fireEvent.scroll(scroller);
    fireEvent.resize(window);
    expect(measure).not.toHaveBeenCalled();
  });

  test("hides when its anchor leaves the viewport", () => {
    const { trigger, bubble, move } = setup();
    fireEvent.focus(trigger);
    move(rect(140, 240, 20, 20));
    fireEvent.scroll(window);
    expect(bubble).not.toHaveClass("is-open");
  });

  test("wheel scrolling hides a disabled row that moved away without pointerleave", () => {
    const { trigger, bubble, scroller, move } = setup();
    let hit: Element | null = trigger;
    const hitTest = vi.fn(() => hit);
    Object.defineProperty(document, "elementFromPoint", { configurable: true, value: hitTest });
    fireEvent(
      trigger,
      new MouseEvent("pointerover", {
        bubbles: true,
        clientX: 150,
        clientY: 130,
      }),
    );
    expect(bubble).toHaveClass("is-open");
    move(rect(140, 110, 20, 20));
    fireEvent.scroll(scroller);
    expect(bubble).toHaveClass("is-open");
    hit = scroller;
    move(rect(140, 80, 20, 20));
    fireEvent.scroll(scroller);
    expect(bubble).not.toHaveClass("is-open");
    expect(hitTest).toHaveBeenCalledWith(150, 130);
    fireEvent.scroll(scroller);
    expect(bubble).not.toHaveClass("is-open");
  });

  test("new disabled-row hover owns the only portal even while an older row keeps keyboard focus", () => {
    const view = render(
      <div>
        <Tooltip portal content="Map data not available">
          <button disabled>First route</button>
        </Tooltip>
        <Tooltip portal content="Map data not available">
          <button disabled>Second route</button>
        </Tooltip>
      </div>,
    );
    const [first, second] = view.container.querySelectorAll<HTMLElement>(".tooltip-trigger");
    const bubbles = screen.getAllByRole("tooltip");
    for (const trigger of [first, second])
      vi.spyOn(trigger, "getBoundingClientRect").mockReturnValue(rect(140, 120, 100, 20));
    for (const bubble of bubbles)
      vi.spyOn(bubble, "getBoundingClientRect").mockReturnValue(rect(0, 0, 180, 24));
    let hit: Element | null = first;
    Object.defineProperty(document, "elementFromPoint", { configurable: true, value: () => hit });
    act(() => first.focus());
    expect(bubbles[0]).toHaveClass("is-open");
    hit = second;
    fireEvent(
      second,
      new MouseEvent("pointerover", {
        bubbles: true,
        clientX: 150,
        clientY: 130,
      }),
    );
    expect(first).toHaveFocus();
    expect(bubbles[0]).not.toHaveClass("is-open");
    expect(bubbles[1]).toHaveClass("is-open");
    fireEvent.scroll(window);
    expect(document.querySelectorAll(".tooltip-content--portal.is-open")).toHaveLength(1);
    fireEvent.pointerLeave(second);
    expect(document.querySelectorAll(".tooltip-content--portal.is-open")).toHaveLength(0);
  });

  test("preserves pointer/focus overlap and dismisses on Escape or final blur", async () => {
    const user = userEvent.setup();
    const { trigger, bubble } = setup();
    await user.tab();
    expect(trigger).toHaveFocus();
    fireEvent.pointerEnter(trigger);
    fireEvent.pointerLeave(trigger);
    expect(bubble).toHaveClass("is-open");
    fireEvent.keyDown(trigger, { key: "Escape" });
    expect(bubble).not.toHaveClass("is-open");
    expect(trigger).toHaveFocus();
    fireEvent.blur(trigger);
    fireEvent.focus(trigger);
    expect(bubble).toHaveClass("is-open");
    fireEvent.pointerEnter(trigger);
    fireEvent.blur(trigger);
    expect(bubble).toHaveClass("is-open");
    fireEvent.pointerLeave(trigger);
    expect(bubble).not.toHaveClass("is-open");
    fireEvent.focus(trigger);
    fireEvent.blur(trigger);
    expect(bubble).not.toHaveClass("is-open");
  });

  test("keeps focus open when moving between descendants", () => {
    const { container } = render(
      <Tooltip portal content="Nested help" focusable={false}>
        <button>First</button>
        <button>Second</button>
      </Tooltip>,
    );
    const trigger = container.querySelector<HTMLElement>(".tooltip-trigger")!;
    const bubble = screen.getByRole("tooltip");
    vi.spyOn(trigger, "getBoundingClientRect").mockReturnValue(rect(140, 120, 20, 20));
    vi.spyOn(bubble, "getBoundingClientRect").mockReturnValue(rect(0, 0, 100, 40));
    fireEvent.focus(screen.getByText("First"));
    fireEvent.blur(screen.getByText("First"), { relatedTarget: screen.getByText("Second") });
    expect(bubble).toHaveClass("is-open");
    fireEvent.blur(screen.getByText("Second"));
    expect(bubble).not.toHaveClass("is-open");
  });

  test("detaches listeners on close and unmount and removes the portaled bubble", () => {
    const add = vi.spyOn(window, "addEventListener");
    const remove = vi.spyOn(window, "removeEventListener");
    const { trigger, bubble, unmount } = setup();
    fireEvent.focus(trigger);
    fireEvent.blur(trigger);
    for (const name of ["scroll", "resize", "keydown"]) {
      const call = add.mock.calls.find(([type]) => type === name)!;
      expect(call).toBeDefined();
      expect(remove.mock.calls).toContainEqual(call);
    }
    add.mockClear();
    remove.mockClear();
    fireEvent.focus(trigger);
    const measure = vi.spyOn(trigger, "getBoundingClientRect");
    unmount();
    expect(remove.mock.calls).toEqual(add.mock.calls);
    measure.mockClear();
    fireEvent.scroll(window);
    fireEvent.resize(window);
    expect(measure).not.toHaveBeenCalled();
    expect(bubble).not.toBeInTheDocument();
  });
});
