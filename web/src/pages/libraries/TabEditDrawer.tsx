import {
  Badge,
  Button,
  Input,
  Select,
  Switch,
  buttonVariants,
  cn,
} from "@braedonsaunders/appkit-ui";
import {
  CheckCircle2,
  ExternalLink,
  Filter,
  Folders,
  Library as LibraryIcon,
  Plus,
  Sparkles,
  Users,
} from "lucide-react";
import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Artwork } from "../../components/Artwork";
import { Drawer } from "../../components/Drawer";
import { Segmented } from "../../components/fields";
import { t, tn } from "../../i18n";
import { useLocale } from "../../i18n/react";
import { api, apiFetch } from "../../lib/api";
import { asObj } from "../settings/SchemaForm";
import type { Rule, RuleField } from "../../lib/contract";
import { formatNumber } from "../../lib/format";
import { useAsync, useDebouncedValue } from "../../lib/hooks";
import { fieldMeta, isRuleComplete, type FieldMeta } from "../../lib/rules";
import { titlePosterURL } from "../../lib/titles";
import { RuleRow } from "../collections/RuleRow";
import {
  librariesApi,
  type Library,
  type LibraryFacet,
  type LibraryTab,
  type LibraryTabKind,
} from "./api";
import {
  ExtendedCondition,
  browseFields,
  newBrowseCondition,
} from "./browser/RuleCondition";

const RULE_FIELDS: RuleField[] = [
  "title",
  "genre",
  "keyword",
  "studio",
  "country",
  "language",
  "contentRating",
  "year",
  "rating",
  "votes",
  "popularity",
  "runtime",
];

const CATALOG_SORTS = [
  "added",
  "title",
  "released",
  "year",
  "rating",
  "popularity",
  "duration",
] as const;

function metaForField(field: RuleField): FieldMeta | undefined {
  const base = fieldMeta(field);
  if (!base) return undefined;
  if (field === "title") return { ...base, label: t("Specific titles") };
  const extra = [...base.ops, "unknown" as const, "known" as const];
  if (field === "keyword")
    return { ...base, kind: "facet", facet: "keyword", ops: extra, defaultOp: "in" };
  if (field === "studio")
    return {
      ...base,
      label: t("Studio or network"),
      kind: "facet",
      facet: "studio",
      ops: extra,
      defaultOp: "in",
    };
  if (field === "contentRating") return base;
  if (base.kind === "facet" || base.kind === "number") return { ...base, ops: extra };
  return base;
}

function pruneIncompleteRule(rule: Rule): Rule | null {
  if (rule.field === "group") {
    if (!Array.isArray(rule.value)) return null;
    const children = (rule.value as Rule[])
      .map(pruneIncompleteRule)
      .filter((c): c is Rule => c !== null);
    return children.length > 0 ? { ...rule, value: children } : null;
  }
  return isRuleComplete(rule) ? rule : null;
}

export function sortFieldLabel(sort: string): string {
  switch (sort) {
    case "title":
      return t("Title");
    case "added":
      return t("Date added");
    case "released":
      return t("Release date");
    case "year":
      return t("Year");
    case "rating":
      return t("Rating");
    case "popularity":
      return t("Popularity");
    case "duration":
      return t("Runtime");
    case "index":
      return t("Order");
    default:
      return sort;
  }
}

function directionOptions(sort: string): { value: "asc" | "desc"; label: string }[] {
  if (sort === "title") {
    return [
      { value: "asc", label: t("A–Z") },
      { value: "desc", label: t("Z–A") },
    ];
  }
  if (sort === "index") {
    return [
      { value: "asc", label: t("First to last") },
      { value: "desc", label: t("Last to first") },
    ];
  }
  if (sort === "added" || sort === "released" || sort === "year") {
    return [
      { value: "desc", label: t("Newest first") },
      { value: "asc", label: t("Oldest first") },
    ];
  }
  if (sort === "duration") {
    return [
      { value: "desc", label: t("Longest first") },
      { value: "asc", label: t("Shortest first") },
    ];
  }
  return [
    { value: "desc", label: t("Highest first") },
    { value: "asc", label: t("Lowest first") },
  ];
}

export function tabKindIcon(kind: LibraryTabKind, size = 16) {
  switch (kind) {
    case "recommended":
      return <Sparkles size={size} />;
    case "library":
      return <LibraryIcon size={size} />;
    case "collections":
      return <Folders size={size} />;
    case "people":
      return <Users size={size} />;
    case "view":
      return <Filter size={size} />;
  }
}

