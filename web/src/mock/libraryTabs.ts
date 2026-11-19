import type { CollectionSpec, Rule, TitleCard } from "../lib/contract";
import type {
  LibraryAvailableFacets,
  LibraryFacetOption,
  LibraryTab,
  LibraryTabKind,
  LibraryTabPreviewResponse,
  LibraryTabsResponse,
} from "../pages/libraries/api";
import { COLLECTIONS, NETWORKS, PEOPLE, STUDIOS, type FakeTitle } from "./data";

const SCOPE_NOTE = "Tabs show in Vela. Plex, Jellyfin and Emby keep their own library tabs.";

const VALID_KINDS = new Set<LibraryTabKind>([
  "recommended",
  "library",
  "collections",
  "people",
  "view",
]);

const CATALOGUE_SORTS = [
  "popularity",
  "added",
  "released",
  "year",
  "rating",
  "duration",
  "title",
  "server",
];

const CATALOGUE_COLLECTION_FACETS = new Set([
  "",
  "genre",
  "studio",
  "network",
  "country",
  "year",
  "decade",
  "franchise",
]);

const CATALOGUE_PEOPLE_FACETS = new Set(["cast", "director"]);

function decadeOf(year: number): string {
  return year ? `${Math.floor(year / 10) * 10}s` : "Unknown";
}

function validTabKey(key: string): boolean {
  return /^[a-z0-9][a-z0-9_-]{0,63}$/.test(key);
}

