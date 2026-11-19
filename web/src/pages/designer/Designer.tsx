import { SubtabNav } from "../../components/layout";
import { Badge, Button, EmptyState, Spinner, toast } from "@braedonsaunders/appkit-ui";
import { Copy, RefreshCw, RotateCcw, Save, Undo2, Users } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { ErrorPanel } from "../../components/feedback";
import { useViewers, viewerName } from "../../components/pickers";
import { t } from "../../i18n";
import { api, errorMessage } from "../../lib/api";
import type { HomeLayout, LayoutPage, LibraryLayoutSection, ViewerHome } from "../../lib/contract";
import { useAsync } from "../../lib/hooks";
import { Workspace, PanelHead } from "../../components/workspace";
import {
  EMPTY_LAYOUT,
  clearLibraryOverride,
  collectionScheduleChanges,
  customiseLibraryPage,
  hasLibraryOverride,
  isLibrarySection,
  libraryIdOfSection,
  mediaSectionForLibrary,
  overrideCount,
  pageOf,
  parseLayoutSection,
  prune,
  sameLayout,
  sectionLabelOf,
  type LayerKey,
} from "../../lib/layout";
import { librariesApi } from "../libraries/api";
import { ContextBar, type ViewContext } from "./ContextBar";
import { HomeCanvas } from "./HomeCanvas";
import { LayerSwitcher } from "./LayerSwitcher";
import { OverrideList } from "./OverrideList";
import { PageSettings } from "./PageSettings";
import { RowPool } from "./RowPool";
import { AnchoredSlots } from "./AnchoredSlots";
import { TuningPanel } from "./TuningPanel";

function initialContext(sectionParam: string | null): ViewContext {
  const now = new Date();
  return {
    device: "tv",
    day: now.getDay(),
    hour: now.getHours(),
    section: parseLayoutSection(sectionParam),
  };
}

/**
 * Home: the native Plex Home (each viewer's For You) for everyone, personalization
 * within it, and per-viewer overrides on top. Controls on the left third,
 * the live Plex Home (rendered server-side from the draft) on the right.
 */