export function tabKindTitle(kind: LibraryTabKind): string {
  switch (kind) {
    case "recommended":
      return t("Recommended");
    case "library":
      return t("Everything");
    case "collections":
      return t("Collections by…");
    case "people":
      return t("People in…");
    case "view":
      return t("Saved view");
  }
}

export function slugTabKey(label: string, kind: LibraryTabKind, existingKeys: Set<string>): string {
  const raw = label
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
  let base = raw && /^[a-z]/.test(raw) ? raw : raw ? `tab-${raw}` : kind;
  base = base.slice(0, 26).replace(/-+$/g, "") || kind;
  if (!existingKeys.has(base)) return base;
  let n = 2;
  while (existingKeys.has(`${base}-${n}`)) n++;
  return `${base}-${n}`;
}

export function defaultLabelForFacet(facet: LibraryFacet | undefined, kind: LibraryTabKind): string {
  if (!facet) return kind === "people" ? t("People") : t("Collections");
  switch (facet.id) {
    case "genre":
      return t("Genres");
    case "studio":
      return t("Studios");
    case "network":
      return t("Networks");
    case "country":
      return t("Countries");
    case "decade":
      return t("Decades");
    case "year":
      return t("Years");
    case "franchise":
      return t("Franchises");
    case "subject":
      return t("Subjects");
    case "series":
      return t("Series");
    case "publisher":
      return t("Publishers");
    case "cast":
      return t("Cast");
    case "director":
      return t("Directors");
    case "author":
      return t("Authors");
    case "narrator":
      return t("Narrators");
    default:
      return facet.label;
  }
}

export function createSeedTab(
  kind: LibraryTabKind,
  library: Library,
  facets: LibraryFacet[],
  existingKeys: Set<string>,
): LibraryTab {
  const defaultSort =
    library.source === "plugin" ? (library.kind?.browse.sorts?.[0] ?? "title") : "added";
  const defaultDir: "asc" | "desc" = defaultSort === "title" || defaultSort === "index" ? "asc" : "desc";

  if (kind === "recommended") {
    const label = t("Recommended");
    return {
      key: slugTabKey("recommended", kind, existingKeys),
      label,
      kind: "recommended",
    };
  }
  if (kind === "library") {
    const label = library.source === "plugin" ? library.name : t("Library");
    return {
      key: slugTabKey("library", kind, existingKeys),
      label,
      kind: "library",
      sort: defaultSort,
      direction: defaultDir,
      search: true,
    };
  }
  if (kind === "collections") {
    const facet = facets.find((f) => f.type === "strings");
    const label = defaultLabelForFacet(facet, "collections");
    return {
      key: slugTabKey(facet?.id ?? "collections", kind, existingKeys),
      label,
      kind: "collections",
      facet: facet?.id ?? "",
      order: "count",
      sort: defaultSort,
      direction: defaultDir,
      search: true,
    };
  }
  if (kind === "people") {
    const facet = facets.find((f) => f.type === "people");
    const label = defaultLabelForFacet(facet, "people");
    return {
      key: slugTabKey(facet?.id ?? "people", kind, existingKeys),
      label,
      kind: "people",
      facet: facet?.id ?? "",
      order: "name",
      search: true,
    };
  }
  const label = t("Unwatched");
  const seedRule: Rule =
    library.source === "plugin"
      ? newBrowseCondition("year", library.kind)
      : { field: "viewer", op: "is", value: { userId: 0, unwatched: true } };
  return {
    key: slugTabKey("unwatched", kind, existingKeys),
    label,
    kind: "view",
    sort: defaultSort,
    direction: defaultDir,
    search: true,
    filters: { match: "all", rules: [seedRule] },
  };
}

