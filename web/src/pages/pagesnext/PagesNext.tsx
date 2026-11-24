// Mock-only prototype of the Rows & Pages designer, built from the real console
// components for the owner's design review (rows-redesign/design.md). The route
// exists only in `vite --mode mock`. Screens are chosen by query parameters so
// screenshots are deterministic: ?page=&drawer=add|row&as=luca&editing=luca.
import { Badge, Button, Input, Switch, cn } from "@braedonsaunders/appkit-ui";
import {
  AlertTriangle,
  CalendarClock,
  ChevronRight,
  Eye,
  EyeOff,
  GripVertical,
  Library,
  Pin,
  Plus,
  Puzzle,
  Save,
  Search,
  ShieldCheck,
  Sparkles,
  Undo2,
  User,
  Users,
} from "lucide-react";
import { useMemo, useState, type ReactNode } from "react";
import { useSearchParams } from "react-router-dom";
import { Artwork } from "../../components/Artwork";
import { Drawer } from "../../components/Drawer";
import { ChipGroup, Segmented } from "../../components/fields";
import { SubtabNav } from "../../components/layout";
import { PosterCard } from "../../components/Poster";
import { ToolbarPicker } from "../../components/ToolbarPicker";
import { PanelHead, Workspace } from "../../components/workspace";
import type { TitleCard } from "../../lib/contract";
import {
  LIBS,
  PAGES,
  SOURCE_GROUPS,
  TRACKS,
  TRENDING_MOVIES,
  TRENDING_SHOWS,
  VIEWERS,
  homeRows,
  musicRows,
  type Placement,
  type Row,
} from "./fixtures";

const PLACEMENT_LABEL: Record<Placement, string> = {
  pinned: "Pinned",
  show: "Show",
  auto: "Auto",
  hide: "Hide",
};

function PlacementChip({ row }: { row: Row }) {
  const variant = row.placement === "pinned" ? "success" : row.placement === "show" ? "info" : "secondary";
  return (
    <Badge variant={variant} className="gap-1">
      {row.placement === "pinned" ? <Pin size={11} /> : row.placement === "auto" ? <Sparkles size={11} /> : <Eye size={11} />}
      {PLACEMENT_LABEL[row.placement]}
      {row.position ? ` #${row.position}` : ""}
    </Badge>
  );
}

function KindChip({ personal }: { personal: boolean }) {
  return personal ? (
    <Badge variant="outline" className="gap-1 border-primary/40 font-normal text-primary">
      <User size={11} /> Personal
    </Badge>
  ) : (
    <Badge variant="outline" className="gap-1 font-normal">
      <Users size={11} /> Shared
    </Badge>
  );
}

// ---------------------------------------------------------------- row list (left)

function RowListItem({ row, onOpen, curated, index }: { row: Row; onOpen: () => void; curated: boolean; index: number }) {
  return (
    <button
      type="button"
      onClick={onOpen}
      className="group flex w-full items-start gap-2 rounded-lg border border-border bg-surface px-2.5 py-2 text-left transition hover:border-border-strong"
    >
      <GripVertical size={14} className="mt-1 shrink-0 text-fg-subtle" />
      <span className="mt-0.5 grid size-5 shrink-0 place-items-center rounded bg-bg-subtle text-[11px] font-semibold tabular-nums text-fg-muted">
        {index}
      </span>
      <span className="min-w-0 flex-1">
        <span className="flex flex-wrap items-center gap-1.5">
          <span className="truncate text-sm font-medium text-fg">{row.group ?? row.title}</span>
          {curated ? null : <PlacementChip row={row} />}
          <KindChip personal={row.personal} />
        </span>
        <span className="mt-0.5 block truncate text-xs text-fg-muted">
          {row.source}
          {row.libraries !== "—" && row.libraries !== "This library" ? ` · ${row.libraries}` : ""}
        </span>
        {row.when || row.audience || row.plugin ? (
          <span className="mt-1 flex flex-wrap gap-1">
            {row.when ? (
              <Badge variant="outline" className="gap-1 font-normal">
                <CalendarClock size={11} /> {row.when}
              </Badge>
            ) : null}
            {row.audience ? (
              <Badge variant="outline" className="gap-1 font-normal">
                <Users size={11} /> {row.audience}
              </Badge>
            ) : null}
            {row.layout === "surface" ? (
              <Badge variant="outline" className="gap-1 font-normal">
                <Puzzle size={11} /> Only in Vela
              </Badge>
            ) : null}
          </span>
        ) : null}
      </span>
      <ChevronRight size={14} className="mt-1 shrink-0 text-fg-subtle opacity-0 group-hover:opacity-100" />
    </button>
  );
}

