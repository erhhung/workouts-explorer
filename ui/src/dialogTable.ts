export function dialogTablePageSize(preferred: number) {
  return Math.max(1, Math.min(25, preferred));
}

export function dialogTableRowSlots(unfilteredCount: number | undefined, preferred: number) {
  const maximum = dialogTablePageSize(preferred);
  return Math.max(1, Math.min(maximum, unfilteredCount ?? maximum));
}
