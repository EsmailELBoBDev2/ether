import {
  Badge,
  Button,
  Checkbox,
  Input,
  Switch,
  cn,
  confirmDialog,
  toast,
} from "@braedonsaunders/appkit-ui";
import { Check, GripVertical, Pencil, Plus, Search, Trash2, Tv, X } from "lucide-react";
import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Segmented } from "../../components/fields";
import { ToolbarPicker } from "../../components/ToolbarPicker";
import { localized, t, tn } from "../../i18n";
import { uuid } from "../../lib/api";
import { formatNumber } from "../../lib/format";
import { LIVE_APPS, type LiveApp, liveAppChipLabel, liveAppNote } from "./ChannelApps";
import { ChannelBulkBar, type ChannelPatch, appStates } from "./ChannelBulkBar";
import { ChannelLogo } from "./ChannelLogo";
import {
  channelVisibleInApp,
  compressOwnerGroupMembers,
  isChannelSource,
  type ChannelAppChange,
  type ChannelSummary,
  type Coverage,
  type OwnerGroup,
  type Source,
} from "./model";

export type ChannelFilter = "all" | "live" | "favorites" | "virtual" | "iptv" | "missing" | "off";

export type ChannelSort = "number" | "name" | "id" | "source";

export type ChannelStatusFilter = "all" | "enabled" | "disabled" | "shown" | "hidden";

export const CHANNEL_FILTERS = localized<{ value: ChannelFilter; label: string }[]>(() => [
  { value: "all", label: t("Everything") },
  { value: "favorites", label: t("Favourites") },
  { value: "virtual", label: t("Virtual") },
  { value: "iptv", label: "IPTV" },
  { value: "missing", label: t("Needs a guide") },
]);

type TreeScope =
  | { kind: "every" }
  | { kind: "ownerGroup"; id: string }
  | { kind: "virtual" }
  | { kind: "source"; sourceId: string }
  | { kind: "providerGroup"; sourceId: string; group: string };

type ScopeCount = { total: number; shown: number };

const ROW_HEIGHT = 56;
const DEFAULT_VIEWPORT_HEIGHT = 900;
const collator = new Intl.Collator();

function sameScope(a: TreeScope, b: TreeScope): boolean {
  if (a.kind !== b.kind) return false;
  switch (a.kind) {
    case "every":
    case "virtual":
      return true;
    case "ownerGroup":
      return b.kind === "ownerGroup" && a.id === b.id;
    case "source":
      return b.kind === "source" && a.sourceId === b.sourceId;
    case "providerGroup":
      return b.kind === "providerGroup" && a.sourceId === b.sourceId && a.group === b.group;
  }
}