export default function Designer() {
  const [searchParams, setSearchParams] = useSearchParams();
  const { viewers, error: viewersError } = useViewers();
  const doc = useAsync(() => api.layout(), []);
  const libraries = useAsync((signal) => librariesApi.list(signal), []);
  const [draft, setDraft] = useState<HomeLayout>(EMPTY_LAYOUT);
  const [layer, setLayer] = useState<LayerKey>("global");
  const [spotCheck, setSpotCheck] = useState<number | null>(null);
  const [ctx, setCtx] = useState<ViewContext>(() => initialContext(searchParams.get("section")));
  const [saving, setSaving] = useState(false);
  const [nonce, setNonce] = useState(0);
  const [tools, setTools] = useState("layout");
  const [focusRow, setFocusRow] = useState<string | null>(null);
  const [preview, setPreview] = useState<ViewerHome | null>(null);

  const urlSection = searchParams.get("section");
  useEffect(() => {
    if (urlSection !== null) {
      const parsed = parseLayoutSection(urlSection);
      setCtx((cur) => (cur.section === parsed ? cur : { ...cur, section: parsed }));
    }
  }, [urlSection]);

  const updateCtx = (nextCtx: ViewContext) => {
    setCtx(nextCtx);
    if (nextCtx.section !== ctx.section) {
      const nextParams = new URLSearchParams(searchParams);
      if (nextCtx.section === "home") nextParams.delete("section");
      else nextParams.set("section", nextCtx.section);
      setSearchParams(nextParams, { replace: true });
    }
  };

  const saved = doc.data?.layout ?? EMPTY_LAYOUT;
  // Adopt the stored layout in the render that delivers it; an effect could
  // replace an edit made before it ran.
  const [adopted, setAdopted] = useState<typeof doc.data>(undefined);
  if (doc.data && doc.data !== adopted) {
    setAdopted(doc.data);
    setDraft(prune(doc.data.layout));
  }
  const dirty = Boolean(doc.data) && !sameLayout(draft, saved);
  const available = (doc.data?.availableRows ?? []).map((row) =>
    row.kind === "favorites"
      ? { ...row, title: t("Favorites"), description: t("Titles this viewer marked as favorites.") }
      : row,
  );
  const defaults = doc.data?.defaults.maxRows ?? { home: 14, movies: 10, shows: 10 };

  const libraryList = libraries.data ?? [];
  const selectedLibraryId = libraryIdOfSection(ctx.section);
  const selectedLibrary =
    selectedLibraryId !== null ? libraryList.find((l) => l.id === selectedLibraryId) ?? null : null;
  const mediaSection: "movies" | "shows" | undefined = isLibrarySection(ctx.section)
    ? mediaSectionForLibrary(selectedLibrary)
    : undefined;
  const sectionLabel = sectionLabelOf(ctx.section, libraryList);
  const libraryCustomised = hasLibraryOverride(draft, ctx.section);

  // If the owner edits an uncustomised library page directly, fork from its
  // shared Movies/TV page first so inherited rules are preserved.
  const applyDraftEdit = (next: HomeLayout) => {
    if (
      layer === "global" &&
      isLibrarySection(ctx.section) &&
      !hasLibraryOverride(draft, ctx.section) &&
      hasLibraryOverride(next, ctx.section) &&
      mediaSection
    ) {
      const base = pageOf(draft, "global", mediaSection);
      const created = pageOf(next, "global", ctx.section);
      if (base && created) {
        const b = structuredClone(base);
        const editedRows = new Set(created.rows.map((r) => r.row));
        const mergedTuning =
          b.tuning || created.tuning ? { ...(b.tuning ?? {}), ...(created.tuning ?? {}) } : undefined;
        const mergedDeviceMax =
          b.deviceMaxRows || created.deviceMaxRows
            ? { ...(b.deviceMaxRows ?? {}), ...(created.deviceMaxRows ?? {}) }
            : undefined;
        const mergedPage: LayoutPage = {
          ...b,
          section: ctx.section,
          maxRows: created.maxRows ?? b.maxRows,
          deviceMaxRows: mergedDeviceMax,
          dedupeHome: created.dedupeHome ?? b.dedupeHome,
          tuning: mergedTuning,
          rows: [...b.rows.filter((r) => !editedRows.has(r.row)), ...created.rows],
        };
        setDraft(
          prune({
            ...next,
            global: (next.global ?? []).map((p) =>
              p.section === ctx.section ? mergedPage : p,
            ),
          }),
        );
        return;
      }
    }
    setDraft(next);
  };

  const viewerId = layer === "global" ? spotCheck : layer;
  const overrideTotal = (viewers ?? []).reduce((n, v) => n + overrideCount(draft, v.id), 0);
  // i18n: memo holds no copy
  const viewerById = useMemo(() => new Map((viewers ?? []).map((v) => [v.id, v])), [viewers]);
  const previewWho =
    viewerId !== null && viewerById.get(viewerId)
      ? viewerName(viewerById.get(viewerId)!)
      : t("Typical viewer");
  const layerName =
    layer === "global"
      ? t("everyone's Home")
      : viewerById.get(layer)
        ? viewerName(viewerById.get(layer)!)
        : t("Viewer {id}", { id: layer });

  const save = async () => {
    setSaving(true);
    try {
      // Collection schedules belong to their collections: send only the edited ones.
      const schedules = collectionScheduleChanges(draft, saved);
      const next = await api.putLayout({
        ...draft,
        collectionSchedules: Object.keys(schedules).length ? schedules : undefined,
      });
      doc.setData(next);
      setDraft(prune(next.layout));
      setNonce((n) => n + 1);
      toast.success(t("Home layout saved"), {
        description: t("Apps pick it up the next time Home loads."),
      });
    } catch (e) {
      toast.error(t("Couldn't save the layout"), { description: errorMessage(e) });
    } finally {
      setSaving(false);
    }
  };

  const refreshRows = async () => {
    try {
      if (viewerId !== null) await api.refreshViewer(viewerId);
      setNonce((n) => n + 1);
      toast.success(viewerId !== null ? t("Rows rebuilt") : t("Preview refreshed"));
    } catch (e) {
      toast.error(t("Couldn't rebuild rows"), { description: errorMessage(e) });
    }
  };

  const actions = (
    <div className="flex flex-wrap items-center gap-2">
      {dirty ? <Badge variant="warning">{t("Unsaved changes")}</Badge> : null}
      <Button variant="ghost" size="sm" onClick={() => void refreshRows()}>
        <RefreshCw size={14} /> {viewerId !== null ? t("Rebuild rows") : t("Refresh")}
      </Button>
      <Button
        variant="outline"
        size="sm"
        onClick={() => setDraft(prune(saved))}
        disabled={!dirty || saving}
      >
        <Undo2 size={14} /> {t("Discard")}
      </Button>
      <Button size="sm" onClick={() => void save()} disabled={!dirty || saving}>
        <Save size={14} /> {saving ? t("Saving…") : t("Save layout")}
      </Button>
    </div>
  );

  const editing = {
    draft,
    onChange: applyDraftEdit,
    layer,
    section: ctx.section,
    viewers: viewers ?? [],
    sectionLabel,
    mediaSection,
  };

  const switchLayer = (l: LayerKey) => {
    setLayer(l);
    setFocusRow(null);
  };

  const controlsReady = Boolean(viewers && doc.data);
  const state = viewersError ? (
    <div className="p-6">
      <ErrorPanel error={viewersError} />
    </div>
  ) : doc.error ? (
    <div className="p-6">
      <ErrorPanel error={doc.error} onRetry={() => void doc.reload()} />
    </div>
  ) : viewers && viewers.length === 0 ? (
    <div className="p-6">
      <EmptyState
        icon={<Users />}
        title={t("No viewers yet")}
        description={t("Add a viewer. They show up here after they sign in.")}
      />
    </div>
  ) : null;

  return (
    <Workspace
      title={t("Home")}
      actions={actions}
      asideLabel={t("Designer controls")}
      mainLabel={t("Home preview")}
      mainClassName="p-3 sm:p-6"
      aside={
        controlsReady && viewers ? (
          <>
            <PanelHead>
              <div
                className="flex min-w-0 flex-wrap items-center gap-1"
                aria-label={t("Editing and context")}
              >
                <LayerSwitcher
                  layer={layer}
                  onChange={switchLayer}
                  viewers={viewers}
                  draft={draft}
                />
                <ContextBar
                  ctx={ctx}
                  onChange={updateCtx}
                  libraries={libraryList}
                  draft={draft}
                  className="shrink-0"
                />
              </div>
              <SubtabNav
                ariaLabel={t("Designer tools")}
                active={tools}
                onSelect={setTools}
                tabs={[
                  { key: "layout", label: t("Layout") },
                  { key: "rows", label: t("Rows") },
                  { key: "tuning", label: t("Tuning") },
                  {
                    key: "overrides",
                    label: t("Overrides"),
                    count: overrideTotal || undefined,
                  },
                ]}
              />
            </PanelHead>
            <div className="space-y-5 p-3 sm:p-4">
              {isLibrarySection(ctx.section) && mediaSection ? (
                <LibraryLayoutBanner
                  section={ctx.section}
                  libraryName={sectionLabel}
                  mediaSection={mediaSection}
                  customised={libraryCustomised}
                  onCustomise={() =>
                    setDraft(customiseLibraryPage(draft, ctx.section as LibraryLayoutSection, mediaSection))
                  }
                  onUseShared={() =>
                    setDraft(clearLibraryOverride(draft, ctx.section as LibraryLayoutSection))
                  }
                />
              ) : null}
              {tools === "layout" && (
                <>
                  <PageSettings {...editing} defaults={defaults} />
                  <AnchoredSlots {...editing} available={available} />
                </>
              )}
              {tools === "rows" && (
                <RowPool
                  {...editing}
                  available={available}
                  focusRow={focusRow}
                  onFocusRow={setFocusRow}
                  preview={preview}
                  previewWho={previewWho}
                />
              )}
              {tools === "tuning" && <TuningPanel {...editing} />}
              {tools === "overrides" && <OverrideList {...editing} onOpenLayer={switchLayer} />}
            </div>
          </>
        ) : (
          <div className="grid flex-1 place-items-center p-6">
            <Spinner size={22} label={t("Loading the Home layout")} />
          </div>
        )
      }
      main={
        <HomeCanvas
          ready={controlsReady}
          draft={draft}
          dirty={dirty}
          saved={saved}
          layer={layer}
          layerName={layerName}
          ctx={ctx}
          sectionLabel={sectionLabel}
          viewers={viewers ?? []}
          viewerId={viewerId}
          onSpotCheck={setSpotCheck}
          refreshNonce={nonce}
          onChange={applyDraftEdit}
          onPreviewChange={setPreview}
          onEditRow={(row) => {
            setTools("rows");
            setFocusRow(row);
          }}
        />
      }
    >
      {state}
    </Workspace>
  );
}

