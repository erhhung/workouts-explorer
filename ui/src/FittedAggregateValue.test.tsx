import { act, render, screen } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { FittedAggregateValue } from "./FittedAggregateValue";

function measurements(initialWidth: number) {
  let width = initialWidth;
  const maximum = 38.4;
  const computed = window.getComputedStyle.bind(window);
  vi.spyOn(window, "getComputedStyle").mockImplementation((element) =>
    element.classList.contains("aggregate-fit-value")
      ? ({
          fontSize: (element instanceof HTMLElement && element.style.fontSize) || `${maximum}px`,
        } as CSSStyleDeclaration)
      : computed(element),
  );
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (
    this: HTMLElement,
  ) {
    return new DOMRect(0, 0, this.classList.contains("aggregate-fit-value") ? width : 0, 40);
  });
  vi.spyOn(document, "createRange").mockImplementation(() => {
    let node: HTMLElement;
    return {
      selectNodeContents(element: HTMLElement) {
        node = element;
      },
      getBoundingClientRect() {
        return new DOMRect(
          0,
          0,
          (node.textContent?.length ?? 0) *
            parseFloat(node.style.fontSize || String(maximum)) *
            0.5,
          40,
        );
      },
    } as unknown as Range;
  });
  return {
    maximum,
    resize(next: number) {
      width = next;
      window.dispatchEvent(new Event("resize"));
    },
  };
}

test("fits the overflowing line by a fractional amount and no more than necessary", () => {
  const width = 228,
    { maximum } = measurements(width);
  render(<FittedAggregateValue>167d 12h 06m 45s</FittedAggregateValue>);
  const value = screen.getByText("167d 12h 06m 45s"),
    size = parseFloat(value.style.fontSize),
    chars = value.textContent!.length;
  expect(size).toBeLessThan(maximum);
  expect(chars * size * 0.5).toBeLessThanOrEqual(width);
  expect(chars * (size + 0.02) * 0.5).toBeGreaterThan(width);
});

test("keeps the CSS size for fitting content and restores it when the value gets shorter", () => {
  measurements(228);
  const view = render(<FittedAggregateValue>3h 20m</FittedAggregateValue>);
  expect(screen.getByText("3h 20m").style.fontSize).toBe("");
  view.rerender(<FittedAggregateValue>167d 12h 06m 45s</FittedAggregateValue>);
  expect(screen.getByText("167d 12h 06m 45s").style.fontSize).not.toBe("");
  view.rerender(<FittedAggregateValue>3h 20m</FittedAggregateValue>);
  expect(screen.getByText("3h 20m").style.fontSize).toBe("");
});

test("refits on available-width changes and never grows above the CSS maximum", () => {
  const measure = measurements(228);
  render(<FittedAggregateValue>167d 12h 06m 45s</FittedAggregateValue>);
  const value = screen.getByText("167d 12h 06m 45s"),
    first = parseFloat(value.style.fontSize);
  act(() => measure.resize(200));
  expect(parseFloat(value.style.fontSize)).toBeLessThan(first);
  act(() => measure.resize(1000));
  expect(value.style.fontSize).toBe("");
});

test("fits each aggregate and paired count independently through the same implementation", () => {
  const { maximum } = measurements(228);
  const values = [
    "123,456,789,012",
    "12",
    "167d 12h 06m 45s",
    "123,456,789 km",
    "123,456,789 kcal",
  ];
  render(
    <>
      {values.map((value, index) => (
        <FittedAggregateValue
          key={index}
          className={index === 1 ? "aggregate-routed-count" : undefined}
          aria-describedby={index === 1 ? "route-count-label" : undefined}
        >
          {value}
        </FittedAggregateValue>
      ))}
    </>,
  );
  for (const value of values.filter((value) => value !== "12")) {
    const element = screen.getByText(value),
      size = parseFloat(element.style.fontSize);
    expect(size).toBeLessThan(maximum);
    expect(value.length * size * 0.5).toBeLessThanOrEqual(228);
  }
  const routed = screen.getByText("12");
  expect(routed.style.fontSize).toBe("");
  expect(routed).toHaveClass("aggregate-routed-count", "aggregate-fit-value");
  expect(routed).toHaveAttribute("aria-describedby", "route-count-label");
  expect(screen.getByText(values[0]).style.fontSize).not.toBe(
    screen.getByText(values[4]).style.fontSize,
  );
});
