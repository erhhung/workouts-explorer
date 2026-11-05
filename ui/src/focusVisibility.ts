export function installFocusVisibility(document: Document = window.document) {
  const root = document.documentElement;
  const previous = root.dataset.inputModality;
  root.dataset.inputModality = "pointer";

  function keyboard(event: KeyboardEvent) {
    if (event.altKey || event.ctrlKey || event.metaKey) return;
    const target = event.target as HTMLElement | null;
    const editing = target?.matches?.(
      "textarea, [contenteditable='true'], input:not([type='checkbox']):not([type='radio']):not([type='button']):not([type='submit'])",
    );
    if (
      event.key === "Tab" ||
      (!editing &&
        ["ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key))
    )
      if (root.dataset.inputModality !== "keyboard") root.dataset.inputModality = "keyboard";
  }

  function pointer() {
    if (root.dataset.inputModality !== "pointer") root.dataset.inputModality = "pointer";
  }

  document.addEventListener("keydown", keyboard, true);
  document.addEventListener("pointerdown", pointer, true);
  return () => {
    document.removeEventListener("keydown", keyboard, true);
    document.removeEventListener("pointerdown", pointer, true);
    if (previous === undefined) delete root.dataset.inputModality;
    else root.dataset.inputModality = previous;
  };
}