export function AddTabPickerDrawer({
  open,
  library,
  facets,
  existingTabs,
  onPick,
  onClose,
}: {
  open: boolean;
  library: Library;
  facets: LibraryFacet[];
  existingTabs: LibraryTab[];
  onPick: (kind: LibraryTabKind) => void;
  onClose: () => void;
}) {
  const { locale } = useLocale();
  const hasStringsFacet = facets.some((f) => f.type === "strings");
  const hasPeopleFacet = facets.some((f) => f.type === "people");
  const hasRecommended = existingTabs.some((tab) => tab.kind === "recommended");

  const cards = useMemo(
    () => [
      {
        kind: "recommended" as const,
        title: t("Recommended"),
        description: t(
          "Personalized rows and hubs arranged for this library in the Home Designer.",
        ),
        badge: hasRecommended ? t("Already in tabs") : undefined,
        disabled: false,
      },
      {
        kind: "library" as const,
        title: t("Everything"),
        description: t(
          "Every item in {name}, with a default sort and optional filter.",
          { name: library.name },
        ),
        disabled: false,
      },
      {
        kind: "collections" as const,
        title: t("Collections by…"),
        description: hasStringsFacet
          ? t("Group items into browsable buckets like genre, studio, decade or subject.")
          : t("No collection facets are indexed for this library."),
        disabled: !hasStringsFacet,
      },
      {
        kind: "people" as const,
        title: t("People in…"),
        description: hasPeopleFacet
          ? t("Browse actors, directors, authors or narrators in this library.")
          : t("No people facets are indexed for this library."),
        disabled: !hasPeopleFacet,
      },
      {
        kind: "view" as const,
        title: t("Saved view"),
        description: t(
          "A filtered slice of this library — such as Unwatched, 4K or 90s Classics.",
        ),
        disabled: false,
      },
    ],
    [hasPeopleFacet, hasRecommended, hasStringsFacet, library.name, locale],
  );

  return (
    <Drawer
      open={open}
      onClose={onClose}
      title={t("Add tab")}
      description={t("Choose what this tab shows when viewers open {name} in Vela.", {
        name: library.name,
      })}
      size="md"
    >
      <div className="space-y-2.5">
        {cards.map((card) => (
          <button
            key={card.kind}
            type="button"
            disabled={card.disabled}
            onClick={() => onPick(card.kind)}
            className={cn(
              "flex w-full items-start gap-3.5 rounded-xl border border-border bg-surface p-3.5 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
              card.disabled
                ? "cursor-not-allowed opacity-50"
                : "hover:border-primary/40 hover:bg-surface-hover",
            )}
          >
            <span className="mt-0.5 grid size-9 shrink-0 place-items-center rounded-lg bg-primary-subtle text-primary">
              {tabKindIcon(card.kind, 18)}
            </span>
            <span className="min-w-0 flex-1">
              <span className="flex flex-wrap items-center gap-2">
                <span className="text-sm font-semibold text-fg">{card.title}</span>
                {card.badge ? <Badge variant="secondary">{card.badge}</Badge> : null}
              </span>
              <span className="mt-0.5 block text-xs text-fg-muted">{card.description}</span>
            </span>
          </button>
        ))}
      </div>
    </Drawer>
  );
}

type VisibilityPreviewResponse = {
  total: number;
  hidden: number;
  visible?: number;
  shown?: { key: string; title: string; year?: number; poster?: string; posterPath?: string }[] | null;
};

export function TabEditDrawer({
  open,
  isNew,
  initialTab,
  library,
  facets,
  existingKeys,
  saving,
  onSave,
  onDelete,
  onClose,
}: {
  open: boolean;
  isNew: boolean;
  initialTab: LibraryTab | null;
  library: Library;
  facets: LibraryFacet[];
  existingKeys: Set<string>;
  saving: boolean;
  onSave: (tab: LibraryTab) => void;
  onDelete?: () => void;
  onClose: () => void;
}) {
  if (!open || !initialTab) return null;
  return (
    <TabEditDrawerInner
      key={`${isNew ? "new" : "edit"}:${initialTab.key}:${initialTab.kind}`}
      isNew={isNew}
      initialTab={initialTab}
      library={library}
      facets={facets}
      existingKeys={existingKeys}
      saving={saving}
      onSave={onSave}
      onDelete={onDelete}
      onClose={onClose}
    />
  );
}

