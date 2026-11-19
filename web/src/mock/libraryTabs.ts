import type {
  LibraryFacet,
  LibraryTab,
  LibraryTabKind,
  LibraryTabsResponse,
} from "../pages/libraries/api";
import { COLLECTIONS, NETWORKS, PEOPLE, STUDIOS, type FakeTitle } from "./data";

const VALID_KINDS = new Set<LibraryTabKind>([
  "recommended",
  "library",
  "collections",
  "people",
  "view",
]);

function decadeOf(year: number): string {
  return year ? `${Math.floor(year / 10) * 10}s` : "Unknown";
}

export function createLibraryTabsMock({
  catalog,
}: {
  catalog: FakeTitle[];
}) {
  // Initial owner overrides so empty and customized libraries can be inspected right away.
  const ownerTabs = new Map<number, LibraryTab[]>([
    [4, []],
    [
      7,
      [
        { key: "recommended", label: "Recommended", kind: "recommended" },
        {
          key: "library",
          label: "All Anime",
          kind: "library",
          sort: "rating",
          direction: "desc",
          search: true,
          searchPlaceholder: "Search anime",
        },
        {
          key: "studios",
          label: "Studios",
          kind: "collections",
          facet: "studio",
          order: "count",
          sort: "rating",
          direction: "desc",
          search: true,
          searchPlaceholder: "Find a studio",
        },
        {
          key: "cast",
          label: "Voice Cast",
          kind: "people",
          facet: "cast",
          order: "name",
          search: true,
          searchPlaceholder: "Find a voice actor",
        },
        {
          key: "unwatched",
          label: "Unwatched",
          kind: "view",
          sort: "added",
          direction: "desc",
          search: true,
          filters: {
            match: "all",
            rules: [
              { field: "viewer", op: "is", value: { userId: 0, unwatched: true } },
              { field: "rating", op: "gte", value: 7.5 },
            ],
          },
        },
        {
          key: "classics",
          label: "90s Classics",
          kind: "view",
          sort: "year",
          direction: "asc",
          search: true,
          hidden: true,
          filters: {
            match: "all",
            rules: [{ field: "year", op: "between", value: [1990, 1999] }],
          },
        },
      ],
    ],
  ]);

  function facetsFor(lib: Record<string, unknown>): LibraryFacet[] {
    if (lib.source === "plugin") {
      const kind = lib.kind as
        | { fields?: { key: string; label: string; type: string; indexed?: boolean }[] }
        | undefined;
      const fromKind: LibraryFacet[] = (kind?.fields ?? [])
        .filter((f) => f.indexed !== false)
        .map((f) => ({
          id: f.key,
          label: f.label,
          type: f.type === "people" || f.key === "author" || f.key === "narrator" ? "people" : "strings",
        }));
      const extras: LibraryFacet[] = [
        { id: "subject", label: "Subject", type: "strings" },
        { id: "series", label: "Series", type: "strings" },
        { id: "publisher", label: "Publisher", type: "strings" },
        { id: "author", label: "Author", type: "people" },
        { id: "narrator", label: "Narrator", type: "people" },
      ];
      const seen = new Set(fromKind.map((f) => f.id));
      for (const ex of extras) {
        if (!seen.has(ex.id)) {
          seen.add(ex.id);
          fromKind.push(ex);
        }
      }
      return fromKind;
    }
    const mediaType = String(lib.mediaType ?? "");
    const facets: LibraryFacet[] = [
      { id: "genre", label: "Genre", type: "strings" },
      { id: "studio", label: "Studio", type: "strings" },
    ];
    if (mediaType !== "movie") {
      facets.push({ id: "network", label: "Network", type: "strings" });
    }
    if (mediaType !== "show") {
      facets.push({ id: "franchise", label: "Franchise", type: "strings" });
    }
    facets.push(
      { id: "country", label: "Country", type: "strings" },
      { id: "decade", label: "Decade", type: "strings" },
      { id: "year", label: "Year", type: "strings" },
      { id: "cast", label: "Actors", type: "people" },
      { id: "director", label: "Directors", type: "people" },
    );
    return facets;
  }

  function defaultTabsFor(lib: Record<string, unknown>): {
    source: "plugin" | "default";
    tabs: LibraryTab[];
  } {
    if (lib.source === "plugin") {
      const name = String(lib.name ?? "Items");
      return {
        source: "plugin",
        tabs: [
          { key: "recommended", label: "Recommended", kind: "recommended" },
          {
            key: "library",
            label: name,
            kind: "library",
            sort: "title",
            direction: "asc",
            search: true,
            searchPlaceholder: `Search ${name.toLowerCase()}`,
          },
          {
            key: "subjects",
            label: "Subjects",
            kind: "collections",
            facet: "subject",
            order: "count",
            sort: "title",
            direction: "asc",
            search: true,
            searchPlaceholder: "Find a subject",
          },
          {
            key: "authors",
            label: "Authors",
            kind: "people",
            facet: "author",
            order: "name",
            search: true,
            searchPlaceholder: "Find an author",
          },
        ],
      };
    }
    return {
      source: "default",
      tabs: [
        { key: "recommended", label: "Recommended", kind: "recommended" },
        {
          key: "library",
          label: "Library",
          kind: "library",
          sort: "added",
          direction: "desc",
          search: true,
        },
      ],
    };
  }

  function getTabs(lib: Record<string, unknown>): LibraryTabsResponse {
    const id = Number(lib.id);
    const facets = facetsFor(lib);
    if (ownerTabs.has(id)) {
      return {
        tabs: structuredClone(ownerTabs.get(id) ?? []),
        source: "owner",
        facets,
      };
    }
    const def = defaultTabsFor(lib);
    return {
      tabs: structuredClone(def.tabs),
      source: def.source,
      facets,
    };
  }

  function putTabs(
    lib: Record<string, unknown>,
    rawTabs: unknown,
  ): { ok: true; data: LibraryTabsResponse } | { ok: false; message: string } {
    if (!Array.isArray(rawTabs)) {
      return { ok: false, message: "tabs: must be an array" };
    }
    const seen = new Set<string>();
    const normalized: LibraryTab[] = [];
    for (let i = 0; i < rawTabs.length; i++) {
      const t = rawTabs[i] as Partial<LibraryTab> | null;
      if (!t || typeof t !== "object") {
        return { ok: false, message: `tabs[${i}]: invalid tab` };
      }
      if (!t.kind || !VALID_KINDS.has(t.kind)) {
        return { ok: false, message: `tabs[${i}].kind: invalid tab kind` };
      }
      const label = String(t.label ?? "").trim();
      if (!label) {
        return { ok: false, message: `tabs[${i}].label: is required` };
      }
      const key = String(t.key ?? "").trim();
      if (!/^[a-z][a-z0-9-]{0,31}$/.test(key)) {
        return { ok: false, message: `tabs[${i}].key: invalid tab key "${key}"` };
      }
      if (seen.has(key)) {
        return { ok: false, message: `tabs[${i}].key: duplicate tab key "${key}"` };
      }
      seen.add(key);
      if ((t.kind === "collections" || t.kind === "people") && !t.facet) {
        return { ok: false, message: `tabs[${i}].facet: is required` };
      }
      normalized.push(structuredClone({ ...t, key, label } as LibraryTab));
    }
    ownerTabs.set(Number(lib.id), normalized);
    return { ok: true, data: getTabs(lib) };
  }

  function resetTabs(lib: Record<string, unknown>): LibraryTabsResponse {
    ownerTabs.delete(Number(lib.id));
    return getTabs(lib);
  }

  function facetValuesFor(
    lib: Record<string, unknown>,
    field: string,
  ): { field: string; values: { value: string; title?: string; count: number }[] } {
    if (lib.source === "plugin") {
      const pluginBuckets: Record<string, { value: string; count: number }[]> = {
        author: [
          { value: "Herbert", count: 1 },
          { value: "Ursula K. Le Guin", count: 4 },
          { value: "Isaac Asimov", count: 3 },
          { value: "William Gibson", count: 2 },
        ],
        narrator: [
          { value: "Simon Vance", count: 4 },
          { value: "Scott Brick", count: 3 },
          { value: "Robin Miles", count: 2 },
        ],
        subject: [
          { value: "Science Fiction", count: 6 },
          { value: "Space Opera", count: 4 },
          { value: "Cyberpunk", count: 2 },
          { value: "Classic Literature", count: 2 },
        ],
        series: [
          { value: "Dune Chronicles", count: 3 },
          { value: "Hainish Cycle", count: 3 },
          { value: "Foundation", count: 2 },
        ],
        publisher: [
          { value: "Ace Books", count: 5 },
          { value: "Chilton Books", count: 2 },
          { value: "Spectra", count: 3 },
        ],
      };
      return {
        field,
        values: pluginBuckets[field] ?? [{ value: "Herbert", count: 1 }],
      };
    }

    const mediaType = String(lib.mediaType ?? "");
    const pool = catalog.filter(
      (x) => !mediaType || mediaType === "mixed" || x.type === mediaType,
    );
    const counts = new Map<string, { title: string; count: number }>();
    const bump = (key: string, title = key) => {
      if (!key) return;
      const prev = counts.get(key);
      counts.set(key, { title: prev?.title ?? title, count: (prev?.count ?? 0) + 1 });
    };

    for (const item of pool) {
      if (field === "genre") {
        for (const g of item.genres ?? []) bump(g);
      } else if (field === "studio") {
        const st = STUDIOS.find((s) => s.id === item.studio);
        bump(st?.name ?? String(item.studio));
      } else if (field === "network") {
        const nw = NETWORKS[item.studio % NETWORKS.length];
        if (nw) bump(nw.name);
      } else if (field === "franchise") {
        if (item.year !== undefined) {
          const col = COLLECTIONS[item.year % COLLECTIONS.length];
          if (col) bump(col.name);
        }
      } else if (field === "country") {
        bump(item.country);
      } else if (field === "decade") {
        if (item.year !== undefined) bump(decadeOf(item.year));
      } else if (field === "year") {
        if (item.year !== undefined) bump(String(item.year));
      } else if (field === "cast") {
        for (const pid of item.people?.slice(0, 3) ?? []) {
          const p = PEOPLE.find((person) => person.id === pid);
          if (p) bump(p.name);
        }
      } else if (field === "director") {
        const pid = item.people?.[0];
        const p = PEOPLE.find((person) => person.id === pid);
        if (p) bump(p.name);
      }
    }

    if (counts.size === 0 && (field === "cast" || field === "director")) {
      for (const [idx, p] of PEOPLE.slice(0, 6).entries()) {
        bump(p.name, p.name);
        counts.set(p.name, { title: p.name, count: 8 - idx });
      }
    }

    const values = [...counts.entries()]
      .sort((a, b) => b[1].count - a[1].count || a[0].localeCompare(b[0]))
      .map(([value, info]) => ({ value, title: info.title, count: info.count }));
    return { field, values };
  }

  return {
    getTabs,
    putTabs,
    resetTabs,
    facetValuesFor,
  };
}
