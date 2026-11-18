import { Badge, Button, cn, toast } from "@braedonsaunders/appkit-ui";
import { AlertTriangle, CalendarRange, Pencil, Plus, RefreshCw } from "lucide-react";
import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { Segmented } from "../../components/fields";
import { ToolbarPicker } from "../../components/ToolbarPicker";
import { t } from "../../i18n";
import { errorMessage } from "../../lib/api";
import { formatNumber, relativeTime } from "../../lib/format";
import { useDebouncedValue, useInterval } from "../../lib/hooks";
import { LIVE_APPS, liveAppName } from "./ChannelApps";
import { GuideSourceList, GuideWorkbench, guideSourceStatus, sourceTitle } from "./GuideData";
import { GuideGrid } from "./GuideGrid";
import { filterGuideChannels, groupGuideChannels, type GuideAppFilter } from "./guide-filter";
import { sourceImportError } from "./sourceErrors";
import {
  MINUTE,
  groupBy,
  isGuideSource,
  tv,
  type Channel,
  type ChannelSummary,
  type Coverage,
  type OwnerGroup,
  type Programme,
  type Source,
} from "./model";

const HOUR = 60 * MINUTE;

function windowStart(shiftHours: number): Date {
  const d = new Date();
  d.setMinutes(0, 0, 0);
  return new Date(d.getTime() - HOUR + shiftHours * HOUR);
}

type Sub = "grid" | "mapping" | "sources";

/**
 * The Guide tab as one full-width page: the TV guide grid, channel mapping
 * and guide sources behind a segmented control in one toolbar row.
 */
export function GuidePage({
  initialSub = "grid",
  channels,
  sources,
  ownerGroups = [],
  coverage,
  query,
  guideUntil,
  missingCount,
  selectedChannelId,
  guideSourceId,
  onSelectGuideSource,
  onSelectChannel,
  onInspect,
  onPlay,
  onMapChannel,
  onToggleFavorite,
  onEditSource,
  onReload,
}: {
  /** The guide-data route opens on channel mapping, the Guide tab on the grid. */
  initialSub?: Sub;
  channels: ChannelSummary[];
  sources: Source[];
  ownerGroups?: OwnerGroup[];
  coverage: Map<string, Coverage> | null;
  query: string;
  /** When listings run out (ms); undefined when some channel never runs out. */
  guideUntil?: number;
  missingCount: number | null;
  selectedChannelId: string | null;
  guideSourceId: string | null;
  onSelectGuideSource: (id: string | null) => void;
  onSelectChannel: (id: string) => void;
  onInspect: (c: Channel, p: Programme) => void;
  onPlay: (c: Channel) => void;
  onMapChannel: (id: string) => void;
  onToggleFavorite?: (c: Channel) => void;
  onEditSource: (id: string) => void;
  onReload: () => Promise<void>;
}) {
  const [sub, setSub] = useState<Sub>(initialSub);
  const [app, setApp] = useState<GuideAppFilter>("all");
  const [group, setGroup] = useState("");
  const [now, setNow] = useState(() => Date.now());
  useInterval(() => setNow(Date.now()), 30_000);

  const guideSources = useMemo(() => sources.filter(isGuideSource), [sources]);
  const guideSource = guideSourceId ? sources.find((s) => s.id === guideSourceId) : undefined;

  // Coverage over live IPTV channels (virtual channels build their own).
  const live = useMemo(() => channels.filter((c) => c.kind === "iptv" && c.enabled), [channels]);
  const missing = useMemo(
    () => live.filter((c) => coverage && (coverage.get(c.id)?.programmes ?? 0) === 0).length,
    [live, coverage],
  );
  const missingLabel = missingCount ?? missing;

  const groups = useMemo(() => groupGuideChannels(channels), [channels]);

  const subTabs = (
    <Segmented
      ariaLabel={t("Guide")}
      size="sm"
      className="p-px"
      value={sub}
      onChange={setSub}
      options={[
        { value: "grid", label: t("TV guide") },
        {
          value: "mapping",
          label: `${t("Channel mapping")} · ${formatNumber(missingLabel)}`,
        },
        {
          value: "sources",
          label: `${t("Guide sources")} · ${formatNumber(guideSources.length)}`,
        },
      ]}
    />
  );

  // One toolbar row: these sit at the start of the grid's own toolbar.
  const gridFilters = (
    <div className="flex min-w-0 flex-wrap items-center gap-2">
      <ToolbarPicker
        ariaLabel={t("As seen in")}
        value={app}
        onChange={(v) => setApp(v as GuideAppFilter)}
        options={[
          { value: "all", label: t("All apps") },
          ...LIVE_APPS.map((a) => ({ value: a, label: liveAppName(a) })),
        ]}
        searchable={false}
        menuClassName="w-48"
      />
      <ToolbarPicker
        ariaLabel={t("Group")}
        value={group}
        onChange={setGroup}
        className="max-w-[11rem]"
        options={[
          { value: "", label: t("All groups") },
          ...(groups.yours.length
            ? [{ value: "yours", label: t("Your groups"), group: t("Your groups") }]
            : []),
          ...groups.yours.map((g) => ({
            value: `name:${g}`,
            label: g,
            group: t("Your groups"),
          })),
          ...(groups.provider.length
            ? [
                {
                  value: "provider",
                  label: t("Provider groups"),
                  group: t("Provider groups"),
                },
              ]
            : []),
          ...groups.provider.map((g) => ({
            value: `name:${g}`,
            label: g,
            group: t("Provider groups"),
          })),
        ]}
      />
    </div>
  );

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {sub !== "grid" ? (
        <div className="flex flex-wrap items-center gap-2 border-b border-border bg-surface px-3 py-2 sm:px-4">
          {subTabs}
        </div>
      ) : null}

      {sub === "grid" ? (
        <TVGuide
          channels={channels}
          app={app}
          group={group}
          query={query}
          now={now}
          guideUntil={guideUntil}
          toolbarStart={subTabs}
          toolbarFilters={gridFilters}
          selectedChannelId={selectedChannelId}
          onSelectChannel={onSelectChannel}
          onInspect={onInspect}
          onPlay={onPlay}
          onMapChannel={onMapChannel}
          onToggleFavorite={onToggleFavorite}
        />
      ) : sub === "mapping" ? (
        <div className="app-scroll min-h-0 flex-1 overflow-y-auto">
          <div className="mx-auto grid max-w-7xl items-start gap-2 lg:grid-cols-[300px_minmax(0,1fr)]">
            <GuideSourceList
              query={query}
              lineup={channels}
              sources={sources}
              selected={guideSourceId}
              onSelect={onSelectGuideSource}
              now={now}
            />
            <GuideWorkbench
              channels={channels}
              query={query}
              sources={sources}
              ownerGroups={ownerGroups}
              coverage={coverage}
              source={guideSource}
              showCoverageCard={false}
              onMapChannel={onMapChannel}
              onEditSource={onEditSource}
              onReload={onReload}
              now={now}
            />
          </div>
        </div>
      ) : (
        <GuideSources
          sources={guideSources}
          channels={channels}
          now={now}
          onEditSource={onEditSource}
          onReload={onReload}
        />
      )}
    </div>
  );
}