export function ChannelsWorkspace({
  channels,
  sources,
  coverage,
  ownerGroups,
  rawQuery,
  onQueryChange,
  query,
  filter,
  onFilterChange,
  sort,
  onSortChange,
  missingCount,
  selectedChannelId,
  busy,
  onOpenChannel,
  onSaveOwnerGroups,
  onSetChannelsApp,
  onUpdateChannels,
}: {
  channels: ChannelSummary[] | null;
  sources: Source[];
  coverage: ReadonlyMap<string, Coverage> | null;
  ownerGroups: OwnerGroup[];
  rawQuery: string;
  onQueryChange: (next: string) => void;
  query: string;
  filter: ChannelFilter;
  onFilterChange: (next: ChannelFilter) => void;
  sort: ChannelSort;
  onSortChange: (next: ChannelSort) => void;
  missingCount: number | null;
  selectedChannelId: string | null;
  busy: boolean;
  onOpenChannel: (id: string) => void;
  onSaveOwnerGroups: (groups: OwnerGroup[]) => Promise<boolean>;
  onSetChannelsApp: (change: ChannelAppChange) => Promise<boolean>;
  onUpdateChannels: (ids: string[], patch: ChannelPatch) => Promise<boolean>;
}) {
  const [selectedApp, setSelectedApp] = useState<"all" | LiveApp>("all");
  const [statusFilter, setStatusFilter] = useState<ChannelStatusFilter>("all");
  const [scope, setScope] = useState<TreeScope>({ kind: "every" });
  const [checkedIds, setCheckedIds] = useState<Set<string>>(new Set());
  const selectionAnchor = useRef<string | null>(null);

  const [creatingGroup, setCreatingGroup] = useState(false);
  const [newGroupName, setNewGroupName] = useState("");
  const [renamingId, setRenamingId] = useState<string | null>(null);
  const [renameDraft, setRenameDraft] = useState("");
  const [groupBusy, setGroupBusy] = useState(false);
  const dragIndex = useRef<number | null>(null);

  const listRef = useRef<HTMLDivElement>(null);
  const [scrollTop, setScrollTop] = useState(0);
  const [viewportHeight, setViewportHeight] = useState(DEFAULT_VIEWPORT_HEIGHT);

  const all = channels ?? [];

  useEffect(() => {
    if (scope.kind === "ownerGroup" && !ownerGroups.some((g) => g.id === scope.id)) {
      setScope({ kind: "every" });
    }
  }, [scope, ownerGroups]);

  useEffect(() => {
    if (selectedApp === "all" && (statusFilter === "shown" || statusFilter === "hidden")) {
      setStatusFilter("all");
    }
  }, [selectedApp, statusFilter]);

  const sourceNameById = useMemo(() => {
    const m = new Map<string, string>();
    for (const s of sources) m.set(s.id, s.name);
    return m;
  }, [sources]);

  // Single O(N) pass over allChannels to compute per-app totals, channel-to-owner-group
  // memberships, and counts for every row in the left group tree.
  const index = useMemo(() => {
    const allFlagGroups = ownerGroups.filter((g) => g.all);
    const byProvKey = new Map<string, OwnerGroup[]>();
    const byChanId = new Map<string, OwnerGroup[]>();

    for (const g of ownerGroups) {
      if (g.all) continue;
      for (const pg of g.groups ?? []) {
        const k = `${pg.sourceId}\u0000${pg.name}`;
        const list = byProvKey.get(k);
        if (list) list.push(g);
        else byProvKey.set(k, [g]);
      }
      for (const cid of g.channels ?? []) {
        const list = byChanId.get(cid);
        if (list) list.push(g);
        else byChanId.set(cid, [g]);
      }
    }

    const appTotals: Record<LiveApp, number> = {
      plex: 0,
      jellyfin: 0,
      emby: 0,
      silo: 0,
      xtream: 0,
      m3u: 0,
      vela: 0,
    };
    const ownerGroupCounts = new Map<string, ScopeCount>();
    for (const g of ownerGroups) ownerGroupCounts.set(g.id, { total: 0, shown: 0 });

    const virtualCounts: ScopeCount = { total: 0, shown: 0 };
    const sourceMap = new Map<string, { counts: ScopeCount; groups: Map<string, ScopeCount> }>();
    for (const s of sources) {
      if (isChannelSource(s)) {
        sourceMap.set(s.id, { counts: { total: 0, shown: 0 }, groups: new Map() });
      }
    }

    const memberGroupsByChannel = new Map<string, OwnerGroup[]>();

    for (const c of all) {
      const provMatches =
        c.kind !== "virtual"
          ? byProvKey.get(`${c.sourceId ?? ""}\u0000${c.group ?? ""}`)
          : undefined;
      const chanMatches = byChanId.get(c.id);

      let cGroups: OwnerGroup[] | undefined;
      if (allFlagGroups.length || provMatches?.length || chanMatches?.length) {
        if (
          (allFlagGroups.length ? 1 : 0) +
            (provMatches?.length ? 1 : 0) +
            (chanMatches?.length ? 1 : 0) ===
          1
        ) {
          cGroups = allFlagGroups.length
            ? allFlagGroups
            : provMatches?.length
              ? provMatches
              : chanMatches;
        } else {
          const hit = new Set<string>();
          for (const g of allFlagGroups) hit.add(g.id);
          if (provMatches) for (const g of provMatches) hit.add(g.id);
          if (chanMatches) for (const g of chanMatches) hit.add(g.id);
          cGroups = ownerGroups.filter((g) => hit.has(g.id));
        }
        if (cGroups?.length) memberGroupsByChannel.set(c.id, cGroups);
      }

      for (const app of LIVE_APPS) {
        if (channelVisibleInApp(c, app, cGroups)) appTotals[app]++;
      }

      const shownInSelected =
        selectedApp === "all" ? true : channelVisibleInApp(c, selectedApp, cGroups);

      if (cGroups) {
        for (const g of cGroups) {
          const stat = ownerGroupCounts.get(g.id);
          if (stat) {
            stat.total++;
            if (shownInSelected) stat.shown++;
          }
        }
      }

      if (c.kind === "virtual") {
        virtualCounts.total++;
        if (shownInSelected) virtualCounts.shown++;
      } else {
        const sid = c.sourceId ?? "";
        let srcEntry = sourceMap.get(sid);
        if (!srcEntry) {
          srcEntry = { counts: { total: 0, shown: 0 }, groups: new Map() };
          sourceMap.set(sid, srcEntry);
        }
        srcEntry.counts.total++;
        if (shownInSelected) srcEntry.counts.shown++;

        const gname = c.group ?? "";
        let grpStat = srcEntry.groups.get(gname);
        if (!grpStat) {
          grpStat = { total: 0, shown: 0 };
          srcEntry.groups.set(gname, grpStat);
        }
        grpStat.total++;
        if (shownInSelected) grpStat.shown++;
      }
    }

    return {
      appTotals,
      ownerGroupCounts,
      virtualCounts,
      sourceMap,
      memberGroupsByChannel,
    };
  }, [all, ownerGroups, sources, selectedApp]);

  const hasCoverage = useCallback(
    (c: ChannelSummary): boolean => {
      if (c.kind === "virtual") return true;
      if (coverage && (coverage.size > 0 || coverage.has(c.id))) {
        return (coverage.get(c.id)?.programmes ?? 0) > 0;
      }
      return Boolean(c.guideId);
    },
    [coverage],
  );

  // Channels matching the left-tree scope, search query, and Show picker (before status filter).
  const scoped = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return all.filter((c) => {
      switch (scope.kind) {
        case "every":
          break;
        case "virtual":
          if (c.kind !== "virtual") return false;
          break;
        case "source":
          if (c.kind === "virtual" || (c.sourceId ?? "") !== scope.sourceId) return false;
          break;
        case "providerGroup":
          if (
            c.kind === "virtual" ||
            (c.sourceId ?? "") !== scope.sourceId ||
            (c.group ?? "") !== scope.group
          ) {
            return false;
          }
          break;
        case "ownerGroup": {
          const gs = index.memberGroupsByChannel.get(c.id);
          if (!gs?.some((g) => g.id === scope.id)) return false;
          break;
        }
      }

      if (filter === "live" && !c.enabled) return false;
      if (filter === "favorites" && !c.favorite) return false;
      if (filter === "virtual" && c.kind !== "virtual") return false;
      if (filter === "iptv" && c.kind !== "iptv") return false;
      if (filter === "off" && c.enabled) return false;
      if (filter === "missing" && (!c.enabled || c.kind !== "iptv" || hasCoverage(c))) {
        return false;
      }

      if (!needle) return true;
      if (`${c.number} ${c.name} ${c.group ?? ""}`.toLowerCase().includes(needle)) return true;
      const gs = index.memberGroupsByChannel.get(c.id);
      return gs ? gs.some((g) => g.name.toLowerCase().includes(needle)) : false;
    });
  }, [all, scope, filter, query, index.memberGroupsByChannel, hasCoverage]);

  // Live status & app counts within the active scope + search.
  const statusCounts = useMemo(() => {
    let enabled = 0;
    let shown = 0;
    for (const c of scoped) {
      if (c.enabled) enabled++;
      if (
        selectedApp !== "all" &&
        channelVisibleInApp(c, selectedApp, index.memberGroupsByChannel.get(c.id))
      ) {
        shown++;
      }
    }
    return {
      all: scoped.length,
      enabled,
      disabled: scoped.length - enabled,
      shown,
      hidden: scoped.length - shown,
    };
  }, [scoped, selectedApp, index.memberGroupsByChannel]);

  const rows = useMemo(() => {
    const matched =
      statusFilter === "all"
        ? scoped
        : scoped.filter((c) => {
            if (statusFilter === "enabled") return c.enabled;
            if (statusFilter === "disabled") return !c.enabled;
            if (selectedApp === "all") return true;
            const vis = channelVisibleInApp(c, selectedApp, index.memberGroupsByChannel.get(c.id));
            return statusFilter === "shown" ? vis : !vis;
          });

    if (sort === "name") {
      return [...matched].sort(
        (a, b) => collator.compare(a.name, b.name) || collator.compare(a.number, b.number),
      );
    }
    if (sort === "id") {
      return [...matched].sort((a, b) => collator.compare(a.id, b.id));
    }
    if (sort === "source") {
      return [...matched].sort(
        (a, b) =>
          collator.compare(a.sourceId ?? "", b.sourceId ?? "") ||
          collator.compare(a.group ?? "", b.group ?? "") ||
          (parseFloat(a.number) || 0) - (parseFloat(b.number) || 0) ||
          collator.compare(a.name, b.name),
      );
    }
    const keyed = matched.map((c) => ({ c, n: parseFloat(c.number) || 0 }));
    keyed.sort(
      (a, b) =>
        a.n - b.n ||
        collator.compare(a.c.number, b.c.number) ||
        collator.compare(a.c.name, b.c.name),
    );
    return keyed.map((k) => k.c);
  }, [scoped, statusFilter, selectedApp, sort, index.memberGroupsByChannel]);

  const selectable = useMemo(() => new Set(rows.map((c) => c.id)), [rows]);

  useEffect(() => {
    setCheckedIds((old) => {
      const next = new Set([...old].filter((id) => selectable.has(id)));
      return next.size === old.size ? old : next;
    });
  }, [selectable]);

  const checked = useMemo(
    () => new Set([...checkedIds].filter((id) => selectable.has(id))),
    [checkedIds, selectable],
  );

  useEffect(() => {
    if (listRef.current) listRef.current.scrollTop = 0;
    setScrollTop(0);
  }, [scope, filter, statusFilter, query, sort]);

  useEffect(() => {
    const el = listRef.current;
    if (!el) return;
    const measure = () => {
      if (el.clientHeight > 0) setViewportHeight(el.clientHeight);
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const providerGroups = useMemo(
    () => [...new Set(all.map((c) => c.group).filter((g): g is string => Boolean(g)))].sort(),
    [all],
  );

  const selectedAppsState = useMemo(
    () => appStates(rows.filter((c) => checked.has(c.id)).map((c) => c.hiddenApps)),
    [rows, checked],
  );

  const clearSelection = useCallback(() => {
    if (busy) return;
    setCheckedIds(new Set());
    selectionAnchor.current = null;
  }, [busy]);

  const selectAllMatches = useCallback(() => {
    if (busy) return;
    setCheckedIds(new Set(rows.map((c) => c.id)));
  }, [busy, rows]);

  const toggleRowChecked = useCallback(
    (id: string, range = false) => {
      if (busy) return;
      const anchor = selectionAnchor.current;
      selectionAnchor.current = id;
      setCheckedIds((old) => {
        const next = new Set(old);
        const include = !old.has(id);
        const first = range && anchor ? rows.findIndex((c) => c.id === anchor) : -1;
        const last = rows.findIndex((c) => c.id === id);
        const ids =
          first >= 0 && last >= 0
            ? rows.slice(Math.min(first, last), Math.max(first, last) + 1).map((c) => c.id)
            : [id];
        for (const key of ids) {
          if (include) next.add(key);
          else next.delete(key);
        }
        return next;
      });
    },
    [busy, rows],
  );

  const toggleSingleChannelApp = useCallback(
    (channelId: string, show: boolean) => {
      if (selectedApp === "all" || busy) return;
      void onSetChannelsApp({ app: selectedApp, show, channels: [channelId] });
    },
    [selectedApp, busy, onSetChannelsApp],
  );

  const handleBulkApply = useCallback(
    async (patch: ChannelPatch) => {
      if (!checked.size) return false;
      const ok = await onUpdateChannels([...checked], patch);
      if (ok) {
        setCheckedIds(new Set());
        selectionAnchor.current = null;
      }
      return ok;
    },
    [checked, onUpdateChannels],
  );

  const handleBulkSetApp = useCallback(
    async (app: LiveApp, show: boolean) => {
      if (!checked.size) return false;
      const ok = await onSetChannelsApp({ app, show, channels: [...checked] });
      if (ok) {
        setCheckedIds(new Set());
        selectionAnchor.current = null;
      }
      return ok;
    },
    [checked, onSetChannelsApp],
  );

  const handleAddToOwnerGroup = useCallback(
    async (target: { id?: string; name: string }) => {
      if (!checked.size) return false;
      const trimmed = target.name.trim();
      if (!trimmed) return false;
      const existing = target.id
        ? ownerGroups.find((g) => g.id === target.id)
        : ownerGroups.find((g) => g.name.toLowerCase() === trimmed.toLowerCase());

      let nextGroups: OwnerGroup[];
      if (existing) {
        const currentIds = new Set<string>(existing.channels ?? []);
        if (existing.all) {
          for (const c of all) currentIds.add(c.id);
        } else {
          const pkeys = new Set((existing.groups ?? []).map((g) => `${g.sourceId}\u0000${g.name}`));
          for (const c of all) {
            if (c.kind !== "virtual" && pkeys.has(`${c.sourceId ?? ""}\u0000${c.group ?? ""}`)) {
              currentIds.add(c.id);
            }
          }
        }
        for (const id of checked) currentIds.add(id);
        const updated = compressOwnerGroupMembers(existing, currentIds, all);
        nextGroups = ownerGroups.map((g) => (g.id === existing.id ? updated : g));
      } else {
        const base: OwnerGroup = { id: uuid(), name: trimmed, groups: [], channels: [] };
        const created = compressOwnerGroupMembers(base, checked, all);
        nextGroups = [...ownerGroups, created];
      }

      const ok = await onSaveOwnerGroups(nextGroups);
      if (ok) {
        setCheckedIds(new Set());
        selectionAnchor.current = null;
      }
      return ok;
    },
    [checked, ownerGroups, all, onSaveOwnerGroups],
  );

  const submitCreateGroup = async () => {
    const name = newGroupName.trim();
    if (!name || groupBusy) return;
    if (ownerGroups.some((g) => g.name.toLowerCase() === name.toLowerCase())) {
      toast.error(t("A group with that name already exists."));
      return;
    }
    setGroupBusy(true);
    try {
      const created: OwnerGroup = { id: uuid(), name, groups: [], channels: [] };
      const ok = await onSaveOwnerGroups([...ownerGroups, created]);
      if (ok) {
        setCreatingGroup(false);
        setNewGroupName("");
        setScope({ kind: "ownerGroup", id: created.id });
      }
    } finally {
      setGroupBusy(false);
    }
  };

  const submitRenameGroup = async (group: OwnerGroup) => {
    const name = renameDraft.trim();
    if (!name || name === group.name) {
      setRenamingId(null);
      return;
    }
    if (ownerGroups.some((g) => g.id !== group.id && g.name.toLowerCase() === name.toLowerCase())) {
      toast.error(t("A group with that name already exists."));
      return;
    }
    setGroupBusy(true);
    try {
      const ok = await onSaveOwnerGroups(
        ownerGroups.map((g) => (g.id === group.id ? { ...g, name } : g)),
      );
      if (ok) setRenamingId(null);
    } finally {
      setGroupBusy(false);
    }
  };

  const handleDeleteGroup = async (group: OwnerGroup) => {
    const ok = await confirmDialog({
      title: t("Delete {name}?", { name: group.name }),
      message: t("Channels stay in your lineup; only this group is removed."),
      confirmLabel: t("Delete"),
      tone: "danger",
    });
    if (!ok) return;
    setGroupBusy(true);
    try {
      await onSaveOwnerGroups(ownerGroups.filter((g) => g.id !== group.id));
    } finally {
      setGroupBusy(false);
    }
  };

  const moveOwnerGroup = (from: number, to: number) => {
    if (groupBusy || from === to || from < 0 || to < 0 || to >= ownerGroups.length) return;
    const next = [...ownerGroups];
    const [picked] = next.splice(from, 1);
    if (!picked) return;
    next.splice(to, 0, picked);
    void onSaveOwnerGroups(next);
  };

  const scopeTitle = useMemo(() => {
    switch (scope.kind) {
      case "every":
        return t("Every channel");
      case "virtual":
        return t("Virtual channels");
      case "ownerGroup": {
        const g = ownerGroups.find((x) => x.id === scope.id);
        return g ? `${g.name} · ${t("your group")}` : t("Every channel");
      }
      case "source":
        return sourceNameById.get(scope.sourceId) || scope.sourceId || "IPTV";
      case "providerGroup": {
        const sname = sourceNameById.get(scope.sourceId) || scope.sourceId || "IPTV";
        return `${sname} › ${scope.group || t("Ungrouped")}`;
      }
    }
  }, [scope, ownerGroups, sourceNameById]);

  const totalChannels = all.length;
  const startRow = Math.max(0, Math.floor(scrollTop / ROW_HEIGHT) - 8);
  const endRow = Math.min(rows.length, Math.ceil((scrollTop + viewportHeight) / ROW_HEIGHT) + 12);
  const visibleRows = rows.slice(startRow, endRow);

  const renderCount = (stat: ScopeCount) =>
    selectedApp === "all"
      ? formatNumber(stat.total)
      : `${formatNumber(stat.shown)} / ${formatNumber(stat.total)}`;

  const renderScopeCheckbox = (
    label: string,
    stat: ScopeCount,
    onToggle: (show: boolean) => void,
  ) => {
    if (selectedApp === "all") return null;
    const allOn = stat.total > 0 && stat.shown === stat.total;
    const mixed = stat.shown > 0 && stat.shown < stat.total;
    return (
      <span
        className="flex shrink-0 items-center"
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => e.stopPropagation()}
      >
        <Checkbox
          checked={allOn}
          aria-checked={mixed ? "mixed" : allOn}
          aria-label={t("Show {group} in {app}", {
            group: label,
            app: liveAppChipLabel(selectedApp),
          })}
          disabled={busy || stat.total === 0}
          ref={(el) => {
            if (el) el.indeterminate = mixed;
          }}
          onChange={() => onToggle(!allOn)}
        />
      </span>
    );
  };

  const statusOptions = useMemo(() => {
    const countBadge = (n: number) => (
      <span className="tabular-nums text-[11px] opacity-70">{formatNumber(n)}</span>
    );
    const base: { value: ChannelStatusFilter; label: React.ReactNode }[] = [
      {
        value: "all",
        label: (
          <>
            <span>{t("All")}</span>
            {countBadge(statusCounts.all)}
          </>
        ),
      },
      {
        value: "enabled",
        label: (
          <>
            <span>{t("Enabled")}</span>
            {countBadge(statusCounts.enabled)}
          </>
        ),
      },
      {
        value: "disabled",
        label: (
          <>
            <span>{t("Disabled")}</span>
            {countBadge(statusCounts.disabled)}
          </>
        ),
      },
    ];
    if (selectedApp !== "all") {
      const appName = liveAppChipLabel(selectedApp);
      base.push(
        {
          value: "shown",
          label: (
            <>
              <span>{t("Shown in {app}", { app: appName })}</span>
              {countBadge(statusCounts.shown)}
            </>
          ),
        },
        {
          value: "hidden",
          label: (
            <>
              <span>{t("Hidden from {app}", { app: appName })}</span>
              {countBadge(statusCounts.hidden)}
            </>
          ),
        },
      );
    }
    return base;
  }, [selectedApp, statusCounts]);

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden bg-bg">
      {/* Showing-for app chips + contextual note */}
      <div className="shrink-0 space-y-2 border-b border-border bg-surface/60 px-3 py-3 sm:px-5">
        <div
          role="toolbar"
          aria-label={t("Showing for")}
          className="flex flex-wrap items-center gap-1.5"
        >
          <span className="mr-1 text-xs font-medium text-fg-muted">{t("Showing for")}</span>
          <button
            type="button"
            aria-pressed={selectedApp === "all"}
            onClick={() => {
              setSelectedApp("all");
              if (statusFilter === "shown" || statusFilter === "hidden") {
                setStatusFilter("all");
              }
              clearSelection();
            }}
            className={cn(
              "inline-flex items-center gap-1.5 rounded-full border px-3 py-1 text-xs font-medium transition-colors",
              selectedApp === "all"
                ? "border-primary/50 bg-primary-subtle text-primary"
                : "border-border bg-surface text-fg-muted hover:border-border-strong hover:text-fg",
            )}
          >
            <span>{t("All apps")}</span>
            <span className="tabular-nums text-[11px] opacity-80">
              {formatNumber(totalChannels)}
            </span>
          </button>
          {LIVE_APPS.map((app) => {
            const active = selectedApp === app;
            const shown = index.appTotals[app];
            return (
              <button
                key={app}
                type="button"
                aria-pressed={active}
                onClick={() => {
                  setSelectedApp(app);
                  clearSelection();
                }}
                className={cn(
                  "inline-flex items-center gap-1.5 rounded-full border px-3 py-1 text-xs font-medium transition-colors",
                  active
                    ? "border-primary/50 bg-primary-subtle text-primary"
                    : "border-border bg-surface text-fg-muted hover:border-border-strong hover:text-fg",
                )}
              >
                <span>{liveAppChipLabel(app)}</span>
                <span className="tabular-nums text-[11px] opacity-80">
                  {formatNumber(shown)} / {formatNumber(totalChannels)}
                </span>
              </button>
            );
          })}
        </div>
        <p className="text-xs text-fg-muted">
          {selectedApp === "all" ? (
            <>
              <strong className="font-semibold text-fg">{t("All apps")}</strong>:{" "}
              {t(
                "every channel, tagged with the apps it appears in. Pick an app to see and edit exactly what it shows.",
              )}
            </>
          ) : (
            <>
              <strong className="font-semibold text-fg">{liveAppChipLabel(selectedApp)}</strong>{" "}
              {t("shows {shown} of {total} channels.", {
                shown: formatNumber(index.appTotals[selectedApp]),
                total: formatNumber(totalChannels),
              })}{" "}
              {liveAppNote(selectedApp)}{" "}
              {t("Switches and group checkboxes here change only this app.")}
            </>
          )}
        </p>
      </div>

      {/* Main workspace split: Left group tree + Right virtualised channel list */}
      <div className="app-scroll flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-3 sm:p-5 lg:grid lg:grid-cols-[272px_minmax(0,1fr)] lg:overflow-hidden">
        {/* Left group tree */}
        <nav
          aria-label={t("Channel groups")}
          className="app-scroll flex shrink-0 flex-col rounded-xl border border-border bg-surface p-2.5 text-sm lg:min-h-0 lg:overflow-y-auto"
        >
          {/* Every channel */}
          <button
            type="button"
            onClick={() => {
              setScope({ kind: "every" });
              clearSelection();
            }}
            className={cn(
              "flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-left transition-colors",
              scope.kind === "every"
                ? "bg-primary-subtle font-medium text-primary"
                : "text-fg hover:bg-surface-hover",
            )}
          >
            <span className="truncate">{t("Every channel")}</span>
            <span className="ml-auto shrink-0 text-xs tabular-nums text-fg-muted">
              {selectedApp === "all"
                ? formatNumber(totalChannels)
                : `${formatNumber(index.appTotals[selectedApp])} / ${formatNumber(totalChannels)}`}
            </span>
          </button>

          {/* Your groups */}
          <div className="mt-3 mb-1 flex items-center justify-between px-2">
            <span className="text-[11px] font-semibold uppercase tracking-wider text-fg-subtle">
              {t("Your groups")}
            </span>
            <button
              type="button"
              disabled={groupBusy}
              onClick={() => {
                setCreatingGroup(true);
                setNewGroupName("");
                setRenamingId(null);
              }}
              className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-xs font-medium text-primary transition-colors hover:bg-primary-subtle disabled:opacity-50"
            >
              <Plus size={12} />
              {t("New")}
            </button>
          </div>

          {creatingGroup ? (
            <form
              className="mb-1.5 flex items-center gap-1 px-1"
              onSubmit={(e) => {
                e.preventDefault();
                void submitCreateGroup();
              }}
            >
              <Input
                autoFocus
                value={newGroupName}
                maxLength={150}
                disabled={groupBusy}
                aria-label={t("Group name")}
                placeholder={t("Group name")}
                onChange={(e) => setNewGroupName(e.currentTarget.value)}
                onKeyDown={(e) => {
                  if (e.key === "Escape") {
                    e.preventDefault();
                    setCreatingGroup(false);
                    setNewGroupName("");
                  }
                }}
                className="h-7 text-xs"
              />
              <Button
                type="submit"
                size="icon"
                variant="ghost"
                disabled={groupBusy || !newGroupName.trim()}
                aria-label={t("Save")}
                className="size-7 shrink-0"
              >
                <Check size={13} />
              </Button>
              <Button
                type="button"
                size="icon"
                variant="ghost"
                disabled={groupBusy}
                aria-label={t("Cancel")}
                onClick={() => {
                  setCreatingGroup(false);
                  setNewGroupName("");
                }}
                className="size-7 shrink-0"
              >
                <X size={13} />
              </Button>
            </form>
          ) : null}

          {ownerGroups.length === 0 && !creatingGroup ? (
            <p className="px-2 py-1 text-xs text-fg-subtle">
              {t("Create groups for Sports, News or Kids across sources.")}
            </p>
          ) : null}

          <div className="space-y-0.5">
            {ownerGroups.map((g, idx) => {
              const stat = index.ownerGroupCounts.get(g.id) ?? { total: 0, shown: 0 };
              const active = sameScope(scope, { kind: "ownerGroup", id: g.id });
              if (renamingId === g.id) {
                return (
                  <form
                    key={g.id}
                    className="flex items-center gap-1 px-1 py-0.5"
                    onSubmit={(e) => {
                      e.preventDefault();
                      void submitRenameGroup(g);
                    }}
                  >
                    <Input
                      autoFocus
                      value={renameDraft}
                      maxLength={150}
                      disabled={groupBusy}
                      aria-label={t("Rename {name}", { name: g.name })}
                      onChange={(e) => setRenameDraft(e.currentTarget.value)}
                      onKeyDown={(e) => {
                        if (e.key === "Escape") {
                          e.preventDefault();
                          setRenamingId(null);
                        }
                      }}
                      className="h-7 text-xs"
                    />
                    <Button
                      type="submit"
                      size="icon"
                      variant="ghost"
                      disabled={groupBusy || !renameDraft.trim()}
                      aria-label={t("Save")}
                      className="size-7 shrink-0"
                    >
                      <Check size={13} />
                    </Button>
                    <Button
                      type="button"
                      size="icon"
                      variant="ghost"
                      disabled={groupBusy}
                      aria-label={t("Cancel")}
                      onClick={() => setRenamingId(null)}
                      className="size-7 shrink-0"
                    >
                      <X size={13} />
                    </Button>
                  </form>
                );
              }

              return (
                <div
                  key={g.id}
                  draggable={!groupBusy}
                  onDragStart={() => {
                    dragIndex.current = idx;
                  }}
                  onDragOver={(e) => e.preventDefault()}
                  onDrop={(e) => {
                    e.preventDefault();
                    if (dragIndex.current !== null) moveOwnerGroup(dragIndex.current, idx);
                    dragIndex.current = null;
                  }}
                  className={cn(
                    "group flex items-center gap-1.5 rounded-lg px-1.5 py-1.5 transition-colors",
                    active ? "bg-primary-subtle text-primary" : "text-fg hover:bg-surface-hover",
                  )}
                >
                  <GripVertical
                    size={13}
                    className="shrink-0 cursor-grab text-fg-subtle"
                    aria-hidden
                  />
                  {renderScopeCheckbox(g.name, stat, (show) => {
                    if (selectedApp !== "all") {
                      void onSetChannelsApp({ app: selectedApp, show, ownerGroup: g.id });
                    }
                  })}
                  <button
                    type="button"
                    onClick={() => {
                      setScope({ kind: "ownerGroup", id: g.id });
                      clearSelection();
                    }}
                    className={cn("min-w-0 flex-1 truncate text-left", active && "font-medium")}
                  >
                    {g.name}
                  </button>
                  <span className="hidden shrink-0 items-center gap-0.5 group-hover:flex group-focus-within:flex">
                    <button
                      type="button"
                      aria-label={t("Rename {name}", { name: g.name })}
                      onClick={() => {
                        setRenamingId(g.id);
                        setRenameDraft(g.name);
                        setCreatingGroup(false);
                      }}
                      className="rounded p-0.5 text-fg-subtle hover:bg-surface hover:text-fg"
                    >
                      <Pencil size={12} />
                    </button>
                    <button
                      type="button"
                      aria-label={t("Delete {name}", { name: g.name })}
                      onClick={() => void handleDeleteGroup(g)}
                      className="rounded p-0.5 text-fg-subtle hover:bg-surface hover:text-danger"
                    >
                      <Trash2 size={12} />
                    </button>
                  </span>
                  <span className="shrink-0 text-xs tabular-nums text-fg-muted">
                    {renderCount(stat)}
                  </span>
                </div>
              );
            })}
          </div>

          {/* Virtual channels */}
          {index.virtualCounts.total > 0 ? (
            <>
              <div className="mt-3 mb-1 px-2 text-[11px] font-semibold uppercase tracking-wider text-fg-subtle">
                {t("Your channels")}
              </div>
              <div
                className={cn(
                  "flex items-center gap-2 rounded-lg px-2 py-1.5 transition-colors",
                  scope.kind === "virtual"
                    ? "bg-primary-subtle text-primary"
                    : "text-fg hover:bg-surface-hover",
                )}
              >
                {renderScopeCheckbox(t("Virtual channels"), index.virtualCounts, (show) => {
                  if (selectedApp !== "all") {
                    void onSetChannelsApp({ app: selectedApp, show, source: "virtual" });
                  }
                })}
                <button
                  type="button"
                  onClick={() => {
                    setScope({ kind: "virtual" });
                    clearSelection();
                  }}
                  className={cn(
                    "min-w-0 flex-1 truncate text-left",
                    scope.kind === "virtual" && "font-medium",
                  )}
                >
                  {t("Virtual channels")}
                </button>
                <span className="shrink-0 text-xs tabular-nums text-fg-muted">
                  {renderCount(index.virtualCounts)}
                </span>
              </div>
            </>
          ) : null}

          {/* Channel sources & provider groups */}
          {[...index.sourceMap.entries()]
            .filter(([, entry]) => entry.counts.total > 0)
            .map(([sid, entry]) => {
              const sname = sourceNameById.get(sid) || sid || "IPTV";
              const sourceActive = sameScope(scope, { kind: "source", sourceId: sid });
              return (
                <div key={sid || "__default__"} className="mt-3 space-y-0.5">
                  <div className="mb-1 flex items-center justify-between px-2 text-[11px] font-semibold uppercase tracking-wider text-fg-subtle">
                    <span className="truncate">{sname}</span>
                    <span className="ml-2 shrink-0 tabular-nums">
                      {formatNumber(entry.counts.total)}
                    </span>
                  </div>
                  <div
                    className={cn(
                      "flex items-center gap-2 rounded-lg px-2 py-1.5 transition-colors",
                      sourceActive
                        ? "bg-primary-subtle text-primary"
                        : "text-fg hover:bg-surface-hover",
                    )}
                  >
                    {renderScopeCheckbox(sname, entry.counts, (show) => {
                      if (selectedApp !== "all") {
                        void onSetChannelsApp({ app: selectedApp, show, source: sid });
                      }
                    })}
                    <button
                      type="button"
                      onClick={() => {
                        setScope({ kind: "source", sourceId: sid });
                        clearSelection();
                      }}
                      className="min-w-0 flex-1 truncate text-left font-semibold"
                    >
                      {sname}
                    </button>
                    <span className="shrink-0 text-xs tabular-nums text-fg-muted">
                      {renderCount(entry.counts)}
                    </span>
                  </div>
                  {[...entry.groups.entries()].map(([gname, gstat]) => {
                    const label = gname || t("Ungrouped");
                    const groupActive = sameScope(scope, {
                      kind: "providerGroup",
                      sourceId: sid,
                      group: gname,
                    });
                    return (
                      <div
                        key={`${sid}\u0000${gname}`}
                        className={cn(
                          "flex items-center gap-2 rounded-lg py-1.5 pr-2 pl-6 transition-colors",
                          groupActive
                            ? "bg-primary-subtle text-primary"
                            : "text-fg hover:bg-surface-hover",
                        )}
                      >
                        {renderScopeCheckbox(label, gstat, (show) => {
                          if (selectedApp !== "all") {
                            void onSetChannelsApp({
                              app: selectedApp,
                              show,
                              source: sid,
                              group: gname,
                            });
                          }
                        })}
                        <button
                          type="button"
                          onClick={() => {
                            setScope({ kind: "providerGroup", sourceId: sid, group: gname });
                            clearSelection();
                          }}
                          className={cn(
                            "min-w-0 flex-1 truncate text-left",
                            groupActive && "font-medium",
                          )}
                        >
                          {label}
                        </button>
                        <span className="shrink-0 text-xs tabular-nums text-fg-muted">
                          {renderCount(gstat)}
                        </span>
                      </div>
                    );
                  })}
                </div>
              );
            })}

          <p className="mt-4 px-2 pt-2 text-[11px] leading-relaxed text-fg-subtle">
            {selectedApp === "all"
              ? t("Pick an app above to add or remove whole groups.")
              : t("Tick a group to add or remove all of it in {app}. – means some.", {
                  app: liveAppChipLabel(selectedApp),
                })}
          </p>
        </nav>

        {/* Right virtualised channel list */}
        <section
          aria-label={t("Channels")}
          className="flex min-h-[24rem] min-w-0 flex-1 flex-col overflow-hidden rounded-xl border border-border bg-surface lg:min-h-0"
        >
          {/* List header */}
          <div className="flex shrink-0 flex-wrap items-center gap-2.5 border-b border-border px-3 py-2.5">
            <Checkbox
              aria-label={t("Select all matches")}
              checked={rows.length > 0 && checked.size === rows.length}
              disabled={busy || rows.length === 0}
              ref={(el) => {
                if (el) el.indeterminate = checked.size > 0 && checked.size < rows.length;
              }}
              onChange={() => {
                if (checked.size === rows.length) clearSelection();
                else selectAllMatches();
              }}
            />
            <h2 className="min-w-0 truncate text-sm font-semibold text-fg">{scopeTitle}</h2>
            <span className="text-xs tabular-nums text-fg-muted">
              {tn(rows.length, "{count} channel", "{count} channels")}
              {statusCounts.disabled > 0 ? (
                <>
                  {" · "}
                  {tn(statusCounts.disabled, "{count} disabled", "{count} disabled")}
                </>
              ) : null}
            </span>
            <div className="ml-auto flex flex-wrap items-center gap-1.5">
              <Segmented
                ariaLabel={t("Status")}
                size="sm"
                value={statusFilter}
                onChange={(next) => {
                  setStatusFilter(next);
                  clearSelection();
                }}
                options={statusOptions}
              />
              <ToolbarPicker
                ariaLabel={t("Show")}
                value={filter}
                onChange={onFilterChange}
                options={CHANNEL_FILTERS.map((f) => ({
                  ...f,
                  hint:
                    f.value === "missing" && missingCount !== null
                      ? tn(missingCount, "{count} channel", "{count} channels")
                      : undefined,
                }))}
                menuClassName="w-48"
              />
              <ToolbarPicker
                ariaLabel={t("Sort channels")}
                value={sort}
                onChange={onSortChange}
                options={[
                  { value: "number", label: t("Channel #") },
                  { value: "name", label: t("Channel Name") },
                  { value: "id", label: t("Channel ID") },
                  { value: "source", label: t("Source") },
                ]}
                menuClassName="w-40"
              />
              <div className="relative w-44 sm:w-56">
                <Search
                  size={13}
                  className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-fg-subtle"
                  aria-hidden
                />
                <Input
                  type="search"
                  value={rawQuery}
                  onChange={(e) => onQueryChange(e.currentTarget.value)}
                  placeholder={t("Search in {scope}…", { scope: scopeTitle })}
                  aria-label={t("Search channels")}
                  className="h-8 pl-7 text-sm"
                />
              </div>
            </div>
          </div>

          {/* Virtualised rows */}
          <div
            ref={listRef}
            onScroll={(e) => setScrollTop(e.currentTarget.scrollTop)}
            className="app-scroll min-h-0 flex-1 overflow-y-auto"
          >
            {channels === null ? (
              <div className="flex h-48 items-center justify-center p-6">
                <p className="text-xs text-fg-muted">{t("Loading channels…")}</p>
              </div>
            ) : rows.length === 0 ? (
              <div className="flex h-56 flex-col items-center justify-center gap-2 p-6 text-center">
                <Tv size={24} className="text-fg-subtle" aria-hidden />
                <p className="text-xs text-fg-muted">{t("No channels match.")}</p>
              </div>
            ) : (
              <div style={{ height: rows.length * ROW_HEIGHT, position: "relative" }}>
                <div
                  style={{
                    position: "absolute",
                    top: startRow * ROW_HEIGHT,
                    left: 0,
                    right: 0,
                  }}
                >
                  {visibleRows.map((c) => {
                    const memberGroups = index.memberGroupsByChannel.get(c.id);
                    return (
                      <ChannelListRow
                        key={c.id}
                        channel={c}
                        memberGroups={memberGroups}
                        selectedApp={selectedApp}
                        checked={checked.has(c.id)}
                        active={selectedChannelId === c.id}
                        covered={hasCoverage(c)}
                        busy={busy}
                        onToggleChecked={toggleRowChecked}
                        onOpen={onOpenChannel}
                        onToggleApp={toggleSingleChannelApp}
                      />
                    );
                  })}
                </div>
              </div>
            )}
          </div>
        </section>
      </div>

      {checked.size > 0 ? (
        <ChannelBulkBar
          count={checked.size}
          total={rows.length}
          busy={busy}
          groups={providerGroups}
          apps={selectedAppsState}
          selectedApp={selectedApp}
          ownerGroups={ownerGroups}
          floating
          onAll={selectAllMatches}
          onClear={clearSelection}
          onApply={handleBulkApply}
          onSetApp={handleBulkSetApp}
          onAddToOwnerGroup={handleAddToOwnerGroup}
        />
      ) : null}
    </div>
  );
}

