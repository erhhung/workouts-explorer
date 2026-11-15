export function createConsistencyScrollGroup() {
  const panes = new Set<HTMLElement>();
  const expected = new WeakMap<HTMLElement, number>();
  let position: number | undefined;
  let atEnd = true;
  function reconcile() {
    const visible = [...panes].filter((pane) => pane.clientWidth > 0);
    if (!visible.length) return;
    const maximum = Math.max(0, ...visible.map((pane) => pane.scrollWidth - pane.clientWidth));
    position = atEnd || position === undefined ? maximum : Math.min(position, maximum);
    for (const pane of visible) {
      if (Math.abs(pane.scrollLeft - position) > 0.1) {
        pane.scrollLeft = position;
        expected.set(pane, pane.scrollLeft);
      } else if (expected.has(pane) && Math.abs(expected.get(pane)! - pane.scrollLeft) > 0.1)
        expected.delete(pane);
    }
  }
  return {
    register(pane: HTMLElement) {
      panes.add(pane);
      return () => {
        panes.delete(pane);
        expected.delete(pane);
      };
    },
    reconcile,
    synchronize(pane: HTMLElement) {
      if (!pane.clientWidth) return;
      const programmed = expected.get(pane);
      expected.delete(pane);
      if (programmed !== undefined && Math.abs(programmed - pane.scrollLeft) < 0.5) return;
      position = pane.scrollLeft;
      atEnd = pane.scrollWidth - pane.clientWidth - position < 1;
      reconcile();
    },
  };
}