function LibraryLayoutBanner({
  libraryName,
  mediaSection,
  customised,
  onCustomise,
  onUseShared,
}: {
  section: LibraryLayoutSection;
  libraryName: string;
  mediaSection: "movies" | "shows";
  customised: boolean;
  onCustomise: () => void;
  onUseShared: () => void;
}) {
  const sharedName = mediaSection === "shows" ? t("TV") : t("Movies");
  return (
    <section
      aria-label={t("Library layout")}
      className="space-y-2.5 rounded-xl border border-border bg-surface p-3"
    >
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0 flex-1 space-y-0.5">
          <div className="flex flex-wrap items-center gap-1.5">
            <p className="text-sm font-semibold text-fg">
              {customised
                ? t("Custom Recommended layout")
                : t("Uses the shared {media} layout", { media: sharedName })}
            </p>
            <Badge variant={customised ? "info" : "secondary"}>
              {customised ? t("Custom") : sharedName}
            </Badge>
          </div>
          <p className="text-xs text-fg-muted">
            {t(
              "Applies to {name}'s Recommended page in Vela, Plex and Jellyfin/Emby.",
              { name: libraryName },
            )}
          </p>
        </div>
      </div>
      <div>
        {customised ? (
          <Button type="button" size="sm" variant="outline" onClick={onUseShared}>
            <RotateCcw size={13} />
            {t("Use the shared layout")}
          </Button>
        ) : (
          <Button type="button" size="sm" variant="outline" onClick={onCustomise}>
            <Copy size={13} />
            {t("Customise this library")}
          </Button>
        )}
      </div>
    </section>
  );
}
