import { afterEach, describe, expect, it } from "vitest";
import {
  GROUP_ORDER_KEY,
  loadGroupOrder,
  mergeGroupOrder,
  orderGroups,
  saveGroupOrder,
} from "./groupOrder";

const groups = ["picks", "explore", "shows", "household", "collections", "plex"].map((key) => ({
  key,
}));

describe("row catalogue group order", () => {
  afterEach(() => localStorage.clear());

  it("keeps the built-in order with nothing saved", () => {
    expect(orderGroups(groups, []).map((g) => g.key)).toEqual(groups.map((g) => g.key));
  });

  it("applies a saved order and keeps unnamed groups after it in their own order", () => {
    expect(orderGroups(groups, ["collections", "picks"]).map((g) => g.key)).toEqual([
      "collections",
      "picks",
      "explore",
      "shows",
      "household",
      "plex",
    ]);
  });

  it("reorders the visible groups without moving hidden ones", () => {
    const full = ["picks", "explore", "shows", "household", "collections", "plex"];
    // shows and plex have no rows right now: Collections dragged to the top.
    expect(mergeGroupOrder(full, ["collections", "picks", "explore", "household"])).toEqual([
      "collections",
      "picks",
      "shows",
      "explore",
      "household",
      "plex",
    ]);
  });

  it("round-trips through storage and ignores a broken value", () => {
    saveGroupOrder(["plex", "picks"]);
    expect(loadGroupOrder()).toEqual(["plex", "picks"]);
    localStorage.setItem(GROUP_ORDER_KEY, "{not json");
    expect(loadGroupOrder()).toEqual([]);
    localStorage.setItem(GROUP_ORDER_KEY, JSON.stringify({ a: 1 }));
    expect(loadGroupOrder()).toEqual([]);
  });
});
