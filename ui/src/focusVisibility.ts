export function installFocusVisibility(document: Document = window.document) {
  const root = document.documentElement;
  const previous = root.dataset.inputModality;
  root.dataset.inputModality = "pointer";

  function keyboard(event: KeyboardEvent) {
    if (event.key !== "Tab" || event.altKey || event.ctrlKey || event.metaKey) return;
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