function RowList({
  rows,
  curated,
  onOpen,
  onAdd,
}: {
  rows: Row[];
  curated: boolean;
  onOpen: (id: string) => void;
  onAdd: () => void;
}) {
  // Fan-out rows collapse to one list entry (the row the owner made).
  const entries = rows.filter((r, i) => !r.group || rows.findIndex((x) => x.group === r.group) === i);
  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between gap-2">
        <div>
          <h2 className="text-xs font-semibold uppercase tracking-wider text-fg-subtle">On this page</h2>
          <p className="text-xs text-fg-muted">
            {curated ? "Exactly these rows, in this order. Drag to reorder." : "Your rows. Ether fills the rest up to the page length."}
          </p>
        </div>
        <Button size="sm" onClick={onAdd}>
          <Plus size={14} /> Add row
        </Button>
      </div>
      <div className="flex items-center gap-2">
        <div className="relative flex-1">
          <Search size={14} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-fg-subtle" />
          <Input className="h-8 pl-8 text-sm" placeholder="Filter rows…" />
        </div>
        <Segmented
          ariaLabel="Show"
          size="sm"
          value="all"
          onChange={() => {}}
          options={[
            { value: "all", label: "All" },
            { value: "changed", label: "Changed" },
          ]}
        />
      </div>
      <div className="space-y-1.5">
        {entries.map((r, i) => (
          <RowListItem key={r.id} row={r} index={i + 1} curated={curated} onOpen={() => onOpen(r.id)} />
        ))}
      </div>
      {curated ? null : (
        <>
          <button
            type="button"
            className="flex w-full items-center justify-between rounded-lg border border-dashed border-border px-3 py-2 text-left text-sm text-fg-muted hover:text-fg"
          >
            <span>
              <Sparkles size={13} className="mr-1.5 inline" />
              Ether may also choose from <b className="text-fg">24 more rows</b>
            </span>
            <ChevronRight size={14} />
          </button>
          <button
            type="button"
            className="flex w-full items-center justify-between rounded-lg border border-dashed border-border px-3 py-2 text-left text-sm text-fg-muted hover:text-fg"
          >
            <span>
              <EyeOff size={13} className="mr-1.5 inline" />
              Hidden on this page: <b className="text-fg">2 rows</b>
            </span>
            <ChevronRight size={14} />
          </button>
        </>
      )}
    </div>
  );
}

// ---------------------------------------------------------------- preview (right)

function SquareCard({ item, round }: { item: TitleCard; round?: boolean }) {
  return (
    <div className="w-28 shrink-0 sm:w-32">
      <div className={cn("aspect-square overflow-hidden shadow-sm ring-1 ring-border-subtle", round ? "rounded-full" : "rounded-md")}>
        <Artwork src={item.poster} alt={item.title} title={item.title} className="size-full" />
      </div>
      <p className={cn("mt-1.5 truncate text-xs font-medium text-fg", round && "text-center")}>{item.title}</p>
      {round ? null : <p className="truncate text-[11px] text-fg-muted">{item.year ?? "Album"}</p>}
    </div>
  );
}

function WideCard({ item, hero }: { item: TitleCard; hero?: boolean }) {
  return (
    <div className={cn("shrink-0", hero ? "w-80 sm:w-[26rem]" : "w-56 sm:w-64")}>
      <div className="relative aspect-video overflow-hidden rounded-lg shadow-sm ring-1 ring-border-subtle">
        <Artwork src={item.backdrop ?? item.poster} alt={item.title} title={item.title} className="size-full" />
        {hero ? (
          <div className="absolute right-2 top-2 rounded bg-overlay/70 px-2 py-0.5">
            <p className="text-xs font-medium text-on-scrim">Trending this week · {item.year}</p>
          </div>
        ) : null}
      </div>
      {hero ? null : <p className="mt-1.5 truncate text-xs font-medium text-fg">{item.title}</p>}
    </div>
  );
}

