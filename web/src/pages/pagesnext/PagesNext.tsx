// Mock-only prototype of the Rows & Pages designer, built from the real console
// components for the owner's design review (rows-redesign/design.md). The route
// exists only in `vite --mode mock`. Screens are chosen by query parameters so
// screenshots are deterministic: ?page=&drawer=add&at=&source=|drawer=row&row=&as=luca&editing=luca.
// One list of rows, ever: the preview is the editor; + inserts in place; one drawer adds and edits.
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
  SlidersHorizontal,
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
      <p className="text-xs text-fg-muted">Music, Audiobooks and Sports Replays don't have TMDB titles, so this row can't draw from them.</p>
      <Line
        label="Show as"
        hint={perLibrary ? "Two rows, kept together: one for Movies, then one for TV Shows. Up to 6 libraries." : "One row mixing both libraries."}
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


// ---------------------------------------------------------------- the preview IS the row list

/** A thin gap between rows: "+" inserts a new row at exactly this position. */
function InsertGap({ at, onAdd, active }: { at: number; onAdd: (at: number) => void; active?: boolean }) {
  return (
    <div className="group/gap relative flex h-6 items-center pl-1">
      <button
        type="button"
        aria-label={`Add a row at position ${at}`}
        onClick={() => onAdd(at)}
        className={cn(
          "mr-2 inline-flex h-6 items-center gap-1 rounded-full border px-2 text-xs font-medium transition",
          active
            ? "border-primary bg-primary text-primary-fg"
            : "border-border bg-surface text-fg-muted hover:border-primary hover:text-primary",
        )}
      >
        <Plus size={12} /> {active ? `New row at #${at}` : ""}
      </button>
      <div className={cn("h-px flex-1 transition", active ? "bg-primary" : "bg-transparent group-hover/gap:bg-border-strong")} />
    </div>
  );
}