function TabEditDrawerInner({
  isNew,
  initialTab,
  library,
  facets,
  existingKeys,
  saving,
  onSave,
  onDelete,
  onClose,
}: {
  isNew: boolean;
  initialTab: LibraryTab;
  library: Library;
  facets: LibraryFacet[];
  existingKeys: Set<string>;
  saving: boolean;
  onSave: (tab: LibraryTab) => void;
  onDelete?: () => void;
  onClose: () => void;
}) {
  const config = useAsync(() => api.getConfig(), []);
  const region = String(asObj(config.data?.catalog).region ?? "");
  const [draft, setDraft] = useState<LibraryTab>(() => structuredClone(initialTab));
  const [labelTouched, setLabelTouched] = useState(!isNew);
  const [freshRuleIndex, setFreshRuleIndex] = useState<number | null>(null);

  const isPlugin = library.source === "plugin";
  const mediaType: "movie" | "show" | "mixed" =
    library.mediaType === "movie" || library.mediaType === "show" ? library.mediaType : "mixed";

  const availableSorts: string[] = isPlugin
    ? library.kind?.browse.sorts?.length
      ? library.kind.browse.sorts
      : ["title"]
    : [...CATALOG_SORTS];

  const matchingFacets = facets.filter((f) =>
    draft.kind === "people" ? f.type === "people" : f.type === "strings",
  );

  const supportsFacet = draft.kind === "collections" || draft.kind === "people";
  const supportsRules = draft.kind === "view" || draft.kind === "library";
  const supportsSort =
    draft.kind === "library" || draft.kind === "view" || draft.kind === "collections";
  const supportsSearch = draft.kind !== "recommended";

  // Facet live preview (bucket count + sample buckets)
  const facetPreview = useAsync(
    (signal) =>
      supportsFacet && draft.facet
        ? librariesApi.facets(library.id, draft.facet, "", signal)
        : Promise.resolve(null),
    [library.id, supportsFacet, draft.facet],
  );

  // Rules live preview (matching title count + sample posters)
  const rules = draft.filters?.rules ?? [];
  const matchMode = draft.filters?.match === "any" ? "any" : "all";
  // i18n: memo holds no copy
  const completeRules = useMemo(
    () => rules.map(pruneIncompleteRule).filter((r): r is Rule => r !== null),
    [rules],
  );
  const allRulesComplete = rules.every(isRuleComplete);

  // i18n: memo holds no copy
  const previewSpec = useMemo(
    () => ({
      enabled: supportsRules && !isPlugin,
      libraryId: library.id,
      mediaType,
      match: matchMode,
      rules: completeRules,
    }),
    [supportsRules, isPlugin, library.id, mediaType, matchMode, completeRules],
  );
  const debouncedSpec = useDebouncedValue(previewSpec, 300);

  const rulesPreview = useAsync(
    async (signal) => {
      if (!debouncedSpec.enabled) return null;
      const hasRules = debouncedSpec.rules.length > 0;
      return apiFetch<VisibilityPreviewResponse>("/catalog/visibility/preview", {
        method: "POST",
        body: JSON.stringify({
          libraryId: debouncedSpec.libraryId,
          exclusions: hasRules
            ? [
                {
                  name: "",
                  action: "show",
                  mediaType: debouncedSpec.mediaType,
                  match: debouncedSpec.match,
                  rules: debouncedSpec.rules,
                },
              ]
            : [],
          visibilityDefault: hasRules ? "hide" : "show",
        }),
        signal,
      });
    },
    [debouncedSpec],
  );

  const pluginPreview = useAsync(
    (signal) =>
      supportsRules && isPlugin ? librariesApi.items(library.id, { limit: "12" }, signal) : Promise.resolve(null),
    [supportsRules, isPlugin, library.id],
  );

  // Plugin field mapping for RuleRow
  // i18n: memo holds no copy
  const { allowedFields, pluginFields } = useMemo(() => {
    if (!isPlugin) return { allowedFields: RULE_FIELDS, pluginFields: undefined };
    const aliases: Record<string, string[]> = {
      genre: ["genre", "genres", "tags", "tag", "subject"],
      studio: ["studio", "studios", "publisher"],
      country: ["country", "countries"],
      keyword: ["keyword", "keywords", "series"],
      language: ["language"],
      runtime: ["runtime", "duration", "length"],
      votes: ["votes"],
      popularity: ["popularity"],
    };
    const mapped: Record<string, string> = {};
    for (const [field, keys] of Object.entries(aliases)) {
      const key = keys.find((k) =>
        library.kind?.fields?.some((f) => f.key === k && f.indexed !== false),
      );
      if (key) mapped[field] = key;
    }
    return {
      allowedFields: RULE_FIELDS.filter((f) => f === "year" || f === "rating" || Boolean(mapped[f])),
      pluginFields: mapped,
    };
  }, [isPlugin, library.kind]);

  const setRules = (nextRules: Rule[], nextMatch: "all" | "any" = matchMode) => {
    setDraft((cur) => ({
      ...cur,
      filters: nextRules.length ? { match: nextMatch, rules: nextRules } : undefined,
    }));
  };

  const renderCondition = (
    rule: Rule,
    j: number,
    change: (r: Rule) => void,
    remove: () => void,
  ): React.ReactNode =>
    rule.field.startsWith("browse:") || rule.field === "group" || rule.field === "viewer" ? (
      <ExtendedCondition
        key={j}
        rule={rule}
        onChange={change}
        onRemove={remove}
        renderChild={renderCondition}
        kind={isPlugin ? library.kind : undefined}
      />
    ) : (
      <RuleRow
        key={j}
        rule={rule}
        index={j}
        kind="include"
        mediaType={mediaType}
        region={region}
        allowedFields={allowedFields}
        facetLibraryId={isPlugin ? library.id : undefined}
        facetFields={pluginFields}
        metaFor={metaForField}
        inline
        openValue={freshRuleIndex === j}
        onChange={change}
        onRemove={remove}
      />
    );

  // i18n: memo holds no copy
  const sortedFacetBuckets = useMemo(() => {
    const vals = [...(facetPreview.data?.values ?? [])];
    const order = draft.order ?? (draft.kind === "people" ? "name" : "count");
    if (order === "name") {
      vals.sort((a, b) =>
        String(a.title ?? a.value ?? "").localeCompare(
          String(b.title ?? b.value ?? ""),
          undefined,
          { sensitivity: "base" },
        ),
      );
    } else {
      vals.sort((a, b) => b.count - a.count);
    }
    return vals;
  }, [facetPreview.data, draft.order, draft.kind]);

  const currentSort = draft.sort || availableSorts[0] || "title";
  const dirOpts = directionOptions(currentSort);
  const currentDirection: "asc" | "desc" =
    draft.direction ?? (currentSort === "title" || currentSort === "index" ? "asc" : "desc");
  const searchEnabled = draft.search !== false;

  const canSave =
    Boolean(draft.label.trim()) &&
    (!supportsFacet || Boolean(draft.facet)) &&
    (draft.kind !== "view" || completeRules.length > 0);

  const commit = () => {
    if (!canSave) return;
    const key = isNew ? slugTabKey(draft.label.trim(), draft.kind, existingKeys) : draft.key;
    const cleaned: LibraryTab = {
      key,
      label: draft.label.trim(),
      kind: draft.kind,
    };
    if (supportsFacet && draft.facet) {
      cleaned.facet = draft.facet;
      cleaned.order = draft.order ?? (draft.kind === "people" ? "name" : "count");
    }
    if (supportsSort) {
      cleaned.sort = currentSort;
      cleaned.direction = currentDirection;
    }
    if (supportsSearch) {
      cleaned.search = searchEnabled;
      if (searchEnabled && draft.searchPlaceholder?.trim()) {
        cleaned.searchPlaceholder = draft.searchPlaceholder.trim();
      }
    }
    if (supportsRules && completeRules.length > 0) {
      cleaned.filters = {
        match: matchMode,
        rules: completeRules,
      };
    }
    if (draft.hidden) {
      cleaned.hidden = true;
    }
    onSave(cleaned);
  };

  const previewCount = isPlugin
    ? (pluginPreview.data?.total ?? library.itemCount ?? 0)
    : (rulesPreview.data?.visible ??
        Math.max(0, (rulesPreview.data?.total ?? 0) - (rulesPreview.data?.hidden ?? 0)));
  const previewTotal = isPlugin
    ? (pluginPreview.data?.total ?? library.itemCount ?? 0)
    : (rulesPreview.data?.total ?? 0);
  const previewItems = rulesPreview.data?.shown ?? [];

  const defaultSearchPlaceholder =
    draft.kind === "people"
      ? t("Search people…")
      : draft.kind === "collections"
        ? t("Search collections…")
        : t("Search {name}…", { name: draft.label.trim() || library.name });

  return (
    <Drawer
      open
      onClose={onClose}
      title={
        isNew
          ? t("New {kind} tab", { kind: tabKindTitle(draft.kind) })
          : t("Edit {label}", { label: initialTab.label })
      }
      description={tabKindTitle(draft.kind)}
      size="lg"
      footer={
        <div className="flex w-full flex-wrap items-center justify-between gap-2">
          <div>
            {!isNew && onDelete ? (
              <Button
                type="button"
                variant="destructive"
                size="sm"
                disabled={saving}
                onClick={onDelete}
              >
                {t("Delete tab")}
              </Button>
            ) : null}
          </div>
          <div className="flex items-center gap-2">
            <Button type="button" variant="outline" size="sm" onClick={onClose}>
              {t("Cancel")}
            </Button>
            <Button type="button" size="sm" disabled={!canSave || saving} onClick={commit}>
              {isNew ? t("Add tab") : t("Save tab")}
            </Button>
          </div>
        </div>
      }
    >
      <div className="space-y-5">
        {/* Label */}
        <div className="space-y-1.5">
          <label className="block text-sm font-medium text-fg" htmlFor="tab-label-input">
            {t("Label")}
          </label>
          <Input
            id="tab-label-input"
            aria-label={t("Tab label")}
            value={draft.label}
            placeholder={tabKindTitle(draft.kind)}
            onChange={(e) => {
              setLabelTouched(true);
              setDraft({ ...draft, label: e.target.value });
            }}
          />
          <p className="text-xs text-fg-muted">
            {t("Shown in the tab bar at the top of {name} in Vela.", { name: library.name })}
          </p>
        </div>

        {/* Recommended content info + Home Designer link */}
        {draft.kind === "recommended" ? (
          <section className="space-y-3 rounded-xl border border-border bg-surface p-4">
            <div className="flex items-start gap-3">
              <span className="mt-0.5 grid size-8 shrink-0 place-items-center rounded-lg bg-primary-subtle text-primary">
                <Sparkles size={16} />
              </span>
              <div className="min-w-0 flex-1 space-y-1">
                <p className="text-sm font-medium text-fg">{t("Recommended rows")}</p>
                <p className="text-xs text-fg-muted">
                  {t(
                    "Rows on this tab come from {name}'s Recommended layout in the Home Designer. Customising that layout updates Vela, Plex and Jellyfin/Emby.",
                    { name: library.name },
                  )}
                </p>
              </div>
            </div>
            <div>
              <Link
                to={`/home?section=library:${library.id}`}
                className={buttonVariants({ variant: "outline", size: "sm" })}
              >
                {t("Open in Home Designer")}
                <ExternalLink size={13} />
              </Link>
            </div>
          </section>
        ) : null}

        {/* Facet picker + bucket order + live bucket preview for collections & people */}
        {supportsFacet ? (
          <section className="space-y-4 rounded-xl border border-border bg-surface p-4">
            <div className="space-y-2">
              <p className="text-sm font-medium text-fg">
                {draft.kind === "people" ? t("Group people by") : t("Group collections by")}
              </p>
              <div
                className="grid grid-cols-2 gap-2 sm:grid-cols-3"
                role="radiogroup"
                aria-label={
                  draft.kind === "people" ? t("Group people by") : t("Group collections by")
                }
              >
                {matchingFacets.map((facet) => {
                  const active = draft.facet === facet.id;
                  return (
                    <button
                      key={facet.id}
                      type="button"
                      role="radio"
                      aria-checked={active}
                      onClick={() => {
                        const nextLabel = labelTouched
                          ? draft.label
                          : defaultLabelForFacet(facet, draft.kind);
                        setDraft({ ...draft, facet: facet.id, label: nextLabel });
                      }}
                      className={cn(
                        "rounded-lg border px-3 py-2 text-left text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
                        active
                          ? "border-primary bg-primary-subtle text-primary"
                          : "border-border bg-bg-subtle/50 text-fg hover:bg-surface-hover",
                      )}
                    >
                      {facet.label}
                    </button>
                  );
                })}
              </div>
            </div>

            <div className="flex flex-wrap items-center justify-between gap-2 border-t border-border-subtle pt-3">
              <div>
                <p className="text-sm font-medium text-fg">
                  {draft.kind === "people" ? t("People order") : t("Bucket order")}
                </p>
                <p className="text-xs text-fg-muted">
                  {draft.kind === "people"
                    ? t("How people are ordered in the directory.")
                    : t("How buckets are ordered on the tab.")}
                </p>
              </div>
              <Segmented
                ariaLabel={draft.kind === "people" ? t("People order") : t("Bucket order")}
                size="sm"
                value={draft.order ?? (draft.kind === "people" ? "name" : "count")}
                onChange={(order) => setDraft({ ...draft, order })}
                options={
                  draft.kind === "people"
                    ? [
                        { value: "name", label: t("Name A–Z") },
                        { value: "count", label: t("Most titles") },
                      ]
                    : [
                        { value: "count", label: t("Most items first") },
                        { value: "name", label: t("Name A–Z") },
                      ]
                }
              />
            </div>

            {/* Live facet preview */}
            <div
              className="rounded-lg border border-border-subtle bg-bg-subtle/60 p-3"
              role="status"
              aria-label={t("Live preview")}
            >
              <div className="flex items-center justify-between gap-2">
                <span className="flex items-center gap-1.5 text-xs font-medium text-fg">
                  <CheckCircle2 size={14} className="text-success" aria-hidden />
                  {facetPreview.loading && !facetPreview.data
                    ? t("Loading preview…")
                    : draft.kind === "people"
                      ? tn(
                          sortedFacetBuckets.length,
                          "{count} person in {name}",
                          "{count} people in {name}",
                          { name: library.name },
                        )
                      : tn(
                          sortedFacetBuckets.length,
                          "{count} collection in {name}",
                          "{count} collections in {name}",
                          { name: library.name },
                        )}
                </span>
              </div>
              {sortedFacetBuckets.length > 0 ? (
                <div className="mt-2 flex flex-wrap gap-1.5">
                  {sortedFacetBuckets.slice(0, 8).map((b) => {
                    const name = String(b.title ?? b.value ?? "");
                    return (
                      <Badge key={name} variant="secondary" className="font-normal">
                        {name}
                        <span className="ml-1 tabular-nums text-fg-muted">
                          · {formatNumber(b.count)}
                        </span>
                      </Badge>
                    );
                  })}
                </div>
              ) : null}
            </div>
          </section>
        ) : null}

        {/* Rules editor + live title preview for view & library */}
        {supportsRules ? (
          <section className="space-y-3 rounded-xl border border-border bg-surface p-4">
            <div className="flex flex-wrap items-start justify-between gap-2">
              <div>
                <p className="text-sm font-medium text-fg">
                  {draft.kind === "view" ? t("Matching titles") : t("Default filter")}
                </p>
                <p className="text-xs text-fg-muted">
                  {draft.kind === "view"
                    ? t("Choose which titles from {name} appear in this saved view.", {
                        name: library.name,
                      })
                    : t(
                        "Optional. Leave empty to show every item in {name}, or add rules to narrow this tab.",
                        { name: library.name },
                      )}
                </p>
              </div>
              {rules.length > 1 ? (
                <Segmented
                  ariaLabel={t("Rule matching")}
                  size="sm"
                  value={matchMode}
                  onChange={(m) => setRules(rules, m)}
                  options={[
                    { value: "all", label: t("Match all") },
                    { value: "any", label: t("Match any") },
                  ]}
                />
              ) : null}
            </div>

            {rules.length === 0 ? (
              <p className="rounded-lg border border-dashed border-border px-3 py-3 text-xs text-fg-muted">
                {draft.kind === "view"
                  ? t("Add at least one rule to define this saved view.")
                  : t("No filter rules — every item in {name} is shown.", { name: library.name })}
              </p>
            ) : (
              <div className="space-y-2">
                {rules.map((rule, i) =>
                  renderCondition(
                    rule,
                    i,
                    (next) => setRules(rules.map((r, idx) => (idx === i ? next : r))),
                    () => {
                      setFreshRuleIndex(null);
                      setRules(rules.filter((_, idx) => idx !== i));
                    },
                  ),
                )}
              </div>
            )}

            <div className="flex flex-wrap items-center gap-2">
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={() => {
                  const seed: Rule = isPlugin
                    ? newBrowseCondition("year", library.kind)
                    : { field: "genre", op: "in", value: [] };
                  setFreshRuleIndex(rules.length);
                  setRules([...rules, seed]);
                }}
              >
                <Plus size={14} />
                {t("Add rule")}
              </Button>
              <div className="w-48">
                <Select
                  aria-label={t("More conditions")}
                  value=""
                  onChange={(e) => {
                    if (!e.target.value) return;
                    setFreshRuleIndex(null);
                    setRules([
                      ...rules,
                      newBrowseCondition(e.target.value, isPlugin ? library.kind : undefined),
                    ]);
                  }}
                >
                  <option value="">{t("More conditions…")}</option>
                  {browseFields(isPlugin ? library.kind : undefined).map(([key, label]) => (
                    <option key={key} value={key}>
                      {label}
                    </option>
                  ))}
                </Select>
              </div>
            </div>

            {/* Live matching count & sample posters */}
            <div
              className="space-y-2 rounded-lg border border-border-subtle bg-bg-subtle/60 p-3"
              role="status"
              aria-label={t("Matching titles preview")}
            >
              <div className="flex flex-wrap items-center justify-between gap-2">
                <span className="flex items-center gap-1.5 text-xs font-medium text-fg">
                  <CheckCircle2 size={14} className="text-success" aria-hidden />
                  {rulesPreview.loading && !rulesPreview.data && !isPlugin
                    ? t("Counting matching titles…")
                    : completeRules.length > 0 && !isPlugin
                      ? t("{matched} of {total} titles match", {
                          matched: formatNumber(previewCount),
                          total: formatNumber(previewTotal),
                        })
                      : tn(
                          previewCount,
                          "{count} item in this library",
                          "{count} items in this library",
                        )}
                </span>
                {!allRulesComplete ? (
                  <span className="text-[11px] text-fg-muted">
                    {t("Finish incomplete rules to include them in the count.")}
                  </span>
                ) : null}
              </div>
              {!isPlugin && previewItems.length > 0 ? (
                <div className="flex gap-2 overflow-x-auto pb-1">
                  {previewItems.slice(0, 10).map((item) => (
                    <Artwork
                      key={item.key}
                      src={item.poster ?? titlePosterURL(item, 185)}
                      alt={item.title}
                      className="aspect-[2/3] w-12 shrink-0 rounded-md"
                    />
                  ))}
                </div>
              ) : null}
            </div>
          </section>
        ) : null}

        {/* Sort & Direction */}
        {supportsSort ? (
          <section className="space-y-3 rounded-xl border border-border bg-surface p-4">
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="space-y-1.5">
                <label className="block text-sm font-medium text-fg" htmlFor="tab-sort-select">
                  {draft.kind === "collections"
                    ? t("Sort items inside each bucket")
                    : t("Default sort")}
                </label>
                <Select
                  id="tab-sort-select"
                  aria-label={
                    draft.kind === "collections"
                      ? t("Sort items inside each bucket")
                      : t("Default sort")
                  }
                  value={currentSort}
                  onChange={(e) => {
                    const nextSort = e.target.value;
                    const nextDir: "asc" | "desc" =
                      nextSort === "title" || nextSort === "index" ? "asc" : "desc";
                    setDraft({ ...draft, sort: nextSort, direction: nextDir });
                  }}
                >
                  {availableSorts.map((s) => (
                    <option key={s} value={s}>
                      {sortFieldLabel(s)}
                    </option>
                  ))}
                </Select>
              </div>
              <div className="space-y-1.5">
                <span className="block text-sm font-medium text-fg">{t("Direction")}</span>
                <Segmented
                  ariaLabel={t("Sort direction")}
                  size="sm"
                  value={currentDirection}
                  onChange={(direction) => setDraft({ ...draft, direction })}
                  options={dirOpts}
                />
              </div>
            </div>
          </section>
        ) : null}

        {/* Search & Visibility */}
        <section className="divide-y divide-border-subtle rounded-xl border border-border bg-surface">
          {supportsSearch ? (
            <div className="space-y-3 p-4">
              <div className="flex items-center justify-between gap-3">
                <div>
                  <p className="text-sm font-medium text-fg">{t("Search box")}</p>
                  <p className="text-xs text-fg-muted">
                    {t("Let viewers filter this tab by typing.")}
                  </p>
                </div>
                <Switch
                  aria-label={t("Search box")}
                  checked={searchEnabled}
                  onChange={(e) => setDraft({ ...draft, search: e.target.checked })}
                />
              </div>
              {searchEnabled ? (
                <div className="space-y-1">
                  <label
                    className="block text-xs font-medium text-fg-muted"
                    htmlFor="tab-search-placeholder"
                  >
                    {t("Search placeholder")}
                  </label>
                  <Input
                    id="tab-search-placeholder"
                    aria-label={t("Search placeholder")}
                    placeholder={defaultSearchPlaceholder}
                    value={draft.searchPlaceholder ?? ""}
                    onChange={(e) => setDraft({ ...draft, searchPlaceholder: e.target.value })}
                  />
                </div>
              ) : null}
            </div>
          ) : null}

          <div className="flex items-center justify-between gap-3 p-4">
            <div>
              <p className="text-sm font-medium text-fg">{t("Visible in Vela")}</p>
              <p className="text-xs text-fg-muted">
                {t("Turn off to keep this tab configured here without showing it to viewers.")}
              </p>
            </div>
            <Switch
              aria-label={t("Visible in Vela")}
              checked={!draft.hidden}
              onChange={(e) => setDraft({ ...draft, hidden: !e.target.checked })}
            />
          </div>
        </section>
      </div>
    </Drawer>
  );
}