function RatedPoster({ item, showRating }: { item: TitleCard; showRating: boolean }) {
  return (
    <div className="w-28 shrink-0 sm:w-32">
      <PosterCard
        item={item}
        badge={
          showRating && item.contentRating ? (
            <span className="rounded bg-overlay/80 px-1 py-0.5 text-[10px] font-semibold text-on-scrim">{item.contentRating}</span>
          ) : undefined
        }
      />
    </div>
  );
}

function RowBody({ row, showRating }: { row: Row; showRating: boolean }) {
  if (row.layout === "surface") {
    return (
      <div className="flex items-center gap-3 rounded-xl border border-dashed border-border bg-bg-subtle/60 px-4 py-5 text-sm text-fg-muted">
        <Puzzle size={18} />
        <span>
          Drawn by <b className="text-fg">{row.plugin}</b> in Vela: tonight's live and upcoming games. Hidden by Vela on days with nothing on. Not counted toward the page length.
        </span>
      </div>
    );
  }
  if (row.layout === "tracks") {
    return (
      <div className="overflow-hidden rounded-xl border border-border">
        {TRACKS.map(([t, a, al, d], i) => (
          <div key={t} className="flex items-center gap-3 border-b border-border-subtle px-3 py-2 text-sm last:border-0">
            <span className="w-4 text-right tabular-nums text-fg-subtle">{i + 1}</span>
            <span className="min-w-0 flex-1 truncate font-medium text-fg">{t}</span>
            <span className="hidden w-32 truncate text-fg-muted sm:block">{a}</span>
            <span className="hidden w-32 truncate text-fg-muted md:block">{al}</span>
            <span className="tabular-nums text-fg-muted">{d}</span>
          </div>
        ))}
      </div>
    );
  }
  if (row.layout === "tiles") {
    return (
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
        {row.items.map((it) => (
          <div key={it.key} className="flex items-center gap-2 overflow-hidden rounded-md bg-surface ring-1 ring-border-subtle">
            <Artwork src={it.poster} alt={it.title} title={it.title} className="size-11 shrink-0" />
            <span className="truncate text-xs font-semibold text-fg">{it.title}</span>
          </div>
        ))}
      </div>
    );
  }
  return (
    <div className="shelf flex gap-3 overflow-x-auto pb-1">
      {row.items.map((it) =>
        row.layout === "hero" ? (
          <WideCard key={it.key} item={it} hero />
        ) : row.layout === "wide" ? (
          <WideCard key={it.key} item={it} />
        ) : row.card === "square" || row.card === "round" ? (
          <SquareCard key={it.key} item={it} round={row.card === "round"} />
        ) : (
          <RatedPoster key={it.key} item={it} showRating={showRating} />
        ),
      )}
    </div>
  );
}

function PreviewRow({ row, index, curated, showRating, onEdit }: { row: Row; index: number; curated: boolean; showRating: boolean; onEdit: () => void }) {
  return (
    <section className="group/row rounded-xl px-2 py-3 hover:bg-surface/60">
      <div className="mb-1.5 flex flex-wrap items-center gap-x-2 gap-y-1">
        <GripVertical size={14} className="text-fg-subtle" />
        <span className={cn("grid size-6 place-items-center rounded-md text-xs font-semibold tabular-nums", row.placement === "pinned" ? "bg-success-subtle text-success" : "bg-bg-subtle text-fg-muted")}>
          {index}
        </span>
        <h3 className="text-base font-semibold text-fg">{row.title}</h3>
        {curated ? null : <PlacementChip row={row} />}
        <KindChip personal={row.personal} />
        {row.audience ? (
          <Badge variant="outline" className="gap-1 font-normal">
            <Users size={11} /> {row.audience}
          </Badge>
        ) : null}
        {row.when ? (
          <Badge variant="outline" className="gap-1 font-normal">
            <CalendarClock size={11} /> {row.when}
          </Badge>
        ) : null}
        <div className="ml-auto flex gap-0.5 opacity-0 group-hover/row:opacity-100">
          <Button variant="ghost" size="sm" className="h-7 gap-1 px-2 text-xs">
            <Pin size={13} /> Pin here
          </Button>
          <Button variant="ghost" size="sm" className="h-7 gap-1 px-2 text-xs">
            <EyeOff size={13} /> Hide
          </Button>
          <Button variant="ghost" size="sm" className="h-7 gap-1 px-2 text-xs" onClick={onEdit}>
            Edit
          </Button>
        </div>
      </div>
      <p className="mb-2 max-w-3xl text-xs text-fg-muted">
        <span className="font-medium text-fg">Why here: </span>
        {row.why}
        {row.note ? <span className="ml-2 text-fg-subtle">· {row.note}</span> : null}
      </p>
      {row.flag ? (
        <p className="mb-2 inline-flex items-center gap-1.5 rounded-md bg-warning-subtle px-2 py-1 text-xs text-warning">
          <AlertTriangle size={12} /> {row.flag}
        </p>
      ) : null}
      <RowBody row={row} showRating={showRating} />
    </section>
  );
}

