export const decimal = s => typeof s === "string" && /^(0|[1-9][0-9]*)$/.test(s) &&
  (s.length < 19 || s.length === 19 && s <= "9223372036854775807");

export function compareRevision(a, b) {
  return a.length === b.length ? (a === b ? 0 : a < b ? -1 : 1) : a.length < b.length ? -1 : 1;
}