/**
 * The EPG grid over the filtered lineup. GuideGrid virtualises rows and
 * reports the visible range; listings are fetched only for that window
 * (plus the first rows), never for the whole lineup.
 */
function TVGuide({
  channels,
  app,
  group,
  query,
  now,
  guideUntil,
  toolbarStart,
  toolbarFilters,
  selectedChannelId,
  onSelectChannel,
  onInspect,
  onPlay,
  onMapChannel,
  onToggleFavorite,
}: {
  channels: ChannelSummary[];
  app: GuideAppFilter;
  group: string;
  query: string;
  now: number;
  guideUntil?: number;
  toolbarStart?: ReactNode;
  toolbarFilters?: ReactNode;
  selectedChannelId: string | null;
  onSelectChannel: (id: string) => void;
  onInspect: (c: Channel, p: Programme) => void;
  onPlay: (c: Channel) => void;
  onMapChannel: (id: string) => void;
  onToggleFavorite?: (c: Channel) => void;
}) {
  const [shift, setShift] = useState(0);
  const [range, setRange] = useState<[number, number]>([0, 40]);
  const [programmes, setProgrammes] = useState<Programme[]>([]);
  const [loading, setLoading] = useState(false);
  const from = useMemo(() => windowStart(shift), [shift]);
  const until = useMemo(() => new Date(from.getTime() + 24 * HOUR), [from]);

  const needle = query.trim().toLowerCase();
  const rows = useMemo(() => {
    const filtered = filterGuideChannels(channels, { app, group });
    return needle
      ? filtered.filter((c) =>
          `${c.number} ${c.name} ${c.group ?? ""}`.toLowerCase().includes(needle),
        )
      : filtered;
  }, [channels, app, group, needle]);

  const guideIDs = useMemo(() => {
    const ids = new Set<string>();
    rows.slice(0, 120).forEach((c) => ids.add(c.id));
    rows.slice(range[0], range[1]).forEach((c) => ids.add(c.id));
    if (selectedChannelId) ids.add(selectedChannelId);
    return [...ids].join(",");
  }, [rows, range, selectedChannelId]);
  const debouncedIDs = useDebouncedValue(guideIDs, 150);

  useEffect(() => {
    if (!debouncedIDs) {
      setProgrammes([]);
      return;
    }
    const ctl = new AbortController();
    setLoading(true);
    tv.guide(from, until, debouncedIDs.split(","), ctl.signal)
      .then((ps) => {
        if (!ctl.signal.aborted) setProgrammes(ps ?? []);
      })
      .catch((e) => {
        if (!ctl.signal.aborted)
          toast.error(t("Couldn't load the guide"), { description: errorMessage(e) });
      })
      .finally(() => {
        if (!ctl.signal.aborted) setLoading(false);
      });
    return () => ctl.abort();
  }, [debouncedIDs, from, until]);

  const guide = useMemo(() => groupBy(programmes, (p) => p.channelId), [programmes]);
  const onRange = useCallback(
    (first: number, last: number) =>
      setRange((prev) => (prev[0] === first && prev[1] === last ? prev : [first, last])),
    [],
  );

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <GuideGrid
        toolbarStart={toolbarStart}
        toolbarFilters={toolbarFilters}
        // Summaries carry everything the grid's channel column reads.
        channels={rows as Channel[]}
        guide={guide}
        from={from}
        until={until}
        now={now}
        selected={selectedChannelId}
        onSelect={onSelectChannel}
        onInspect={onInspect}
        onPlay={onPlay}
        onMapGuide={(c) => onMapChannel(c.id)}
        onToggleFavorite={onToggleFavorite}
        guideUntil={guideUntil}
        onShift={(h) => setShift((s) => s + h)}
        onNow={() => setShift(0)}
        onRange={onRange}
        loading={loading}
        emptyHint={<p className="text-xs text-fg-muted">{t("No channels match.")}</p>}
      />
    </div>
  );
}

