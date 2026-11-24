// The one row drawer (design §2.6): "+" opens it at "What fills this row?",
// picking a source turns it into that row's settings, and clicking a row
// opens the same drawer to edit it. Every change goes straight into the page
// draft, so the page behind it previews it as you go.
import { Badge, Button, Input, Spinner, Switch, cn } from "@braedonsaunders/appkit-ui";
import { ChevronDown, ChevronRight, Puzzle, Search } from "lucide-react";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { Drawer } from "../../components/Drawer";
import { pluginCollectionHref } from "../collections/links";
import { ChipGroup, Segmented } from "../../components/fields";
import { SubtabNav } from "../../components/layout";
import { ToolbarPicker } from "../../components/ToolbarPicker";
import { viewerName } from "../../components/pickers";
import { t, tn } from "../../i18n";
import { useLocale } from "../../i18n/react";
import { api, errorMessage, isAbort } from "../../lib/api";
import type { Device, Viewer } from "../../lib/contract";
import {
  MAX_ROWS_PER_LIBRARY_ROW,
  SOURCE_GROUP_ORDER,
  daypartLabel,
  deviceLabel,
  fillLibrary,
  hideWatchedLabel,
  holidayGroupLabel,
  holidays,
  monthNames,
  presetOf,
  presetSchedule,
  sourceGroupLabel,
  withMode,
  type Daypart,
  type Experience,
  type FacetValueOption,
  type PageLibrary,
  type PageRow,
  type PageSource,
  type PlacementMode,
  type PreviewRow,
  type RowCard,
  type RowLook,
  type RowParam,
  type RowPlacement,
  type RowStyle,
  type Schedule,
  type SchedulePreset,
} from "../../lib/pages";
import { KindChip, RowBody, VelaOnlyChip } from "./RowVisuals";

export type DrawerState =
  | { kind: "add"; at: number; picking: boolean; preset?: { source: string; params: Record<string, string> } }
  | { kind: "edit"; rowId: string; tab?: "settings" | "content" };

export interface DrawerContext {
  pageKey: string;
  curated: boolean;
  /** The library of a library page; null on Home. */
  library: PageLibrary | null;
  /** Every library a Home row can draw from. */
  libraries: PageLibrary[];
  sources: PageSource[] | null;
  sourcesError: unknown;
  viewers: Viewer[];
  /** Editing for one viewer: only Look and Placement change, just for them. */
  editingFor: Viewer | null;
  preview: { viewer?: number; device: Device; at: string };
}

// ---------------------------------------------------------------- layout bits

function Section({ title, hint, children }: { title: string; hint?: string; children: ReactNode }) {
  return (
    <section className="space-y-2" aria-label={title}>
      <div>
        <h3 className="text-xs font-semibold uppercase tracking-wider text-fg-subtle">{title}</h3>
        {hint ? <p className="mt-0.5 text-sm text-fg-muted">{hint}</p> : null}
      </div>
      <div className="space-y-3.5 rounded-xl border border-border bg-surface p-3.5 text-sm">
        {children}
      </div>
    </section>
  );
}

function Line({
  label,
  hint,
  children,
}: {
  label: ReactNode;
  hint?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="space-y-1 border-t border-border-subtle pt-3 first:border-0 first:pt-0">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <span className="font-medium text-fg">{label}</span>
        {children}
      </div>
      {hint ? <p className="text-xs text-fg-muted">{hint}</p> : null}
    </div>
  );
}

function NumberField({
  label,
  value,
  onChange,
  min = 1,
  max = 50,
  disabled,
}: {
  label: string;
  value: number | undefined;
  onChange: (v: number | undefined) => void;
  min?: number;
  max?: number;
  disabled?: boolean;
}) {
  return (
    <Input
      aria-label={label}
      type="number"
      inputMode="numeric"
      min={min}
      max={max}
      className="h-8 w-20"
      disabled={disabled}
      value={value ?? ""}
      onChange={(e) => {
        const raw = e.currentTarget.value;
        if (raw === "") return onChange(undefined);
        const n = Math.round(Number(raw));
        if (Number.isFinite(n)) onChange(Math.min(max, Math.max(min, n)));
      }}
    />
  );
}

// ---------------------------------------------------------------- source picker

interface PickEntry {
  key: string;
  source: PageSource;
  title: string;
  description: string;
  params: Record<string, string>;
}

function entriesFor(sources: PageSource[]): PickEntry[] {
  const out: PickEntry[] = [];
  for (const s of sources) {
    if (!s.available) continue;
    const ref = s.params?.find((p) => p.type === "ref" && p.required);
    if (ref && s.refs?.length) {
      for (const r of s.refs) {
        out.push({
          key: `${s.kind}:${r.value}`,
          source: s,
          title: r.label,
          description: s.title,
          params: { [ref.key]: r.value },
        });
      }
      continue;
    }
    out.push({ key: s.kind, source: s, title: s.title, description: s.description, params: {} });
  }
  return out;
}

