import { expect, test, vi } from "vitest";
import { captureContentScroll, currentContentScroll, restoreContentScroll } from "./contentScroll";

test("uses the nearest content owner and restores an anchored offset without scrolling the window", () => {
  const outer = document.createElement("div"),
    owner = document.createElement("div"),
    button = document.createElement("button");
  outer.dataset.appScroll = "";
  owner.dataset.appScroll = "";
  outer.append(owner);
  owner.append(button);
  document.body.append(outer);
  const windowScroll = vi.spyOn(window, "scrollTo");
  outer.scrollTop = 20;
  owner.scrollTop = 800;
  owner.scrollLeft = 15;
  const saved = captureContentScroll(button);
  expect(saved.owner).toBe(owner);
  owner.scrollTop = 100;
  expect(currentContentScroll(saved).y).toBe(100);
  restoreContentScroll(saved);
  expect(owner.scrollTop).toBe(800);
  restoreContentScroll(saved, 950);
  expect(owner.scrollTop).toBe(950);
  expect(owner.scrollLeft).toBe(15);
  expect(outer.scrollTop).toBe(20);
  expect(windowScroll).not.toHaveBeenCalled();
  outer.remove();
});
test("ignores a detached private owner instead of falling back to the window", () => {
  const owner = document.createElement("div");
  owner.dataset.appScroll = "";
  document.body.append(owner);
  owner.scrollTop = 100;
  const saved = captureContentScroll(owner);
  owner.remove();
  const scroll = vi.spyOn(window, "scrollTo");
  restoreContentScroll(saved);
  expect(scroll).not.toHaveBeenCalled();
});
test("supports document scroll for standalone content outside the authenticated shell", () => {
  const scroll = vi.spyOn(window, "scrollTo");
  const position = captureContentScroll(null);
  expect(position.owner).toBeUndefined();
  restoreContentScroll({ ...position, x: 12, y: 34 });
  expect(scroll).toHaveBeenLastCalledWith(12, 34);
});