/** Every guide source, with refresh and edit; new ones are added in Sources. */
function GuideSources({
  sources,
  channels,
  now,
  onEditSource,
  onReload,
}: {
  sources: Source[];
  channels: ChannelSummary[];
  now: number;
  onEditSource: (id: string) => void;
  onReload: () => Promise<void>;
}) {
  const [refreshing, setRefreshing] = useState<string | null>(null);
  const usedBy = useMemo(() => {
    const counts = new Map<string, number>();
    for (const c of channels) {
      if (c.kind !== "iptv" || !c.guideId) continue;
      const id = c.guideSourceId || c.sourceId;
      if (id) counts.set(id, (counts.get(id) ?? 0) + 1);
    }
    return counts;
  }, [channels]);

  const refresh = async (s: Source) => {
    setRefreshing(s.id);
    try {
      await tv.refreshSource(s.id);
      toast.success(t("{name} refreshed", { name: sourceTitle(s) }));
      await onReload();
    } catch (e) {
      toast.error(t("Couldn't refresh the guide"), { description: sourceImportError(e) });
    } finally {
      setRefreshing(null);
    }
  };

  return (
    <div className="app-scroll min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto max-w-3xl space-y-3 p-3 sm:p-6">
        {sources.length === 0 ? (
          <p className="rounded-xl border border-dashed border-border p-8 text-center text-sm text-fg-muted">
            {t(
              "No guide sources yet. Add an XMLTV guide or your Plex guide to give channels listings.",
            )}
          </p>
        ) : (
          sources.map((s) => {
            const status = guideSourceStatus(s, usedBy.get(s.id) ?? 0, now);
            const busy = refreshing === s.id;
            return (
              <section
                key={s.id}
                className="flex flex-wrap items-start gap-3 rounded-2xl border border-border bg-surface p-4 shadow-xs"
              >
                <span className="grid size-9 shrink-0 place-items-center rounded-lg bg-info/15 text-info">
                  <CalendarRange size={17} />
                </span>
                <div className="min-w-0 flex-1 space-y-1">
                  <p className="flex flex-wrap items-center gap-2">
                    <span className="truncate text-sm font-medium text-fg">{sourceTitle(s)}</span>
                    {s.disabled ? <Badge>{t("Disabled")}</Badge> : null}
                    <Badge variant="secondary">
                      {s.kind === "plex" ? t("Plex") : s.hasPlaylist ? t("M3U guide") : t("XMLTV")}
                    </Badge>
                  </p>
                  <p
                    className={cn(
                      "flex items-center gap-1 text-xs",
                      status.warn ? "text-warning" : "text-fg-muted",
                    )}
                  >
                    {status.warn ? <AlertTriangle size={12} className="shrink-0" /> : null}
                    <span>
                      {status.text}
                      {s.updatedAt
                        ? ` · ${t("refreshed {when}", { when: relativeTime(s.updatedAt) })}`
                        : ""}
                    </span>
                  </p>
                  {s.lastError ? (
                    <p role="alert" className="text-xs text-danger">
                      {sourceImportError(s.lastError)}
                    </p>
                  ) : null}
                </div>
                <div className="flex gap-2">
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={busy}
                    onClick={() => void refresh(s)}
                  >
                    <RefreshCw size={14} className={cn(busy && "animate-spin")} /> {t("Refresh")}
                  </Button>
                  <Button variant="outline" size="sm" onClick={() => onEditSource(s.id)}>
                    <Pencil size={14} /> {t("Edit")}
                  </Button>
                </div>
              </section>
            );
          })
        )}
        <Button asChild variant="outline" className="w-full">
          <Link to="/sources/iptv?new=guide">
            <Plus size={14} /> {t("Add a guide source in Sources")}
          </Link>
        </Button>
      </div>
    </div>
  );
}