const ChannelListRow = memo(function ChannelListRow({
  channel: c,
  memberGroups,
  selectedApp,
  checked,
  active,
  covered,
  busy,
  onToggleChecked,
  onOpen,
  onToggleApp,
}: {
  channel: ChannelSummary;
  memberGroups?: readonly OwnerGroup[];
  selectedApp: "all" | LiveApp;
  checked: boolean;
  active: boolean;
  covered: boolean;
  busy: boolean;
  onToggleChecked: (id: string, range?: boolean) => void;
  onOpen: (id: string) => void;
  onToggleApp: (id: string, show: boolean) => void;
}) {
  const providerLabel = c.group || (c.kind === "virtual" ? t("Virtual") : "");
  const ownerGroupNames = memberGroups?.map((g) => g.name).join(", ") ?? "";
  const visibleInSelected =
    selectedApp === "all" ? true : channelVisibleInApp(c, selectedApp, memberGroups);

  return (
    <div
      style={{ height: ROW_HEIGHT }}
      className={cn(
        "flex items-center gap-3 border-b border-border px-3 transition-colors",
        active ? "bg-primary-subtle/60" : "hover:bg-surface-hover/60",
      )}
    >
      <Checkbox
        checked={checked}
        disabled={busy}
        aria-label={t("Select {name}", { name: c.name })}
        onClick={(e) => {
          e.stopPropagation();
        }}
        onChange={(e) => {
          const shift = e.nativeEvent instanceof MouseEvent ? e.nativeEvent.shiftKey : false;
          onToggleChecked(c.id, shift);
        }}
      />
      <button
        type="button"
        aria-label={`${c.number} ${c.name}`}
        onClick={() => onOpen(c.id)}
        className="flex min-w-0 flex-1 items-center gap-3 py-1.5 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
      >
        <span className="w-11 shrink-0 truncate text-xs tabular-nums text-fg-muted">
          {c.number}
        </span>
        <ChannelLogo name={c.name} logo={c.logo} seed={c.id} channelId={c.id} size={34} />
        <span className="min-w-0 flex-1">
          <span className="flex items-center gap-2">
            <span className="truncate text-sm font-medium text-fg">{c.name}</span>
            {!c.enabled ? <Badge>{t("Off air")}</Badge> : null}
          </span>
          <span className="block truncate text-xs text-fg-muted">
            {providerLabel}
            {providerLabel && ownerGroupNames ? " · " : ""}
            {ownerGroupNames ? <span className="text-fg">{ownerGroupNames}</span> : null}
            {!covered ? (
              <>
                {providerLabel || ownerGroupNames ? " · " : ""}
                <span className="font-medium text-warning">{t("needs a guide")}</span>
              </>
            ) : null}
          </span>
        </span>
      </button>
      <div className="flex shrink-0 items-center gap-1">
        {selectedApp === "all" ? (
          <div className="hidden flex-wrap items-center justify-end gap-1 sm:flex">
            {LIVE_APPS.map((app) => {
              const on = channelVisibleInApp(c, app, memberGroups);
              return (
                <span
                  key={app}
                  className={cn(
                    "rounded-full border px-2 py-0.5 text-[11px] leading-tight",
                    on
                      ? "border-primary/35 bg-primary-subtle text-primary"
                      : "border-border bg-bg-subtle text-fg-subtle opacity-60",
                  )}
                >
                  {liveAppChipLabel(app)}
                </span>
              );
            })}
          </div>
        ) : (
          <Switch
            checked={visibleInSelected}
            disabled={busy}
            aria-label={t("Show {channel} in {app}", {
              channel: c.name,
              app: liveAppChipLabel(selectedApp),
            })}
            onChange={(e) => onToggleApp(c.id, e.currentTarget.checked)}
          />
        )}
      </div>
    </div>
  );
});