export function createLibraryTabsMock({
  catalog,
  getCollections = () => [],
  matchesRule = () => true,
  toCard,
}: {
  catalog: FakeTitle[];
  getCollections?: () => CollectionSpec[];
  matchesRule?: (title: FakeTitle, rule: Rule) => boolean;
  toCard?: (title: FakeTitle) => TitleCard;
}) {
  // Initial owner overrides so customized libraries can be inspected right away.
  const ownerTabs = new Map<number, LibraryTab[]>([
    [
      4,
      [
        {
          key: "library",
          label: "Weekend Picks",
          kind: "library",
          sort: "rating",
          direction: "desc",
          search: true,
        },
      ],
    ],
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

  function availableFacetsFor(lib: Record<string, unknown>): LibraryAvailableFacets {
    if (lib.source === "plugin") {
      const kind = lib.kind as
        | {
            fields?: { key: string; label: string; type: string; indexed?: boolean }[];
            browse?: { facets?: string[] };
          }
        | undefined;
      const collections: LibraryFacetOption[] = [];
      const people: LibraryFacetOption[] = [];
      const seenCol = new Set<string>();
      const seenPeople = new Set<string>();

      for (const f of kind?.fields ?? []) {
        if (f.indexed === false) continue;
        const isPeople = f.type === "people" || f.key === "author" || f.key === "narrator";
        if (isPeople) {
          if (!seenPeople.has(f.key)) {
            seenPeople.add(f.key);
            people.push({ id: f.key, label: f.label });
          }
        } else if (!seenCol.has(f.key)) {
          seenCol.add(f.key);
          collections.push({ id: f.key, label: f.label });
        }
      }
      return { collections, people };
    }

    const mediaType = String(lib.mediaType ?? "");
    const collections: LibraryFacetOption[] = [
      { id: "", label: "Owner collections" },
      { id: "genre", label: "Genres" },
    ];
    if (mediaType === "show") {
      collections.push({ id: "network", label: "Networks" });
    } else if (mediaType === "movie") {
      collections.push({ id: "studio", label: "Studios" });
    } else {
      collections.push(
        { id: "studio", label: "Studios" },
        { id: "network", label: "Networks" },
      );
    }
    collections.push(
      { id: "country", label: "Countries" },
      { id: "year", label: "Years" },
      { id: "decade", label: "Decades" },
    );
    if (mediaType !== "show") {
      collections.push({ id: "franchise", label: "Franchises" });
    }
    return {
      collections,
      people: [
        { id: "cast", label: "Actors" },
        { id: "director", label: "Directors" },
      ],
    };
  }

  function availableSortsFor(lib: Record<string, unknown>): string[] {
    if (lib.source === "plugin") {
      const kind = lib.kind as { browse?: { sorts?: string[] } } | undefined;
      const sorts = kind?.browse?.sorts ?? [];
      return sorts.length > 0 ? [...sorts] : ["server"];
    }
    return [...CATALOGUE_SORTS];
  }

  function defaultTabsFor(lib: Record<string, unknown>): LibraryTab[] {
    if (lib.source === "plugin") {
      const name = String(lib.name ?? "Items");
      return [
        { key: "recommended", label: "Recommended", kind: "recommended", search: false },
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
      ];
    }
    return [
      { key: "recommended", label: "Recommended", kind: "recommended", search: false },
      {
        key: "library",
        label: "Library",
        kind: "library",
        search: true,
      },
    ];
  }

  function withEffectiveSearch(tabs: LibraryTab[]): LibraryTab[] {
    return tabs.map((t) => ({
      ...structuredClone(t),
      search: t.kind === "recommended" ? false : t.search !== false,
    }));
  }

  function getTabs(lib: Record<string, unknown>): LibraryTabsResponse {
    const id = Number(lib.id);
    const defaults = defaultTabsFor(lib);
    const customized = ownerTabs.has(id);
    const stored = customized ? structuredClone(ownerTabs.get(id) ?? []) : [];
    const effectiveTabs = customized ? withEffectiveSearch(stored) : structuredClone(defaults);
    return {
      libraryId: id,
      customized,
      tabs: stored,
      effectiveTabs,
      defaults,
      scopeNote: SCOPE_NOTE,
      availableFacets: availableFacetsFor(lib),
      availableSorts: availableSortsFor(lib),
    };
  }

  function validateTabList(
    lib: Record<string, unknown>,
    rawTabs: unknown,
  ): { ok: true; tabs: LibraryTab[] } | { ok: false; message: string } {
    if (!Array.isArray(rawTabs)) {
      return { ok: false, message: "Invalid tabs payload" };
    }
    if (rawTabs.length === 0) {
      return { ok: false, message: "invalid: library must have at least one tab" };
    }
    if (rawTabs.length > 16) {
      return { ok: false, message: "invalid: library may have at most 16 tabs" };
    }

    const isPlugin = lib.source === "plugin";
    const facets = availableFacetsFor(lib);
    const sorts = new Set(availableSortsFor(lib));
    const seen = new Set<string>();
    const normalized: LibraryTab[] = [];
    let visibleCount = 0;

    for (let i = 0; i < rawTabs.length; i++) {
      const raw = rawTabs[i] as Partial<LibraryTab> | null;
      if (!raw || typeof raw !== "object") {
        return { ok: false, message: `tab ${i + 1}: invalid tab` };
      }
      const label = String(raw.label ?? "").trim();
      if (!label || label.length > 80) {
        return { ok: false, message: `invalid: tab ${i + 1} needs a label (1-80 characters)` };
      }
      const key = String(raw.key ?? "")
        .trim()
        .toLowerCase();
      if (!validTabKey(key)) {
        return { ok: false, message: `invalid: invalid tab key "${key}"` };
      }
      if (seen.has(key)) {
        return { ok: false, message: `invalid: duplicate tab key "${key}"` };
      }
      seen.add(key);

      const kind = String(raw.kind ?? "")
        .trim()
        .toLowerCase() as LibraryTabKind;
      if (!VALID_KINDS.has(kind)) {
        return { ok: false, message: `invalid: tab "${key}" has unknown kind "${kind}"` };
      }

      const facet = String(raw.facet ?? "").trim();
      if (kind === "collections") {
        if (isPlugin) {
          if (!facet || !facets.collections.some((f) => f.id === facet)) {
            return {
              ok: false,
              message: `invalid: plugin collections tab "${key}" requires a declared facet`,
            };
          }
        } else if (!CATALOGUE_COLLECTION_FACETS.has(facet)) {
          return {
            ok: false,
            message: `invalid: tab "${key}" has unsupported collections facet "${facet}"`,
          };
        }
      } else if (kind === "people") {
        if (isPlugin) {
          if (!facet || !facets.people.some((f) => f.id === facet)) {
            return {
              ok: false,
              message: `invalid: plugin people tab "${key}" requires a declared people facet`,
            };
          }
        } else if (!CATALOGUE_PEOPLE_FACETS.has(facet)) {
          return {
            ok: false,
            message: `invalid: tab "${key}" has unsupported people facet "${facet}"`,
          };
        }
      } else if (facet !== "") {
        return { ok: false, message: `invalid: tab "${key}" of kind ${kind} cannot set facet` };
      }

      const order = raw.order ? String(raw.order).trim().toLowerCase() : "";
      const allowsOrder = kind === "people" || (kind === "collections" && facet !== "");
      if (order) {
        if (!allowsOrder) {
          return { ok: false, message: `invalid: tab "${key}" cannot set order` };
        }
        if (order !== "count" && order !== "name") {
          return { ok: false, message: `invalid: tab "${key}" order must be count or name` };
        }
      }

      const sort = raw.sort ? String(raw.sort).trim() : "";
      const direction = raw.direction ? String(raw.direction).trim().toLowerCase() : "";
      const allowsSort =
        kind === "library" || kind === "view" || (kind === "collections" && facet !== "");
      if (!allowsSort) {
        if (sort || direction) {
          return { ok: false, message: `invalid: tab "${key}" cannot set sort or direction` };
        }
      } else {
        if (sort && !sorts.has(sort)) {
          return { ok: false, message: `invalid: tab "${key}" has unsupported sort "${sort}"` };
        }
        if (direction && direction !== "asc" && direction !== "desc") {
          return { ok: false, message: `invalid: tab "${key}" direction must be asc or desc` };
        }
      }

      if (kind === "recommended" && raw.search === true) {
        return {
          ok: false,
          message: `invalid: recommended tab "${key}" cannot enable in-tab search`,
        };
      }

      if (raw.filters && raw.filters.rules?.length && kind !== "view" && kind !== "library") {
        return { ok: false, message: `invalid: tab "${key}" of kind ${kind} cannot set filters` };
      }

      const tab: LibraryTab = { key, label, kind };
      if (kind === "collections" && facet) tab.facet = facet;
      if (kind === "people") tab.facet = facet;
      if (order === "count" || order === "name") tab.order = order;
      if (sort) tab.sort = sort;
      if (direction === "asc" || direction === "desc") tab.direction = direction;
      if (kind !== "recommended") {
        if (raw.search !== undefined) tab.search = Boolean(raw.search);
        const ph = String(raw.searchPlaceholder ?? "").trim();
        if (ph) tab.searchPlaceholder = ph;
      }
      if (raw.filters && raw.filters.rules?.length) {
        tab.filters = structuredClone(raw.filters);
      }
      if (raw.hidden) {
        tab.hidden = true;
      } else {
        visibleCount++;
      }
      normalized.push(tab);
    }

    if (visibleCount === 0) {
      return { ok: false, message: "invalid: at least one tab must be visible" };
    }
    return { ok: true, tabs: normalized };
  }

  function putTabs(
    lib: Record<string, unknown>,
    body: unknown,
  ): { ok: true; data: LibraryTabsResponse } | { ok: false; message: string } {
    const rawList = Array.isArray(body)
      ? body
      : body && typeof body === "object"
        ? (body as { tabs?: unknown }).tabs
        : undefined;
    const res = validateTabList(lib, rawList);
    if (!res.ok) return res;
    ownerTabs.set(Number(lib.id), res.tabs);
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

  function previewTab(
    lib: Record<string, unknown>,
    body: unknown,
  ): { ok: true; data: LibraryTabPreviewResponse } | { ok: false; message: string } {
    const obj = (body && typeof body === "object" ? body : {}) as Record<string, unknown>;
    const rawTab = ("tab" in obj ? obj.tab : obj) as Partial<LibraryTab> | null;
    if (!rawTab || typeof rawTab !== "object") {
      return { ok: false, message: "Invalid tab preview body" };
    }
    const query = String(obj.query ?? "")
      .trim()
      .toLowerCase();
    const rawLimit = Number(obj.limit ?? 12);
    const limit = rawLimit > 0 ? Math.min(48, rawLimit) : 12;

    const candidate: Partial<LibraryTab> = {
      ...rawTab,
      key: String(rawTab.key ?? "").trim() || "preview",
      label: String(rawTab.label ?? "").trim() || "Preview",
      hidden: false,
    };
    const validated = validateTabList(lib, [candidate]);
    if (!validated.ok) return validated;
    const tab = validated.tabs[0]!;

    if (tab.kind === "recommended") {
      return {
        ok: true,
        data: { kind: "recommended", scopeNote: SCOPE_NOTE },
      };
    }

    if (tab.kind === "collections") {
      const facet = tab.facet ?? "";
      if (facet === "") {
        const mediaType = String(lib.mediaType ?? "");
        const matched = getCollections().filter((sp) => {
          if (mediaType && sp.mediaType && sp.mediaType !== "mixed" && sp.mediaType !== mediaType) {
            return false;
          }
          if (query && !sp.name.toLowerCase().includes(query)) return false;
          return true;
        });
        return {
          ok: true,
          data: {
            kind: "collections",
            facet: "",
            order: "server",
            collections: matched.slice(0, limit).map((sp) => ({
              id: String(sp.id ?? sp.name),
              name: sp.name,
              itemCount: sp.itemCount ?? 0,
            })),
            more: matched.length > limit,
          },
        };
      }

      const order = tab.order ?? "count";
      const buckets = [...facetValuesFor(lib, facet).values].filter((b) =>
        query ? (b.title ?? b.value).toLowerCase().includes(query) : true,
      );
      if (order === "name") {
        buckets.sort((a, b) =>
          (a.title ?? a.value).localeCompare(b.title ?? b.value, undefined, {
            sensitivity: "base",
          }),
        );
      } else {
        buckets.sort((a, b) => b.count - a.count);
      }
      return {
        ok: true,
        data: {
          kind: "collections",
          facet,
          order,
          collections: buckets.slice(0, limit).map((b) => ({
            id: `facet:${facet}:${b.value}`,
            bucketId: b.value,
            name: b.title ?? b.value,
            itemCount: b.count,
          })),
          more: buckets.length > limit,
        },
      };
    }

    if (tab.kind === "people") {
      const facet = tab.facet ?? "";
      const order = tab.order ?? "name";
      const buckets = [...facetValuesFor(lib, facet).values].filter((b) =>
        query ? (b.title ?? b.value).toLowerCase().includes(query) : true,
      );
      if (order === "name") {
        buckets.sort((a, b) =>
          (a.title ?? a.value).localeCompare(b.title ?? b.value, undefined, {
            sensitivity: "base",
          }),
        );
      } else {
        buckets.sort((a, b) => b.count - a.count);
      }
      return {
        ok: true,
        data: {
          kind: "people",
          facet,
          order,
          people: buckets.slice(0, limit).map((b, idx) => ({
            id: idx + 1,
            name: b.title ?? b.value,
            itemCount: b.count,
          })),
          more: buckets.length > limit,
        },
      };
    }

    // kind === "library" | "view"
    const defaultSort = lib.source === "plugin" ? "title" : "popularity";
    const sort = tab.sort || defaultSort;
    const direction =
      tab.direction ??
      (sort === "server" || sort === "title" || sort === "index" ? "asc" : "desc");

    if (lib.source === "plugin") {
      const items = [
        { key: 1, title: "Dune", year: 1965 },
        { key: 2, title: "The Left Hand of Darkness", year: 1969 },
      ].filter((it) => (query ? it.title.toLowerCase().includes(query) : true));
      return {
        ok: true,
        data: {
          kind: tab.kind,
          sort,
          direction,
          total: items.length,
          items: items.slice(0, limit),
        },
      };
    }

    const mediaType = String(lib.mediaType ?? "");
    let pool = catalog.filter(
      (x) => !mediaType || mediaType === "mixed" || x.type === mediaType,
    );
    if (query) {
      pool = pool.filter((x) => x.title.toLowerCase().includes(query));
    }
    if (tab.filters?.rules?.length) {
      const matchAny = tab.filters.match === "any";
      pool = pool.filter((item) =>
        matchAny
          ? tab.filters!.rules.some((r) => matchesRule(item, r))
          : tab.filters!.rules.every((r) => matchesRule(item, r)),
      );
    }

    const dirMul = direction === "asc" ? 1 : -1;
    pool.sort((a, b) => {
      switch (sort) {
        case "title":
        case "server":
          return a.title.localeCompare(b.title) * dirMul;
        case "added":
          return a.addedAt.localeCompare(b.addedAt) * dirMul;
        case "released":
          return a.released.localeCompare(b.released) * dirMul;
        case "year":
          return ((a.year ?? 0) - (b.year ?? 0)) * dirMul;
        case "rating":
          return ((a.rating ?? 0) - (b.rating ?? 0)) * dirMul;
        case "duration":
          return ((a.runtimeMinutes ?? 0) - (b.runtimeMinutes ?? 0)) * dirMul;
        case "popularity":
        default:
          return (a.popularity - b.popularity) * dirMul;
      }
    });

    const mapped = pool.slice(0, limit).map((item) =>
      toCard
        ? toCard(item)
        : {
            key: item.key,
            title: item.title,
            year: item.year,
            poster: item.poster,
          },
    );

    return {
      ok: true,
      data: {
        kind: tab.kind,
        sort,
        direction,
        total: pool.length,
        items: mapped,
      },
    };
  }

  return {
    getTabs,
    putTabs,
    resetTabs,
    previewTab,
    facetValuesFor,
  };
}