function SourcePicker({
  sources,
  error,
  onPick,
}: {
  sources: PageSource[] | null;
  error: unknown;
  onPick: (s: PageSource, params: Record<string, string>) => void;
}) {
  const [q, setQ] = useState("");
  const { locale } = useLocale();
  const groups = useMemo(() => {
    const needle = q.trim().toLowerCase();
    const entries = entriesFor(sources ?? []).filter(
      (e) =>
        !needle ||
        `${e.title} ${e.description} ${e.source.plugin ?? ""}`.toLowerCase().includes(needle),
    );
    const byGroup = new Map<string, PickEntry[]>();
    for (const e of entries) {
      const g = e.source.group || "more";
      byGroup.set(g, [...(byGroup.get(g) ?? []), e]);
    }
    return [...byGroup.entries()]
      .sort(
        ([a], [b]) =>
          (SOURCE_GROUP_ORDER.indexOf(a) + 100) % 100 - (SOURCE_GROUP_ORDER.indexOf(b) + 100) % 100,
      )
      .map(([g, items]) => ({ group: g, label: sourceGroupLabel(g), items }));
  }, [sources, q, locale]);
  if (error) return <p className="text-sm text-danger">{errorMessage(error)}</p>;
  if (!sources) return <Spinner size={18} label={t("Loading what can fill a row")} />;
  return (
    <div className="space-y-4">
      <div>
        <h3 className="text-base font-semibold text-fg">{t("What fills this row?")}</h3>
        <p className="text-sm text-fg-muted">
          {t("Built-in rows, your collections and plugin rows that fit this page. Pick one to set it up.")}
        </p>
      </div>
      <div className="relative">
        <Search
          size={14}
          className="absolute left-2.5 top-1/2 -translate-y-1/2 text-fg-subtle"
          aria-hidden
        />
        <Input
          aria-label={t("Search rows")}
          className="h-9 pl-8"
          placeholder={t("Search: trending, anime, playlists, family…")}
          value={q}
          onChange={(e) => setQ(e.currentTarget.value)}
        />
      </div>
      {groups.length === 0 ? (
        <p className="text-sm text-fg-muted">{t("Nothing matches that search.")}</p>
      ) : null}
      <div className="grid gap-x-6 gap-y-4 sm:grid-cols-2">
        {groups.map((g) => (
          <div key={g.group} role="group" aria-label={g.label}>
            <h4 className="mb-1 text-xs font-semibold uppercase tracking-wider text-fg-subtle">
              {g.label}
            </h4>
            <div className="space-y-0.5">
              {g.items.map((e) => (
                <button
                  type="button"
                  key={e.key}
                  aria-label={e.title}
                  onClick={() => onPick(e.source, e.params)}
                  className="block w-full rounded-lg border border-transparent px-2.5 py-1.5 text-left hover:border-border hover:bg-surface"
                >
                  <span className="flex flex-wrap items-center gap-1.5">
                    <span className="text-sm font-medium text-fg">{e.title}</span>
                    <KindChip personal={e.source.personal} />
                    {e.source.kind === "surface" || e.source.kind.startsWith("surface:") ? (
                      <VelaOnlyChip />
                    ) : null}
                    {e.source.used > 0 ? (
                      <span className="text-[11px] text-fg-subtle">
                        {t("already on this page")}
                      </span>
                    ) : null}
                  </span>
                  <span className="block text-xs text-fg-muted">
                    {e.source.plugin && g.group === "plugins"
                      ? `${e.source.plugin} · ${e.description}`
                      : e.description}
                  </span>
                </button>
              ))}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------- parameters

function FacetPicker({
  param,
  value,
  library,
  onChange,
  disabled,
}: {
  param: RowParam;
  value: string;
  library: number | null;
  onChange: (v: string) => void;
  disabled?: boolean;
}) {
  const [pinning, setPinning] = useState(Boolean(value));
  const [q, setQ] = useState("");
  const [values, setValues] = useState<FacetValueOption[] | null>(null);
  useEffect(() => {
    if (!pinning || library == null || !param.facet) return;
    const ctl = new AbortController();
    const timer = window.setTimeout(() => {
      api
        .facetValues(library, param.facet!, q, ctl.signal)
        .then((r) => setValues(r.values ?? []))
        .catch((e) => {
          if (!isAbort(e)) setValues([]);
        });
    }, 200);
    return () => {
      window.clearTimeout(timer);
      ctl.abort();
    };
  }, [pinning, library, param.facet, q]);
  return (
    <div className="space-y-2 border-t border-border-subtle pt-3 first:border-0 first:pt-0">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <span className="font-medium text-fg">{param.label}</span>
        <Segmented
          ariaLabel={param.label}
          size="sm"
          value={pinning ? "pin" : "ether"}
          onChange={(v) => {
            setPinning(v === "pin");
            if (v === "ether") onChange("");
          }}
          options={[
            { value: "ether", label: t("Ether picks for each viewer"), disabled },
            { value: "pin", label: t("Pick one…"), disabled },
          ]}
        />
      </div>
      {pinning ? (
        <div className="space-y-2">
          {value ? (
            <p className="text-sm">
              {t("Pinned to:")} <Badge variant="info">{values?.find((v) => v.value === value)?.label ?? value}</Badge>
            </p>
          ) : null}
          <Input
            aria-label={t("Search {facet}", { facet: param.label })}
            className="h-8"
            placeholder={t("Search…")}
            value={q}
            disabled={disabled}
            onChange={(e) => setQ(e.currentTarget.value)}
          />
          {library == null ? (
            <p className="text-xs text-fg-muted">{t("Choose a library first.")}</p>
          ) : values === null ? (
            <Spinner size={14} />
          ) : (
            <div className="flex max-h-40 flex-wrap gap-1.5 overflow-y-auto">
              {values.map((v) => (
                <button
                  key={v.value}
                  type="button"
                  disabled={disabled}
                  onClick={() => onChange(v.value)}
                  className={cn(
                    "rounded-full border px-2.5 py-0.5 text-xs",
                    v.value === value
                      ? "border-primary bg-primary text-primary-fg"
                      : "border-border hover:border-primary",
                  )}
                >
                  {v.label}
                  {v.count ? <span className="ml-1 opacity-70">{v.count}</span> : null}
                </button>
              ))}
            </div>
          )}
        </div>
      ) : null}
    </div>
  );
}

function ParamControl({
  param,
  source,
  value,
  library,
  onChange,
  disabled,
}: {
  param: RowParam;
  source: PageSource;
  value: string;
  library: number | null;
  onChange: (v: string) => void;
  disabled?: boolean;
}) {
  if (param.type === "facetValue") {
    return (
      <FacetPicker
        param={param}
        value={value}
        library={library}
        onChange={onChange}
        disabled={disabled}
      />
    );
  }
  if (param.type === "count") {
    return (
      <Line label={param.label}>
        <NumberField
          label={param.label}
          value={value ? Number(value) : param.default ? Number(param.default) : undefined}
          min={param.min || 1}
          max={param.max || 10}
          disabled={disabled}
          onChange={(n) => onChange(n === undefined ? "" : String(n))}
        />
      </Line>
    );
  }
  const choices = param.type === "ref" ? (source.refs ?? []) : (param.choices ?? []);
  const current = value || param.default || "";
  if (param.type === "choice" && choices.length <= 4) {
    return (
      <Line label={param.label}>
        <Segmented
          ariaLabel={param.label}
          size="sm"
          value={current}
          onChange={onChange}
          options={choices.map((c) => ({ value: c.value, label: c.label, disabled }))}
        />
      </Line>
    );
  }
  return (
    <Line label={param.label}>
      <ToolbarPicker
        ariaLabel={param.label}
        value={current}
        onChange={onChange}
        disabled={disabled}
        searchable={choices.length > 8}
        options={[
          ...(param.required ? [] : [{ value: "", label: t("Any") }]),
          ...choices.map((c) => ({ value: c.value, label: c.label })),
        ]}
      />
    </Line>
  );
}

// ---------------------------------------------------------------- libraries

function LibrariesSection({
  row,
  source,
  libraries,
  onChange,
  disabled,
}: {
  row: PageRow;
  source: PageSource;
  libraries: PageLibrary[];
  onChange: (r: PageRow) => void;
  disabled?: boolean;
}) {
  const supported = libraries.filter(
    (l) => !source.libraries || source.libraries.includes(l.id),
  );
  const unsupported = libraries.filter((l) => !supported.includes(l));
  const chosen = row.scope.libraries ?? [];
  const these = chosen.length > 0;
  const per = Boolean(row.scope.perLibrary);
  const firstExp = libraries.find((l) => l.id === chosen[0])?.experience;
  const offered = supported.filter((l) => per || !these || !firstExp || l.experience === firstExp);
  const otherKind = supported.filter((l) => !offered.includes(l));
  const fanCount = per ? (these ? chosen.length : supported.length) : 1;
  const set = (scope: PageRow["scope"]) =>
    onChange({
      ...row,
      scope: {
        libraries: scope.libraries?.length ? scope.libraries : undefined,
        perLibrary: scope.perLibrary || undefined,
      },
    });
  return (
    <Section title={t("Libraries")} hint={t("Where the titles come from.")}>
      <Line label={t("Draw from")}>
        <Segmented
          ariaLabel={t("Draw from")}
          size="sm"
          value={these ? "these" : "any"}
          onChange={(v) =>
            set({
              ...row.scope,
              libraries: v === "these" ? supported.slice(0, 1).map((l) => l.id) : undefined,
            })
          }
          options={[
            { value: "any", label: t("Any library you can watch"), disabled },
            { value: "these", label: t("These libraries"), disabled },
          ]}
        />
      </Line>
      {these ? (
        <ChipGroup
          ariaLabel={t("Libraries")}
          size="sm"
          value={chosen}
          onChange={(v) => !disabled && set({ ...row.scope, libraries: v })}
          options={offered.map((l) => ({ value: l.id, label: l.name }))}
        />
      ) : null}
      {these && otherKind.length ? (
        <p className="text-xs text-fg-muted">
          {t("{libraries}: a different kind of library. Use one row per library to include them.", {
            libraries: otherKind.map((l) => l.name).join(", "),
          })}
        </p>
      ) : null}
      {unsupported.length ? (
        <p className="text-xs text-fg-muted">
          {t("{libraries} can't fill this row.", {
            libraries: unsupported.map((l) => l.name).join(", "),
          })}
        </p>
      ) : null}
      {source.split ? (
        <Line
          label={t("Show as")}
          hint={
            per
              ? fanCount > MAX_ROWS_PER_LIBRARY_ROW
                ? t("Only the first {max} libraries get a row.", { max: MAX_ROWS_PER_LIBRARY_ROW })
                : tn(
                    fanCount,
                    "One row, kept together with the others. Up to {max} libraries.",
                    "{count} rows kept together, one per library. Up to {max} libraries.",
                    { max: MAX_ROWS_PER_LIBRARY_ROW },
                  )
              : t("One row mixing the libraries.")
          }
        >
          <Segmented
            ariaLabel={t("Show as")}
            size="sm"
            value={per ? "per" : "one"}
            onChange={(v) => set({ ...row.scope, perLibrary: v === "per" })}
            options={[
              { value: "one", label: t("One combined row"), disabled },
              { value: "per", label: t("One row per library"), disabled },
            ]}
          />
        </Line>
      ) : null}
    </Section>
  );
}

// ---------------------------------------------------------------- look

function triState(v: boolean | null | undefined): "source" | "on" | "off" {
  return v == null ? "source" : v ? "on" : "off";
}
function fromTri(v: string): boolean | null {
  return v === "source" ? null : v === "on";
}

function LookSection({
  look,
  source,
  experience,
  onChange,
}: {
  look: RowLook;
  source: PageSource;
  experience: Experience;
  onChange: (l: RowLook) => void;
}) {
  const listen = experience === "listen" || source.look.style === "tiles" || source.look.style === "tracks";
  const styles: { value: RowStyle; label: string }[] = [
    { value: "", label: t("Source default") },
    { value: "standard", label: t("Standard") },
    { value: "hero", label: t("Hero") },
    { value: "wide", label: t("Wide") },
    ...(listen
      ? [
          { value: "tiles" as const, label: t("Tiles") },
          { value: "tracks" as const, label: t("Track list") },
        ]
      : []),
  ];
  const cards: { value: RowCard; label: string }[] = [
    { value: "", label: t("Follow the library") },
    { value: "poster", label: t("Poster") },
    { value: "landscape", label: t("Landscape") },
    { value: "square", label: t("Square") },
    { value: "round", label: t("Round") },
  ];
  const patch = (p: Partial<RowLook>) => {
    const next: RowLook = { ...look, ...p };
    for (const k of Object.keys(next) as (keyof RowLook)[]) {
      if (next[k] === null || next[k] === undefined || next[k] === "" || next[k] === false)
        delete next[k];
    }
    onChange(next);
  };
  return (
    <Section title={t("Look")} hint={t("Changes show in the page as you make them.")}>
      <Line
        label={t("Style")}
        hint={t("Plex, Vela and Silo show Hero and Wide. Jellyfin and Emby keep their own look. Tiles and Track list are Vela's listening layouts.")}
      >
        <ToolbarPicker
          ariaLabel={t("Style")}
          value={look.style ?? ""}
          onChange={(v) => patch({ style: v })}
          options={styles}
        />
      </Line>
      <Line label={t("Cards")} hint={t("Plex and Vela follow the card shape; other apps use the library's.")}>
        <ToolbarPicker
          ariaLabel={t("Cards")}
          value={look.card ?? ""}
          onChange={(v) => patch({ card: v })}
          options={cards}
        />
      </Line>
      <Line
        label={hideWatchedLabel(experience)}
        hint={t("Leave out titles the viewer has finished. Every app honours it.")}
      >
        <Switch
          aria-label={hideWatchedLabel(experience)}
          checked={Boolean(look.hideWatched)}
          onChange={(e) => patch({ hideWatched: e.currentTarget.checked })}
        />
      </Line>
      <Line
        label={t("Numbered")}
        hint={t("Number the first ten, Top 10 style. Plex, Vela, Silo, Jellyfin and Emby show it.")}
      >
        <Segmented
          ariaLabel={t("Numbered")}
          size="sm"
          value={triState(look.numbered)}
          onChange={(v) => patch({ numbered: fromTri(v) })}
          options={[
            { value: "source", label: source.look.numbered ? t("Source (on)") : t("Source (off)") },
            { value: "on", label: t("On") },
            { value: "off", label: t("Off") },
          ]}
        />
      </Line>
      {experience === "watch" || experience === "" ? (
        <Line
          label={t("Trailer on focus")}
          hint={t("Plays a trailer when the row is focused, where the app can.")}
        >
          <Segmented
            ariaLabel={t("Trailer on focus")}
            size="sm"
            value={triState(look.trailer)}
            onChange={(v) => patch({ trailer: fromTri(v) })}
            options={[
              { value: "source", label: t("Follow the source") },
              { value: "on", label: t("On") },
              { value: "off", label: t("Off") },
            ]}
          />
        </Line>
      ) : null}
      <Line label={t("Titles other rows show")}>
        <Segmented
          ariaLabel={t("Titles other rows show")}
          size="sm"
          value={look.keepWhole ? "keep" : "skip"}
          onChange={(v) => patch({ keepWhole: v === "keep" })}
          options={[
            { value: "skip", label: t("Skip them") },
            { value: "keep", label: t("Keep this row whole") },
          ]}
        />
      </Line>
    </Section>
  );
}

// ---------------------------------------------------------------- placement

const DEVICES: Device[] = ["tv", "mobile", "desktop", "web"];
const DAYPARTS: Daypart[] = ["morning", "afternoon", "evening", "late"];

function ScheduleEditor({
  when,
  onChange,
}: {
  when: Schedule | null | undefined;
  onChange: (s: Schedule | null) => void;
}) {
  const preset = presetOf(when);
  const [custom, setCustom] = useState(preset === "custom");
  const shown: SchedulePreset = custom ? "custom" : preset;
  const presets: { value: SchedulePreset; label: string }[] = [
    { value: "any", label: t("Any time") },
    { value: "evenings", label: t("Evenings") },
    { value: "weeknights", label: t("Weeknights") },
    { value: "weekends", label: t("Weekends") },
    { value: "weekend-nights", label: t("Weekend nights") },
    { value: "mornings", label: t("Mornings") },
    { value: "holidays", label: t("Holidays…") },
    { value: "custom", label: t("Custom…") },
  ];
  const s = when ?? {};
  const set = (p: Partial<Schedule>) => {
    const next: Schedule = { ...s, ...p };
    for (const k of Object.keys(next) as (keyof Schedule)[]) {
      const v = next[k];
      if (v === undefined || (Array.isArray(v) && v.length === 0 && k !== "holidays"))
        delete next[k];
    }
    onChange(next);
  };
  const days = [t("Sun"), t("Mon"), t("Tue"), t("Wed"), t("Thu"), t("Fri"), t("Sat")];
  const months = monthNames();
  const all = holidays();
  const groups = [...new Set(all.map((h) => h.group))];
  const range = s.dates?.[0];
  return (
    <div className="space-y-2 border-t border-border-subtle pt-3">
      <span className="font-medium text-fg">{t("When")}</span>
      <div className="flex flex-wrap gap-1.5" role="radiogroup" aria-label={t("When")}>
        {presets.map((p) => (
          <button
            key={p.value}
            type="button"
            role="radio"
            aria-checked={shown === p.value}
            onClick={() => {
              setCustom(p.value === "custom");
              if (p.value === "custom") onChange(when ?? {});
              else onChange(presetSchedule(p.value));
            }}
            className={cn(
              "rounded-full border px-2.5 py-0.5 text-xs",
              shown === p.value
                ? "border-primary bg-primary text-primary-fg"
                : "border-border text-fg hover:border-primary",
            )}
          >
            {p.label}
          </button>
        ))}
      </div>
      {shown === "holidays" ? (
        <div className="space-y-2">
          {groups.map((g) => (
            <div key={g}>
              <p className="text-[11px] font-semibold uppercase tracking-wider text-fg-subtle">
                {holidayGroupLabel(g)}
              </p>
              <ChipGroup
                ariaLabel={holidayGroupLabel(g)}
                size="sm"
                value={(s.holidays ?? []).filter((h) => all.some((x) => x.id === h && x.group === g))}
                onChange={(v) =>
                  set({
                    holidays: [
                      ...(s.holidays ?? []).filter(
                        (h) => !all.some((x) => x.id === h && x.group === g),
                      ),
                      ...v,
                    ],
                  })
                }
                options={all.filter((h) => h.group === g).map((h) => ({ value: h.id, label: h.label }))}
              />
            </div>
          ))}
          <p className="text-xs text-fg-muted">
            {t("Moving dates (Easter, Ramadan, Diwali…) are worked out every year.")}
          </p>
        </div>
      ) : null}
      {shown === "custom" ? (
        <div className="space-y-2">
          <ChipGroup
            ariaLabel={t("Days")}
            size="sm"
            value={s.days ?? []}
            onChange={(v) => set({ days: v })}
            options={days.map((d, i) => ({ value: i, label: d }))}
          />
          <ChipGroup
            ariaLabel={t("Times of day")}
            size="sm"
            value={s.dayparts ?? []}
            onChange={(v) => set({ dayparts: v })}
            options={DAYPARTS.map((d) => ({ value: d, label: daypartLabel(d) }))}
          />
          <p className="text-xs text-fg-muted">
            {t("Late is 10 PM to 5 AM and belongs to the day it started.")}
          </p>
          <ChipGroup
            ariaLabel={t("Months")}
            size="sm"
            value={s.months ?? []}
            onChange={(v) => set({ months: v })}
            options={months.map((m, i) => ({ value: i + 1, label: m }))}
          />
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-fg-muted">{t("Dates")}</span>
            <Input
              aria-label={t("From date")}
              type="date"
              className="h-8 w-40"
              value={range ? dateInput(range.from) : ""}
              onChange={(e) =>
                set({
                  dates: e.currentTarget.value
                    ? [{ from: e.currentTarget.value, to: range?.to ? dateInput(range.to) : e.currentTarget.value, yearly: range?.yearly }].map(yearlyRange)
                    : [],
                })
              }
            />
            <span>–</span>
            <Input
              aria-label={t("To date")}
              type="date"
              className="h-8 w-40"
              value={range ? dateInput(range.to) : ""}
              onChange={(e) =>
                set({
                  dates: e.currentTarget.value
                    ? [{ from: range?.from ? dateInput(range.from) : e.currentTarget.value, to: e.currentTarget.value, yearly: range?.yearly }].map(yearlyRange)
                    : [],
                })
              }
            />
            <label className="inline-flex items-center gap-1.5 text-xs">
              <Switch
                aria-label={t("Repeats every year")}
                checked={Boolean(range?.yearly)}
                disabled={!range}
                onChange={(e) =>
                  range &&
                  set({
                    dates: [
                      yearlyRange({
                        from: dateInput(range.from),
                        to: dateInput(range.to),
                        yearly: e.currentTarget.checked,
                      }),
                    ],
                  })
                }
              />
              {t("Repeats every year")}
            </label>
          </div>
        </div>
      ) : null}
    </div>
  );
}

/** A stored date ("MM-DD" yearly, "YYYY-MM-DD" once) as a date input value. */
function dateInput(v: string): string {
  return /^\d{2}-\d{2}$/.test(v) ? `${new Date().getFullYear()}-${v}` : v;
}
function yearlyRange(r: { from: string; to: string; yearly?: boolean }) {
  return r.yearly
    ? { from: r.from.slice(5), to: r.to.slice(5), yearly: true }
    : { from: r.from, to: r.to };
}

function PlacementSection({
  placement,
  curated,
  viewers,
  onChange,
  lockAudience,
}: {
  placement: RowPlacement;
  curated: boolean;
  viewers: Viewer[];
  onChange: (p: RowPlacement) => void;
  lockAudience?: boolean;
}) {
  const [more, setMore] = useState(
    Boolean(placement.notAbove || placement.notBelow || placement.daysPerWeek || placement.priority),
  );
  const modes: { value: PlacementMode; label: string }[] = curated
    ? [
        { value: "show", label: t("On the page") },
        { value: "hide", label: t("Hide") },
      ]
    : [
        { value: "pinned", label: t("Pinned") },
        { value: "show", label: t("Show") },
        { value: "auto", label: t("Auto") },
        { value: "hide", label: t("Hide") },
      ];
  const audience = placement.audience?.mode ?? "everyone";
  const set = (p: Partial<RowPlacement>) => onChange({ ...placement, ...p });
  return (
    <Section title={t("Placement")} hint={t("Where, when and for whom.")}>
      <Line
        label={t("On this page")}
        hint={
          curated
            ? t("Curated: the page shows its rows in your order. Rows with nothing to show are skipped.")
            : t("Pinned: always at this position. Show: always on the page, Ether picks where. Auto: Ether decides by what each viewer plays.")
        }
      >
        <Segmented
          ariaLabel={t("Placement")}
          size="sm"
          value={placement.mode}
          onChange={(m) => onChange(withMode(placement, m))}
          options={modes}
        />
      </Line>
      {placement.mode === "pinned" ? (
        <Line label={t("Position")}>
          <NumberField
            label={t("Position")}
            value={placement.position}
            onChange={(n) => set({ position: n ?? 1 })}
          />
        </Line>
      ) : null}
      {placement.mode !== "hide" ? (
        <>
          <ScheduleEditor when={placement.when} onChange={(when) => set({ when })} />
          <div className="space-y-2 border-t border-border-subtle pt-3">
            <span className="font-medium text-fg">{t("Devices")}</span>
            <div className="flex flex-wrap items-center gap-2">
              <button
                type="button"
                aria-pressed={!placement.devices?.length}
                onClick={() => set({ devices: undefined })}
                className={cn(
                  "rounded-full border px-2.5 py-0.5 text-xs",
                  !placement.devices?.length
                    ? "border-primary bg-primary text-primary-fg"
                    : "border-border hover:border-primary",
                )}
              >
                {t("All devices")}
              </button>
              <ChipGroup
                ariaLabel={t("Devices")}
                size="sm"
                value={placement.devices ?? []}
                onChange={(v) => set({ devices: v.length ? v : undefined })}
                options={DEVICES.map((d) => ({ value: d, label: deviceLabel(d) }))}
              />
            </div>
          </div>
          <Line
            label={t("Who sees it")}
            hint={lockAudience ? t("A row you add while editing for one viewer is just for them.") : undefined}
          >
            <Segmented
              ariaLabel={t("Who sees it")}
              size="sm"
              value={audience}
              onChange={(m) =>
                set({
                  audience:
                    m === "everyone"
                      ? { mode: "everyone" }
                      : { mode: m, viewers: placement.audience?.viewers ?? [] },
                })
              }
              options={[
                { value: "everyone", label: t("Everyone"), disabled: lockAudience },
                { value: "only", label: t("Only…"), disabled: lockAudience },
                { value: "except", label: t("Everyone except…"), disabled: lockAudience },
              ]}
            />
          </Line>
          {audience !== "everyone" ? (
            <ChipGroup
              ariaLabel={t("Viewers")}
              size="sm"
              value={placement.audience?.viewers ?? []}
              onChange={(v) => !lockAudience && set({ audience: { mode: audience, viewers: v } })}
              options={viewers.map((v) => ({ value: v.id, label: viewerName(v) }))}
            />
          ) : null}
        </>
      ) : null}
      {placement.mode === "auto" ? (
        <>
          <button
            type="button"
            aria-expanded={more}
            onClick={() => setMore(!more)}
            className="flex w-full items-center justify-between border-t border-border-subtle pt-3 text-left text-fg-muted"
          >
            {t("More options: never above or below, days a week, priority")}
            {more ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
          </button>
          {more ? (
            <>
              <Line label={t("Never above #")}>
                <NumberField
                  label={t("Never above #")}
                  value={placement.notAbove || undefined}
                  onChange={(n) => set({ notAbove: n })}
                />
              </Line>
              <Line label={t("Never below #")}>
                <NumberField
                  label={t("Never below #")}
                  value={placement.notBelow || undefined}
                  onChange={(n) => set({ notBelow: n })}
                />
              </Line>
              <Line label={t("At most this many days a week")}>
                <NumberField
                  label={t("At most this many days a week")}
                  value={placement.daysPerWeek || undefined}
                  max={6}
                  onChange={(n) => set({ daysPerWeek: n })}
                />
              </Line>
              <Line label={t("Priority")}>
                <Segmented
                  ariaLabel={t("Priority")}
                  size="sm"
                  value={String(placement.priority ?? 0)}
                  onChange={(v) => set({ priority: Number(v) || undefined })}
                  options={[
                    { value: "-1", label: t("Lower") },
                    { value: "0", label: t("Normal") },
                    { value: "1", label: t("Higher") },
                  ]}
                />
              </Line>
            </>
          ) : null}
        </>
      ) : null}
    </Section>
  );
}

// ---------------------------------------------------------------- content tab

function ContentTab({ ctx, row, source }: { ctx: DrawerContext; row: PageRow; source?: PageSource }) {
  const [result, setResult] = useState<{ rows: PreviewRow[]; total: number } | null>(null);
  const [error, setError] = useState<unknown>(null);
  const key = JSON.stringify({ row, p: ctx.preview });
  useEffect(() => {
    const ctl = new AbortController();
    setError(null);
    api
      .previewPageRow(ctx.pageKey, { row, ...ctx.preview, count: 40 }, ctl.signal)
      .then((r) => setResult({ rows: r.rows ?? [], total: r.total }))
      .catch((e) => {
        if (!isAbort(e)) setError(e);
      });
    return () => ctl.abort();
    // The key captures the row and the preview context.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, ctx.pageKey]);
  if (error) return <p className="text-sm text-danger">{errorMessage(error)}</p>;
  if (!result) return <Spinner size={18} label={t("Loading the titles")} />;
  return (
    <div className="space-y-4">
      <p className="text-sm text-fg-muted">
        {tn(result.total, "{count} title for this viewer right now.", "{count} titles for this viewer right now.")}
      </p>
      {result.rows.map((r) => (
        <section key={r.hubId} className="space-y-2" aria-label={r.title}>
          <h4 className="text-sm font-semibold text-fg">{r.title}</h4>
          <div className="flex flex-wrap gap-2">
            <RowBody row={r} showRating={false} pluginName={source?.plugin} />
          </div>
        </section>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------- the drawer

export function RowDrawer({
  state,
  row,
  source,
  isNew,
  ctx,
  onClose,
  onPick,
  onChange,
  onAdd,
  onRemove,
  onChangeSource,
  onTab,
}: {
  state: DrawerState | null;
  /** The row being set up or edited (Everyone's, or the viewer's effective row). */
  row: PageRow | null;
  source: PageSource | undefined;
  isNew: boolean;
  ctx: DrawerContext;
  onClose: () => void;
  onPick: (s: PageSource, params: Record<string, string>) => void;
  onChange: (r: PageRow) => void;
  onAdd: () => void;
  onRemove: () => void;
  onChangeSource: () => void;
  onTab: (tab: "settings" | "content") => void;
}) {
  const adding = state?.kind === "add";
  const picking = adding && (state.picking || !row);
  const viewerOnly = Boolean(ctx.editingFor) && !isNew;
  const experience: Experience =
    ctx.library?.experience ??
    ctx.libraries.find((l) => l.id === row?.scope.libraries?.[0])?.experience ??
    "watch";
  const libraryNames = (row?.scope.libraries ?? []).map(
    (id) => ctx.libraries.find((l) => l.id === id)?.name ?? "",
  );
  const sourceTitle = source?.title ?? row?.source.kind ?? "";
  const title = adding
    ? source && !picking
      ? t("New row at #{at} · {source}", { at: state.at, source: sourceTitle })
      : t("New row at #{at}", { at: state.at })
    : row?.name
      ? fillLibrary(row.name, libraryNames[0] ?? "")
      : sourceTitle;
  const facetLibrary =
    ctx.library?.id ?? row?.scope.libraries?.[0] ?? source?.libraries?.[0] ?? null;
  const tab = state?.kind === "edit" ? (state.tab ?? "settings") : "settings";
  const fanOut = row?.scope.perLibrary
    ? row.scope.libraries?.length
      ? row.scope.libraries.slice(0, MAX_ROWS_PER_LIBRARY_ROW)
      : (source?.libraries ?? []).slice(0, MAX_ROWS_PER_LIBRARY_ROW)
    : [];
  const addSummary =
    row && source && fanOut.length > 1
      ? tn(fanOut.length, "Adds {count} row at #{at}", "Adds {count} rows at #{at}: {names}", {
          at: adding ? state.at : 0,
          names: fanOut
            .map((id) =>
              fillLibrary(
                row.name || `${source.title} {library}`,
                ctx.libraries.find((l) => l.id === id)?.name ?? "",
              ),
            )
            .join(", "),
        })
      : t("Pick what fills the row");
  const setParams = (k: string, v: string) => {
    if (!row) return;
    const params = { ...(row.source.params ?? {}), [k]: v };
    if (!v) delete params[k];
    onChange({ ...row, source: { ...row.source, params } });
  };
  const visibleParams = (source?.params ?? []).filter(
    (p) => !(p.type === "ref" && p.required && source?.refs?.length && row?.source.params?.[p.key]),
  );
  return (
    <Drawer
      open={state !== null}
      onClose={onClose}
      title={title}
      description={
        adding
          ? t("It goes into the draft at this position and shows in the page as you set it up.")
          : viewerOnly
            ? t("Changes here are just for {viewer}: only the look and placement change.", {
                viewer: viewerName(ctx.editingFor!),
              })
            : t("Edits go straight into the draft and show in the page. Save the page when you're done.")
      }
      size="md"
      footer={
        adding ? (
          <div className="flex w-full flex-wrap items-center justify-between gap-2">
            <span className="text-sm text-fg-muted">
              {row && source ? (fanOut.length > 1 ? addSummary : t("Adds one row at #{at}", { at: state.at })) : addSummary}
            </span>
            <div className="flex gap-2">
              <Button variant="ghost" onClick={onClose}>
                {t("Cancel")}
              </Button>
              <Button disabled={!row || picking} onClick={onAdd}>
                {t("Add to page")}
              </Button>
            </div>
          </div>
        ) : (
          <div className="flex w-full items-center justify-between">
            {viewerOnly ? (
              <span />
            ) : (
              <Button variant="ghost" className="text-destructive" onClick={onRemove}>
                {t("Remove from page")}
              </Button>
            )}
            <Button onClick={onClose}>{t("Close")}</Button>
          </div>
        )
      }
    >
      {picking ? (
        <SourcePicker sources={ctx.sources} error={ctx.sourcesError} onPick={onPick} />
      ) : row ? (
        <>
          {adding ? null : (
            <SubtabNav
              ariaLabel={t("Row")}
              active={tab}
              onSelect={(k) => onTab(k as "settings" | "content")}
              tabs={[
                { key: "settings", label: t("Settings") },
                { key: "content", label: t("Content") },
              ]}
            />
          )}
          {tab === "content" && !adding ? (
            <ContentTab ctx={ctx} row={row} source={source} />
          ) : (
            <div className="space-y-5">
              <Section title={t("What fills it")}>
                <Line
                  label={
                    <span className="inline-flex items-center gap-2">
                      {sourceTitle}
                      {source?.plugin ? (
                        <Badge variant="outline" className="gap-1 font-normal">
                          <Puzzle size={11} /> {source.plugin}
                        </Badge>
                      ) : null}
                    </span>
                  }
                  hint={source?.description}
                >
                  <span className="flex items-center gap-2">
                    {source ? <KindChip personal={source.personal} /> : null}
                    {viewerOnly ? null : (
                      <Button
                        variant="ghost"
                        size="sm"
                        className="h-7 px-2 text-xs"
                        onClick={onChangeSource}
                      >
                        {t("Change")}
                      </Button>
                    )}
                    {source && source.group === "plugins" && !source.kind.startsWith("surface") && !viewerOnly ? (
                      <Link
                        to={pluginCollectionHref(source.kind, source.title)}
                        className="text-xs font-medium text-primary hover:underline"
                      >
                        {t("Save as a collection")}
                      </Link>
                    ) : null}
                  </span>
                </Line>
                {source
                  ? visibleParams
                      .filter((p) => !p.more)
                      .map((p) => (
                        <ParamControl
                          key={p.key}
                          param={p}
                          source={source}
                          value={row.source.params?.[p.key] ?? ""}
                          library={facetLibrary}
                          disabled={viewerOnly}
                          onChange={(v) => setParams(p.key, v)}
                        />
                      ))
                  : null}
                {source && visibleParams.some((p) => p.more) ? (
                  <details className="border-t border-border-subtle pt-3">
                    <summary className="cursor-pointer text-fg-muted">{t("More options")}</summary>
                    <div className="mt-3 space-y-3">
                      {visibleParams
                        .filter((p) => p.more)
                        .map((p) => (
                          <ParamControl
                            key={p.key}
                            param={p}
                            source={source}
                            value={row.source.params?.[p.key] ?? ""}
                            library={facetLibrary}
                            disabled={viewerOnly}
                            onChange={(v) => setParams(p.key, v)}
                          />
                        ))}
                    </div>
                  </details>
                ) : null}
                <Line
                  label={t("Name")}
                  hint={
                    row.scope.perLibrary
                      ? t("{library} becomes each library's name.")
                      : t("Leave it empty to use the source's name.")
                  }
                >
                  <Input
                    aria-label={t("Name")}
                    className="h-8 w-56"
                    maxLength={80}
                    disabled={viewerOnly}
                    placeholder={row.scope.perLibrary ? `${sourceTitle} {library}` : sourceTitle}
                    value={row.name ?? ""}
                    onChange={(e) => onChange({ ...row, name: e.currentTarget.value || undefined })}
                  />
                </Line>
              </Section>
              {ctx.library === null && source ? (
                <LibrariesSection
                  row={row}
                  source={source}
                  libraries={ctx.libraries}
                  onChange={onChange}
                  disabled={viewerOnly}
                />
              ) : null}
              {source ? (
                <LookSection
                  look={row.look}
                  source={source}
                  experience={experience}
                  onChange={(look) => onChange({ ...row, look })}
                />
              ) : null}
              <PlacementSection
                placement={row.placement}
                curated={ctx.curated}
                viewers={ctx.viewers}
                lockAudience={Boolean(ctx.editingFor) && isNew}
                onChange={(placement) => onChange({ ...row, placement })}
              />
            </div>
          )}
        </>
      ) : (
        <p className="text-sm text-fg-muted">{t("This row is no longer on the page.")}</p>
      )}
    </Drawer>
  );
}
