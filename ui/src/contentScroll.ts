export interface ContentScrollPosition {
  x: number;
  y: number;
  owner?: HTMLElement;
}

export function captureContentScroll(anchor: Element | null): ContentScrollPosition {
  const owner = anchor?.closest<HTMLElement>("[data-app-scroll]");
  return owner
    ? { owner, x: owner.scrollLeft, y: owner.scrollTop }
    : { x: window.scrollX, y: window.scrollY };
}

export function currentContentScroll(position: ContentScrollPosition) {
  return position.owner
    ? { x: position.owner.scrollLeft, y: position.owner.scrollTop }
    : { x: window.scrollX, y: window.scrollY };
}

export function restoreContentScroll(position: ContentScrollPosition, y = position.y) {
  if (!position.owner) window.scrollTo(position.x, y);
  else if (position.owner.isConnected) {
    position.owner.scrollLeft = position.x;
    position.owner.scrollTop = y;
  }
}
