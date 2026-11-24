/** Where this browser keeps the owner's order of the row catalogue's groups. */
export const GROUP_ORDER_KEY = "ether.designer.groupOrder";

export function loadGroupOrder<K extends string>(): K[] {
  try {
    const raw: unknown = JSON.parse(localStorage.getItem(GROUP_ORDER_KEY) ?? "[]");
    return Array.isArray(raw) ? (raw.filter((k) => typeof k === "string") as K[]) : [];
  } catch {
    return [];
  }
}

export function saveGroupOrder<K extends string>(order: K[]) {
  try {
    localStorage.setItem(GROUP_ORDER_KEY, JSON.stringify(order));
  } catch {
    // The order still applies for this visit without storage.
  }
}

/** Groups in the saved order; groups the order does not name keep their place after it. */
export function orderGroups<T extends { key: string }>(
  groups: T[],
  order: readonly T["key"][],
): T[] {
  const rank = new Map(order.map((k, i) => [k, i]));
  return groups
    .map((g, i) => ({ g, i }))
    .sort(
      (a, b) =>
        (rank.get(a.g.key) ?? order.length + a.i) - (rank.get(b.g.key) ?? order.length + b.i),
    )
    .map(({ g }) => g);
}

/**
 * The full order after the visible groups were reordered to `visible`: hidden
 * groups (no rows right now) keep their slots, visible ones fill theirs in
 * the new order.
 */
export function mergeGroupOrder<K extends string>(full: K[], visible: K[]): K[] {
  const shown = new Set(visible);
  let next = 0;
  return full.map((k) => (shown.has(k) ? (visible[next++] as K) : k));
}