function PreviewRow({
  row,
  index,
  curated,
  showRating,
  selected,
  fresh,
  onEdit,
}: {
  row: Row;
  index: number;
  curated: boolean;
  showRating: boolean;
  selected?: boolean;
  fresh?: boolean;
  onEdit: () => void;
}) {
  return (
    <section
      className={cn(
        "group/row rounded-xl px-2 py-3 transition-colors hover:bg-surface/60",
        selected && "bg-surface/70 ring-2 ring-primary/70",
        fresh && "border border-dashed border-primary/70 bg-primary-subtle/20",
      )}
    >
      <div className="mb-1.5 flex flex-wrap items-center gap-x-2 gap-y-1">
        <span className="cursor-grab text-fg-subtle" title="Drag to move; drop to pin there">
          <GripVertical size={14} />
        </span>
        <span
          className={cn(
            "grid size-6 place-items-center rounded-md text-xs font-semibold tabular-nums",
            row.placement === "pinned" ? "bg-success-subtle text-success" : "bg-bg-subtle text-fg-muted",
          )}
        >
          {index}
        </span>
        <button type="button" onClick={onEdit} className="text-left text-base font-semibold text-fg hover:underline">
          {row.title}
        </button>
        {fresh ? <Badge variant="info">New · in the draft</Badge> : null}
        {curated ? null : <PlacementChip row={row} />}
        <KindChip personal={row.personal} />
        {row.layout === "surface" ? (
          <Badge variant="outline" className="gap-1 font-normal">
            <Puzzle size={11} /> Only in Vela
          </Badge>
        ) : null}
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
          {curated ? null : (
            <Button variant="ghost" size="sm" className="h-7 gap-1 px-2 text-xs">
              <Pin size={13} /> Pin here
            </Button>
          )}
          <Button variant="ghost" size="sm" className="h-7 gap-1 px-2 text-xs">
            <EyeOff size={13} /> Hide
          </Button>
          <Button variant="ghost" size="sm" className="h-7 gap-1 px-2 text-xs" onClick={onEdit}>
            <SlidersHorizontal size={13} /> Edit
          </Button>
        </div>
      </div>
      <p className="mb-2 max-w-3xl text-xs text-fg-muted">
        <span className="font-medium text-fg">Why here: </span>
        {row.why}
        <span className="ml-1 text-fg-subtle">· {row.source}{row.libraries !== "—" && row.libraries !== "This library" ? ` · ${row.libraries}` : ""}</span>
        {row.note ? <span className="ml-1 text-fg-subtle">· {row.note}</span> : null}
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

/** Fan-out rows stay one block: a bracket labels the row the owner made. */
function Block({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="relative rounded-2xl border border-border-subtle pl-1">
      <span className="absolute -top-2.5 left-4 bg-bg px-1.5 text-[11px] font-medium text-fg-subtle">
        One row you made · {label}
      </span>
      {children}
    </div>
  );
}

function Folded({ icon, title, hint }: { icon: ReactNode; title: ReactNode; hint: string }) {
  return (
    <button
      type="button"
      className="flex w-full items-center justify-between gap-3 rounded-xl border border-dashed border-border px-4 py-3 text-left hover:border-border-strong"
    >
      <span className="flex items-center gap-2 text-sm text-fg">
        {icon}
        <span>
          {title}
          <span className="block text-xs text-fg-muted">{hint}</span>
        </span>
      </span>
      <ChevronRight size={16} className="text-fg-subtle" />
    </button>
  );
}

function PageFooter({ kid, curated }: { kid: boolean; curated: boolean }) {
  return (
    <div className="mt-2 space-y-2">
      {curated ? null : (
        <Folded
          icon={<Sparkles size={15} className="text-primary" />}
          title={<b>Ether's picks · 24 more rows it may choose from</b>}
          hint="Rows you haven't added that fit this page. Ether uses them to fill up to the page length. Hide any you never want."
        />
      )}
      <Folded
        icon={<EyeOff size={15} className="text-fg-muted" />}
        title={<b>Not shown right now · 3</b>}
        hint={
          kid
            ? "Horror Night: nothing within Luca's PG limit · Trending Anime Shows: Luca can't see Anime Shows · Christmas Movies: shows Dec 1–26"
            : "Family Movie Night: only for Luca and Mia (preview as Luca ›) · Christmas Movies: shows Dec 1–26 (preview at Dec 20 ›) · Top 10 today: hidden on this page"
        }
      />
    </div>
  );
}

// ---------------------------------------------------------------- one drawer: add and edit

function SourcePicker({ onPick }: { onPick: (title: string) => void }) {
  return (
    <div className="space-y-4">
      <div>
        <h3 className="text-base font-semibold text-fg">What fills this row?</h3>
        <p className="text-sm text-fg-muted">Built-in rows, your collections and plugin rows that fit this page. Pick one to set it up.</p>
      </div>
      <div className="relative">
        <Search size={14} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-fg-subtle" />
        <Input className="h-9 pl-8" placeholder="Search: trending, anime, playlists, family…" />
      </div>
      <div className="grid gap-x-6 gap-y-4 sm:grid-cols-2">
        {SOURCE_GROUPS.map((g) => (
          <div key={g.name}>
            <h4 className="mb-1 text-xs font-semibold uppercase tracking-wider text-fg-subtle">{g.name}</h4>
            <div className="space-y-0.5">
              {g.items.map((s) => (
                <button
                  type="button"
                  key={s.title}
                  onClick={() => onPick(s.title)}
                  className="block w-full rounded-lg border border-transparent px-2.5 py-1.5 text-left hover:border-border hover:bg-surface"
                >
                  <span className="flex flex-wrap items-center gap-1.5">
                    <span className="text-sm font-medium text-fg">{s.title}</span>
                    <KindChip personal={s.personal} />
                    {s.only ? (
                      <Badge variant="outline" className="gap-1 font-normal">
                        <Puzzle size={11} /> {s.only}
                      </Badge>
                    ) : null}
                    {s.used ? <span className="text-[11px] text-fg-subtle">already on this page</span> : null}
                  </span>
                  <span className="block text-xs text-fg-muted">{s.desc}</span>
                </button>
              ))}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

function RowDrawer({
  open,
  onClose,
  row,
  addAt,
  source,
  onPick,
  onChangeSource,
}: {
  open: boolean;
  onClose: () => void;
  row?: Row;
  addAt?: number;
  source?: string;
  onPick: (s: string) => void;
  onChangeSource: () => void;
}) {
  const adding = addAt !== undefined;
  const topRated = source === "Top rated";
  const picking = adding && !source;
  const [style, setStyle] = useState(row?.layout === "hero" ? "hero" : "standard");
  const [perLibrary, setPerLibrary] = useState(true);
  const [tab, setTab] = useState("settings");
  const title = adding ? (source ? `New row at #${addAt} · ${source}` : `New row at #${addAt}`) : (row?.group ?? row?.title ?? "");
  return (
    <Drawer
      open={open}
      onClose={onClose}
      title={title}
      description={
        adding
          ? "It goes into the draft at this position and shows in the preview as you set it up."
          : "Edits go straight into the draft and show in the preview. Save the page when you're done."
      }
      size="md"
      footer={
        adding ? (
          <div className="flex w-full items-center justify-between gap-2">
            <span className="text-sm text-fg-muted">{source ? `Adds 2 rows at #${addAt}: ${source === "Top rated" ? "Top Rated Movies, Top Rated TV Shows" : `${source} Movies, ${source} TV Shows`}` : "Pick what fills the row"}</span>
            <div className="flex gap-2">
              <Button variant="ghost" onClick={onClose}>
                Cancel
              </Button>
              <Button disabled={!source}>Add to page</Button>
            </div>
          </div>
        ) : (
          <div className="flex w-full items-center justify-between">
            <Button variant="ghost" className="text-destructive">
              Remove from page
            </Button>
            <Button onClick={onClose}>Close</Button>
          </div>
        )
      }
    >
      {picking ? (
        <SourcePicker onPick={onPick} />
      ) : (
        <>
          {adding ? null : (
            <SubtabNav
              ariaLabel="Row"
              active={tab}
              onSelect={setTab}
              tabs={[
                { key: "settings", label: "Settings" },
                { key: "content", label: "Content", count: 24 },
              ]}
            />
          )}
          <div className="space-y-5">
            <Section title="What fills it">
              <Line
                label={topRated ? "Top rated" : "Trending"}
                hint={
                  topRated
                    ? "TMDB's top rated with enough votes to trust. The same list for everyone; titles you don't have are left out."
                    : "What's trending on TMDB. The same list for everyone; titles you don't have yet are left out."
                }
              >
                <span className="flex items-center gap-2">
                  <KindChip personal={false} />
                  <Button variant="ghost" size="sm" className="h-7 px-2 text-xs" onClick={onChangeSource}>
                    Change
                  </Button>
                </span>
              </Line>
              {topRated ? (
                <Line label="Genre">
                  <Segmented
                    ariaLabel="Genre"
                    size="sm"
                    value="any"
                    onChange={() => {}}
                    options={[
                      { value: "any", label: "Any genre" },
                      { value: "pick", label: "Pick one…" },
                    ]}
                  />
                </Line>
              ) : (
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
              )}
              <Line label="Name" hint="{library} becomes each library's name.">
                <Input className="h-8 w-48" defaultValue={topRated ? "Top Rated {library}" : "Trending {library}"} />
              </Line>
            </Section>
            <LibraryScope perLibrary={perLibrary} onPerLibrary={setPerLibrary} />
            <LookSection style={style} onStyle={setStyle} />
            <PlacementSection />
          </div>
        </>
      )}
    </Drawer>
  );
}

// ---------------------------------------------------------------- page

function Dial({ label, low, high, value }: { label: string; low: string; high: string; value: number }) {
  return (
    <div className="space-y-1">
      <span className="font-medium text-fg">{label}</span>
      <input type="range" min={0} max={100} defaultValue={value} className="w-full accent-[var(--color-primary)]" aria-label={label} />
      <div className="flex justify-between text-xs text-fg-muted">
        <span>{low}</span>
        <span>{high}</span>
      </div>
    </div>
  );
}

export default function PagesNext() {
  const [params, setParams] = useSearchParams();
  const page = params.get("page") ?? "home";
  const drawer = params.get("drawer");
  const addAtRaw = params.get("at");
  const addAt = drawer === "add" ? Number(addAtRaw ?? "7") : undefined;
  const source = params.get("source") ?? undefined;
  const editing = params.get("editing") ?? "everyone";
  const as = params.get("as") ?? (editing !== "everyone" ? editing : "typical");
  const kid = as === "luca";
  const isMusic = page.startsWith("library:5");
  const isLibrary = page !== "home";
  const pageInfo = PAGES.find((p) => p.key === page) ?? PAGES[0]!;
  const curated = pageInfo.mode === "curated";
  const base = useMemo(() => (isMusic ? musicRows() : homeRows(kid)), [isMusic, kid]);
  const rows = kid ? base.filter((r) => r.id !== "r7") : base.filter((r) => r.id !== "r6");
  const [tab, setTab] = useState(params.get("tab") ?? "page");
  const set = (patch: Record<string, string | null>) => {
    const next = new URLSearchParams(params);
    for (const [k, v] of Object.entries(patch)) {
      if (v === null) next.delete(k);
      else next.set(k, v);
    }
    setParams(next, { replace: true });
  };
  const editRow = rows.find((r) => r.id === params.get("row"));
  const editingViewer = editing !== "everyone";

  // While adding Trending, the new rows appear in place in the preview.
  const display: { row: Row; fresh?: boolean }[] = rows.map((r) => ({ row: r }));
  if (addAt !== undefined && source) {
    const fresh: Row[] = [
      { id: "n1", source: "Top rated", title: "Top Rated Movies", group: "Top rated · one row per library", personal: false, placement: "pinned", position: addAt, libraries: "Movies, TV Shows · one row per library", layout: "standard", why: `New: pinned to #${addAt}`, items: TRENDING_MOVIES.slice().reverse() },
      { id: "n2", source: "Top rated", title: "Top Rated TV Shows", group: "Top rated · one row per library", personal: false, placement: "pinned", position: addAt + 1, libraries: "Movies, TV Shows · one row per library", layout: "standard", why: "Same row, next library", items: TRENDING_SHOWS.slice().reverse() },
    ];
    display.splice(addAt - 1, 0, ...fresh.map((row) => ({ row, fresh: true })));
  }

  // Group consecutive fan-out rows into one block.
  const blocks: { label?: string; items: { row: Row; fresh?: boolean; index: number }[] }[] = [];
  display.forEach((d, i) => {
    const last = blocks[blocks.length - 1];
    const item = { ...d, index: i + 1 };
    if (d.row.group && last?.label === d.row.group) last.items.push(item);
    else blocks.push({ label: d.row.group, items: [item] });
  });

  const openAdd = (at: number) => set({ drawer: "add", at: String(at), source: null, row: null });
  const openRow = (id: string) => set({ drawer: "row", row: id, at: null, source: null });

  const pagePicker = (
    <ToolbarPicker
      ariaLabel="Page"
      value={page}
      onChange={(v) => set({ page: v })}
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
  const editingPicker = (
    <ToolbarPicker
      ariaLabel="Editing for"
      value={editing}
      onChange={(v) => set({ editing: v })}
      matchWidth
      className={cn("w-full justify-start text-left", editingViewer && "border-primary text-primary")}
      options={VIEWERS.map((v) => ({
        value: v.id,
        label: v.id === "everyone" ? "Everyone" : `Just ${v.label}`,
        hint: v.id === "everyone" ? "Changes apply to every viewer" : v.hint,
        icon: v.id === "everyone" ? <Users size={14} /> : <User size={14} />,
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

  const summary = curated
    ? `${display.length} rows · in your order`
    : kid
      ? "8 rows · 3 pinned · 3 shown · 2 chosen by Ether · 3 not shown"
      : "8 of up to 9 rows · 4 pinned · 2 shown · 2 chosen by Ether";

  return (
    <>
      <Workspace
        title="Pages"
        subtitle="Home and every library's rows, in one place"
        actions={actions}
        asideLabel="Page controls"
        mainLabel="The page: edit rows right here"
        mainClassName="p-3 sm:p-6"
        aside={
          <>
            <PanelHead>
              <div className="grid gap-2 pb-1">
                <label className="grid gap-1">
                  <span className="text-[11px] font-semibold uppercase tracking-wider text-fg-subtle">Page</span>
                  {pagePicker}
                </label>
                <label className="grid gap-1">
                  <span className="text-[11px] font-semibold uppercase tracking-wider text-fg-subtle">Editing for</span>
                  {editingPicker}
                </label>
              </div>
              <SubtabNav
                ariaLabel="Page tools"
                active={tab}
                onSelect={setTab}
                tabs={[{ key: "page", label: "Page" }, ...(curated ? [] : [{ key: "tuning", label: "Tuning" }])]}
              />
            </PanelHead>
            <div className="space-y-5 p-3 sm:p-4">
              {tab === "tuning" && !curated ? (
                <Section title="Tuning" hint="How Ether chooses the rows it adds and the titles in them.">
                  <Dial label="Discovery" low="Proven favourites" high="New territory" value={50} />
                  <Dial label="Freshness" low="Timeless" high="New and trending" value={60} />
                  <Dial label="Variety" low="Focused" high="Wide-ranging" value={45} />
                </Section>
              ) : (
                <>
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
                      {curated
                        ? "Exactly your rows, in your order. Ether adds nothing."
                        : "Your pinned and shown rows always appear. Ether fills the rest from your Auto rows and its picks."}
                    </p>
                  </Section>
                  {curated ? null : (
                    <Section title="Length">
                      <Line label="Up to" hint="Pinned and Show rows always appear, even past this.">
                        <span className="flex items-center gap-2">
                          <Input className="h-8 w-16" defaultValue="9" /> rows
                        </span>
                      </Line>
                      <button type="button" className="flex w-full items-center justify-between border-t border-border-subtle pt-3 text-left text-fg-muted">
                        Different length per device <ChevronRight size={14} />
                      </button>
                      <Line label="Let Ether add its picks" hint="Off: only the rows you added.">
                        <Switch aria-label="Ether's picks" checked onChange={() => {}} />
                      </Line>
                    </Section>
                  )}
                  {isLibrary ? (
                    <Section title="This library">
                      <Line label="Skip titles Home already shows">
                        <Switch aria-label="Skip Home titles" checked={!isMusic} onChange={() => {}} />
                      </Line>
                    </Section>
                  ) : null}
                  <Section title="Shown in">
                    <p className="text-fg-muted">
                      {isMusic
                        ? "Vela's Listen app (Music › Home). Plex and Jellyfin show these rows on Music's Recommended page."
                        : "Home in Plex, Jellyfin, Emby, Vela and Silo. Each app draws what it can; rows marked Only in Vela are skipped elsewhere."}
                    </p>
                  </Section>
                </>
              )}
            </div>
          </>
        }
        main={
          <div>
            <div className="mb-3 flex flex-wrap items-center gap-2">
              {editingViewer ? (
                <Badge variant="info" className="h-8 gap-1.5 px-3 text-sm">
                  <Eye size={13} /> Previewing Luca
                </Badge>
              ) : (
                <ToolbarPicker
                  ariaLabel="Preview as"
                  value={as}
                  onChange={(v) => set({ as: v })}
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
              <span className="ml-auto text-sm text-fg-muted">{summary}</span>
            </div>
            <p className={cn("mb-3 text-sm", editingViewer ? "font-medium text-primary" : "text-fg-muted")}>
              {editingViewer
                ? "Editing Luca's page only: 2 changes from Everyone. Rows you add here are for Luca only."
                : kid
                  ? "Editing everyone's Home, previewing as Luca. Your changes apply to everyone."
                  : "This is the page. Click a row to change it, drag it to move or pin it, or use + to add a row right there."}
            </p>
            {kid ? <AccessStrip /> : null}
            <div>
              <InsertGap at={1} onAdd={openAdd} active={addAt === 1} />
              {blocks.map((b, bi) => {
                const body = b.items.map((it) => (
                  <div key={it.row.id}>
                    <PreviewRow
                      row={it.row}
                      index={it.index}
                      curated={curated}
                      showRating={kid}
                      fresh={it.fresh}
                      selected={drawer === "row" && editRow?.group ? editRow.group === it.row.group : editRow?.id === it.row.id}
                      onEdit={() => openRow(it.row.id)}
                    />
                  </div>
                ));
                const last = b.items[b.items.length - 1]!.index;
                const fresh = b.items.some((x) => x.fresh);
                return (
                  <div key={bi}>
                    {b.label && b.items.length > 1 ? <div className="pt-3"><Block label={b.label}>{body}</Block></div> : body}
                    <InsertGap at={last + 1} onAdd={openAdd} active={!fresh && addAt === last + 1 && !source} />
                  </div>
                );
              })}
            </div>
            <PageFooter kid={kid} curated={curated} />
          </div>
        }
      />
      <RowDrawer
        key={`${drawer}-${params.get("row")}-${addAt}`}
        open={drawer === "row" || drawer === "add"}
        onClose={() => set({ drawer: null, row: null, at: null, source: null })}
        row={editRow}
        addAt={addAt}
        source={source}
        onPick={(s) => set({ source: s })}
        onChangeSource={() => set({ source: null })}
      />
    </>
  );
}