function AccessStrip() {
  return (
    <div className="mb-4 flex flex-wrap items-center gap-x-4 gap-y-2 rounded-xl border border-primary/30 bg-primary-subtle/40 px-4 py-3 text-sm">
      <span className="inline-flex items-center gap-1.5 font-semibold text-fg">
        <ShieldCheck size={16} className="text-primary" /> Luca · age limit PG
      </span>
      <span className="text-fg-muted">Unrated titles hidden</span>
      <span className="text-fg-muted">Sees 4 of 6 libraries (not Anime Shows, Sports Replays)</span>
      <span className="text-fg-muted">Every title below is within PG. 2 rows need a look ↓</span>
      <a className="ml-auto text-sm font-medium text-primary hover:underline" href="#viewer">
        Change in Viewers ›
      </a>
    </div>
  );
}

function NotShown({ kid }: { kid: boolean }) {
  const items = kid
    ? [
        ["Horror Night", "Nothing within Luca's PG limit"],
        ["Trending Anime Shows", "Luca can't see Anime Shows"],
        ["Christmas Movies", "Shows Dec 1–26 (Christmas season)"],
      ]
    : [
        ["Christmas Movies", "Shows Dec 1–26 (Christmas season). Preview at Dec 20 ›"],
        ["Top 10 today", "Ether left it out: the page is full (9 of 9)"],
      ];
  return (
    <section className="mt-4 rounded-xl border border-dashed border-border p-3">
      <h3 className="mb-2 text-xs font-semibold uppercase tracking-wider text-fg-subtle">Not shown right now</h3>
      <ul className="space-y-1.5 text-sm">
        {items.map(([t, why]) => (
          <li key={t} className="flex flex-wrap gap-x-2">
            <span className="font-medium text-fg">{t}</span>
            <span className="text-fg-muted">{why}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}

// ---------------------------------------------------------------- drawers

function Section({ title, hint, children }: { title: string; hint?: string; children: ReactNode }) {
  return (
    <section className="space-y-2">
      <div>
        <h3 className="text-xs font-semibold uppercase tracking-wider text-fg-subtle">{title}</h3>
        {hint ? <p className="mt-0.5 text-sm text-fg-muted">{hint}</p> : null}
      </div>
      <div className="space-y-3.5 rounded-xl border border-border bg-surface p-3.5 text-sm">{children}</div>
    </section>
  );
}

function Line({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div className="space-y-1 border-t border-border-subtle pt-3 first:border-0 first:pt-0">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <span className="font-medium text-fg">{label}</span>
        {children}
      </div>
      {hint ? <p className="text-fg-muted">{hint}</p> : null}
    </div>
  );
}

function LibraryScope({ perLibrary, onPerLibrary }: { perLibrary: boolean; onPerLibrary: (v: boolean) => void }) {
  return (
    <Section title="Libraries" hint="Where the titles come from.">
      <Line label="Draw from">
        <Segmented
          ariaLabel="Libraries"
          size="sm"
          value="these"
          onChange={() => {}}
          options={[
            { value: "any", label: "Any library you can watch" },
            { value: "these", label: "These libraries" },
          ]}
        />
      </Line>
      <ChipGroup
        ariaLabel="Libraries"
        size="sm"
        value={[1, 2]}
        onChange={() => {}}
        options={LIBS.filter((l) => l.experience === "watch" && !l.plugin).map((l) => ({ value: l.id, label: l.name }))}
      />
      <p className="text-xs text-fg-muted">Music, Audiobooks and Sports Replays don't have TMDB trending titles, so Trending can't use them.</p>
      <Line
        label="Show as"
        hint={perLibrary ? "Two rows, kept together: Trending Movies, then Trending TV Shows. Up to 6 libraries." : "One row mixing both libraries."}
      >
        <Segmented
          ariaLabel="Show as"
          size="sm"
          value={perLibrary ? "per" : "one"}
          onChange={(v) => onPerLibrary(v === "per")}
          options={[
            { value: "one", label: "One combined row" },
            { value: "per", label: "One row per library" },
          ]}
        />
      </Line>
    </Section>
  );
}

function LookSection({ style, onStyle }: { style: string; onStyle: (v: string) => void }) {
  return (
    <Section title="Look" hint="Changes show in the preview as you make them.">
      <Line label="Style" hint="Plex, Vela and Silo show Hero and Wide. Jellyfin and Emby keep their own look.">
        <Segmented
          ariaLabel="Style"
          size="sm"
          value={style}
          onChange={onStyle}
          options={[
            { value: "standard", label: "Standard" },
            { value: "hero", label: "Hero" },
            { value: "wide", label: "Wide" },
          ]}
        />
      </Line>
      <Line label="Cards">
        <Segmented
          ariaLabel="Cards"
          size="sm"
          value="library"
          onChange={() => {}}
          options={[
            { value: "library", label: "Follow the library" },
            { value: "poster", label: "Poster" },
            { value: "landscape", label: "Landscape" },
          ]}
        />
      </Line>
      <Line label="Hide watched" hint="Leave out titles the viewer has finished.">
        <Switch aria-label="Hide watched" checked onChange={() => {}} />
      </Line>
      <Line label="Numbered" hint="Number the first ten, Top 10 style.">
        <Switch aria-label="Numbered" checked={false} onChange={() => {}} />
      </Line>
      <Line label="Titles other rows show">
        <Segmented
          ariaLabel="Repeats"
          size="sm"
          value="skip"
          onChange={() => {}}
          options={[
            { value: "skip", label: "Skip them" },
            { value: "keep", label: "Keep this row whole" },
          ]}
        />
      </Line>
    </Section>
  );
}

function PlacementSection() {
  return (
    <Section title="Placement" hint="Where, when and for whom.">
      <Line label="On this page" hint="Pinned: always at this position. Show: always on the page, Ether picks where. Auto: Ether decides by what each viewer plays.">
        <Segmented
          ariaLabel="Placement"
          size="sm"
          value="pinned"
          onChange={() => {}}
          options={[
            { value: "pinned", label: "Pinned" },
            { value: "show", label: "Show" },
            { value: "auto", label: "Auto" },
            { value: "hide", label: "Hide" },
          ]}
        />
      </Line>
      <Line label="Position">
        <Input className="h-8 w-20" defaultValue="3" />
      </Line>
      <div className="space-y-2 border-t border-border-subtle pt-3">
        <span className="font-medium text-fg">When</span>
        <ChipGroup
          ariaLabel="When"
          size="sm"
          value={["any"]}
          onChange={() => {}}
          options={[
            { value: "any", label: "Any time" },
            { value: "evenings", label: "Evenings" },
            { value: "weeknights", label: "Weeknights" },
            { value: "weekends", label: "Weekends" },
            { value: "weekend-nights", label: "Weekend nights" },
            { value: "holidays", label: "Holidays…" },
            { value: "custom", label: "Custom…" },
          ]}
        />
      </div>
      <div className="space-y-2 border-t border-border-subtle pt-3">
        <span className="font-medium text-fg">Devices</span>
        <ChipGroup
          ariaLabel="Devices"
          size="sm"
          value={["all"]}
          onChange={() => {}}
          options={[
            { value: "all", label: "All devices" },
            { value: "tv", label: "TV" },
            { value: "mobile", label: "Phone & tablet" },
            { value: "desktop", label: "Computer" },
            { value: "web", label: "Web" },
          ]}
        />
      </div>
      <Line label="Who sees it">
        <Segmented
          ariaLabel="Who sees it"
          size="sm"
          value="everyone"
          onChange={() => {}}
          options={[
            { value: "everyone", label: "Everyone" },
            { value: "only", label: "Only…" },
            { value: "except", label: "Everyone except…" },
          ]}
        />
      </Line>
      <button type="button" className="flex w-full items-center justify-between border-t border-border-subtle pt-3 text-left text-fg-muted">
        More options: never above or below, days a week, priority <ChevronRight size={14} />
      </button>
    </Section>
  );
}

function MiniShelf({ title, items }: { title: string; items: TitleCard[] }) {
  return (
    <div>
      <p className="mb-1.5 text-sm font-semibold text-fg">{title}</p>
      <div className="shelf flex gap-2 overflow-x-auto pb-1">
        {items.slice(0, 7).map((it) => (
          <div key={it.key} className="w-20 shrink-0">
            <PosterCard item={it} showMeta={false} />
          </div>
        ))}
      </div>
    </div>
  );
}

function AddRowDrawer({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [perLibrary, setPerLibrary] = useState(true);
  return (
    <Drawer
      open={open}
      onClose={onClose}
      title="Add a row to Home"
      description="Pick what fills it, then where its titles come from. It lands in the draft; nothing is saved yet."
      size="xl"
      footer={
        <div className="flex w-full items-center justify-between gap-2">
          <span className="text-sm text-fg-muted">Adds 2 rows: Trending Movies, Trending TV Shows</span>
          <div className="flex gap-2">
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button>Add to Home</Button>
          </div>
        </div>
      }
    >
      <div className="grid gap-5 md:grid-cols-[minmax(0,5fr)_minmax(0,7fr)]">
        <div className="space-y-3">
          <div className="relative">
            <Search size={14} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-fg-subtle" />
            <Input className="h-9 pl-8" placeholder="Search rows, collections and plugins…" />
          </div>
          <div className="space-y-4">
            {SOURCE_GROUPS.slice(0, 6).map((g) => (
              <div key={g.name}>
                <h4 className="mb-1 text-xs font-semibold uppercase tracking-wider text-fg-subtle">{g.name}</h4>
                <div className="space-y-1">
                  {g.items.map((s) => {
                    const on = s.title === "Trending";
                    return (
                      <div
                        key={s.title}
                        className={cn(
                          "rounded-lg border px-2.5 py-1.5",
                          on ? "border-primary/50 bg-primary-subtle/50" : "border-transparent hover:border-border",
                        )}
                      >
                        <div className="flex flex-wrap items-center gap-1.5">
                          <span className="text-sm font-medium text-fg">{s.title}</span>
                          <KindChip personal={s.personal} />
                          {s.used ? <span className="text-[11px] text-fg-subtle">{s.used}</span> : null}
                        </div>
                        <p className="text-xs text-fg-muted">{s.desc}</p>
                      </div>
                    );
                  })}
                </div>
              </div>
            ))}
            <p className="text-xs text-fg-muted">More below: Your collections · From plugins (English Dubs, Sports, Cinemeta) · Live</p>
          </div>
        </div>
        <div className="space-y-5">
          <div>
            <div className="flex items-center gap-2">
              <h3 className="text-lg font-semibold text-fg">Trending</h3>
              <KindChip personal={false} />
            </div>
            <p className="text-sm text-fg-muted">What's trending on TMDB. The same list for everyone; titles you don't have yet are left out.</p>
          </div>
          <Section title="Options">
            <Line label="Window">
              <Segmented
                ariaLabel="Window"
                size="sm"
                value="week"
                onChange={() => {}}
                options={[
                  { value: "day", label: "Today" },
                  { value: "week", label: "This week" },
                ]}
              />
            </Line>
          </Section>
          <LibraryScope perLibrary={perLibrary} onPerLibrary={setPerLibrary} />
          <section className="space-y-3">
            <h3 className="text-xs font-semibold uppercase tracking-wider text-fg-subtle">Preview</h3>
            {perLibrary ? (
              <>
                <MiniShelf title="Trending Movies" items={TRENDING_MOVIES} />
                <MiniShelf title="Trending TV Shows" items={TRENDING_SHOWS} />
              </>
            ) : (
              <MiniShelf title="Trending" items={[...TRENDING_MOVIES.slice(0, 4), ...TRENDING_SHOWS.slice(0, 4)]} />
            )}
          </section>
        </div>
      </div>
    </Drawer>
  );
}

function RowDrawer({ open, onClose, row }: { open: boolean; onClose: () => void; row?: Row }) {
  const [tab, setTab] = useState("settings");
  const [style, setStyle] = useState(row?.layout === "hero" ? "hero" : "standard");
  const [perLibrary, setPerLibrary] = useState(true);
  return (
    <Drawer
      open={open}
      onClose={onClose}
      title={row?.group ?? row?.title ?? ""}
      description="Edits go straight into the draft and show in the preview. Save the page when you're done."
      size="lg"
      footer={
        <div className="flex w-full items-center justify-between">
          <Button variant="ghost" className="text-destructive">
            Remove from page
          </Button>
          <Button onClick={onClose}>Close</Button>
        </div>
      }
    >
      <SubtabNav
        ariaLabel="Row"
        active={tab}
        onSelect={setTab}
        tabs={[
          { key: "settings", label: "Settings" },
          { key: "content", label: "Content", count: 24 },
        ]}
      />
      <div className="space-y-5">
        <Section title="Source">
          <Line label="Trending" hint="What's trending on TMDB. Same list for everyone.">
            <KindChip personal={false} />
          </Line>
          <Line label="Window">
            <Segmented
              ariaLabel="Window"
              size="sm"
              value="week"
              onChange={() => {}}
              options={[
                { value: "day", label: "Today" },
                { value: "week", label: "This week" },
              ]}
            />
          </Line>
          <Line label="Name" hint="Use {library} for the library name in each row.">
            <Input className="h-8 w-56" defaultValue="Trending {library}" />
          </Line>
        </Section>
        <LibraryScope perLibrary={perLibrary} onPerLibrary={setPerLibrary} />
        <LookSection style={style} onStyle={setStyle} />
        <PlacementSection />
      </div>
    </Drawer>
  );
}

// ---------------------------------------------------------------- page

export default function PagesNext() {
  const [params, setParams] = useSearchParams();
  const page = params.get("page") ?? "home";
  const drawer = params.get("drawer");
  const editing = params.get("editing") ?? "everyone";
  const as = params.get("as") ?? (editing !== "everyone" ? editing : "typical");
  const kid = as === "luca";
  const isMusic = page.startsWith("library:5");
  const pageInfo = PAGES.find((p) => p.key === page) ?? PAGES[0]!;
  const curated = pageInfo.mode === "curated";
  const rows = useMemo(() => (isMusic ? musicRows() : homeRows(kid)), [isMusic, kid]);
  const visible = kid ? rows.filter((r) => r.id !== "r7") : rows;
  const [tab, setTab] = useState(params.get("tab") ?? "rows");
  const set = (k: string, v: string | null) => {
    const next = new URLSearchParams(params);
    if (v === null) next.delete(k);
    else next.set(k, v);
    setParams(next, { replace: true });
  };
  const editRow = rows.find((r) => r.id === (params.get("row") ?? "r3"));
  const editingViewer = editing !== "everyone";

  const pagePicker = (
    <ToolbarPicker
      ariaLabel="Page"
      value={page}
      onChange={(v) => set("page", v)}
      searchable
      matchWidth
      className="w-full justify-start text-left"
      options={PAGES.map((p) => ({
        value: p.key,
        label: p.label,
        group: p.group,
        icon: p.key === "home" ? <Sparkles size={14} /> : <Library size={14} />,
        hint: `${p.rows} rows · ${p.mode === "curated" ? "Curated" : "Personalized"}`,
      }))}
    />
  );

  const actions = (
    <div className="flex flex-wrap items-center gap-2">
      <Badge variant="warning">Unsaved changes · kept if you leave</Badge>
      <Button variant="outline" size="sm">
        <Undo2 size={14} /> Discard
      </Button>
      <Button size="sm">
        <Save size={14} /> Save page
      </Button>
    </div>
  );

  return (
    <>
      <Workspace
        title="Pages"
        subtitle="Home and every library's rows, in one place"
        actions={actions}
        asideLabel="Page controls"
        mainLabel="Page preview"
        mainClassName="p-3 sm:p-6"
        aside={
          <>
            <PanelHead>
              <div className="flex min-w-0 flex-col gap-1">
                <span className="text-[11px] font-semibold uppercase tracking-wider text-fg-subtle">Page</span>
                {pagePicker}
              </div>
              <SubtabNav
                ariaLabel="Page tools"
                active={tab}
                onSelect={setTab}
                tabs={[
                  { key: "rows", label: "Rows" },
                  { key: "page", label: "Page" },
                  ...(curated ? [] : [{ key: "tuning", label: "Tuning" }]),
                ]}
              />
            </PanelHead>
            <div className="space-y-5 p-3 sm:p-4">
              {tab === "page" ? (
                <div className="space-y-5">
                  <Section title="Mode">
                    <Segmented
                      ariaLabel="Mode"
                      value={curated ? "curated" : "personalized"}
                      onChange={() => {}}
                      options={[
                        { value: "curated", label: "Curated" },
                        { value: "personalized", label: "Personalized" },
                      ]}
                    />
                    <p className="text-fg-muted">
                      Curated: exactly your rows, in your order. Personalized: Ether fills around your pinned and shown rows, up to the length below.
                    </p>
                  </Section>
                  <Section title="Length">
                    <Line label="Up to" hint="Pinned and Show rows always appear, even past this.">
                      <span className="flex items-center gap-2">
                        <Input className="h-8 w-16" defaultValue="9" /> rows
                      </span>
                    </Line>
                    <Line label="Let Ether also choose rows you haven't added">
                      <Switch aria-label="Suggestions" checked onChange={() => {}} />
                    </Line>
                  </Section>
                </div>
              ) : (
                <RowList rows={rows} curated={curated} onOpen={(id) => { set("row", id); set("drawer", "row"); }} onAdd={() => set("drawer", "add")} />
              )}
            </div>
          </>
        }
        main={
          <div>
            <div className="mb-3 flex flex-wrap items-center gap-2">
              <ToolbarPicker
                ariaLabel="Editing for"
                value={editing}
                onChange={(v) => set("editing", v)}
                className={cn(editingViewer && "border-primary text-primary")}
                trigger={
                  <span className="inline-flex items-center gap-1.5">
                    <span className="text-fg-muted">Editing for</span>
                    <b>{VIEWERS.find((v) => v.id === editing)?.label}</b>
                  </span>
                }
                options={VIEWERS.map((v) => ({ value: v.id, label: v.label, hint: v.hint, icon: v.id === "everyone" ? <Users size={14} /> : <User size={14} /> }))}
              />
              {editingViewer ? null : (
                <ToolbarPicker
                  ariaLabel="Preview as"
                  value={as}
                  onChange={(v) => set("as", v)}
                  trigger={
                    <span className="inline-flex items-center gap-1.5">
                      <Eye size={13} className="text-fg-muted" />
                      <span className="text-fg-muted">Preview as</span>
                      <b>{as === "typical" ? "Typical viewer" : VIEWERS.find((v) => v.id === as)?.label}</b>
                    </span>
                  }
                  options={[{ value: "typical", label: "Typical viewer" }, ...VIEWERS.slice(1).map((v) => ({ value: v.id, label: v.label, hint: v.hint }))]}
                />
              )}
              <ToolbarPicker ariaLabel="Device" value="tv" onChange={() => {}} options={[{ value: "tv", label: "TV" }, { value: "mobile", label: "Phone" }]} />
              <ToolbarPicker ariaLabel="When" value="fri" onChange={() => {}} options={[{ value: "fri", label: "Fri 28 Nov · 9 PM" }]} />
            </div>
            <p className={cn("mb-3 text-sm", editingViewer ? "font-medium text-primary" : "text-fg-muted")}>
              {editingViewer
                ? "Editing Luca's page only. 2 changes from Everyone. Rows you add here are for Luca only."
                : kid
                  ? "Editing everyone's Home · previewing as Luca. Your changes apply to everyone."
                  : isMusic
                    ? "Music › Home in Vela's Listen app. Plex and Jellyfin show these rows on Music's Recommended page."
                    : "Editing everyone's Home. Drag a row to pin it to that position."}
            </p>
            {kid ? <AccessStrip /> : null}
            <p className="mb-2 text-sm text-fg-muted">
              {curated
                ? `${visible.length} rows · in your order`
                : kid
                  ? "8 rows · 3 pinned · 3 shown · 2 chosen by Ether · 3 not shown ↓"
                  : "9 rows · 4 pinned · 3 shown · 2 chosen by Ether · 2 not shown ↓"}
            </p>
            <div className="space-y-1">
              {visible.map((r, i) => (
                <PreviewRow
                  key={r.id}
                  row={r}
                  index={i + 1}
                  curated={curated}
                  showRating={kid}
                  onEdit={() => {
                    set("row", r.id);
                    set("drawer", "row");
                  }}
                />
              ))}
            </div>
            {isMusic ? null : <NotShown kid={kid} />}
          </div>
        }
      />
      <AddRowDrawer open={drawer === "add"} onClose={() => set("drawer", null)} />
      <RowDrawer key={editRow?.id} open={drawer === "row"} onClose={() => set("drawer", null)} row={editRow} />
    </>
  );
}
