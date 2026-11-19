import { SubtabNav } from "../../components/layout";
import { Badge, Button, EmptyState, Spinner, toast } from "@braedonsaunders/appkit-ui";
import { RefreshCw, Save, Undo2, Users } from "lucide-react";
import { useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { ErrorPanel } from "../../components/feedback";
import { useViewers, viewerName } from "../../components/pickers";
import { t } from "../../i18n";
import { api, errorMessage } from "../../lib/api";
import type { BuiltinLayoutSection, HomeLayout, ViewerHome } from "../../lib/contract";
import { useAsync } from "../../lib/hooks";
import { Workspace, PanelHead } from "../../components/workspace";
import {
  EMPTY_LAYOUT,
  collectionScheduleChanges,
  hasLibraryOverride,
  overrideCount,
  prune,
  sameLayout,
  type LayerKey,
} from "../../lib/layout";
import { ContextBar, type ViewContext } from "./ContextBar";
import { HomeCanvas } from "./HomeCanvas";
import { LayerSwitcher } from "./LayerSwitcher";
import { OverrideList } from "./OverrideList";
import { PageSettings } from "./PageSettings";
import { RowPool } from "./RowPool";
import { AnchoredSlots } from "./AnchoredSlots";
import { TuningPanel } from "./TuningPanel";

function parseDesignerSection(raw: string | null): BuiltinLayoutSection {
  if (raw === "movies" || raw === "shows") return raw;
  return "home";
}

function initialContext(section: BuiltinLayoutSection): ViewContext {
  const now = new Date();
  return { device: "tv", day: now.getDay(), hour: now.getHours(), section };
}

/**
 * Home: the native Plex Home (each viewer's For You) for everyone, personalization
 * within it, and per-viewer overrides on top. Controls on the left third,
 * the live Plex Home (rendered server-side from the draft) on the right.
 */
export default function Designer() {
  const [searchParams, setSearchParams] = useSearchParams();
  const urlSectionParam = searchParams.get("section");
  const { viewers, error: viewersError } = useViewers();
  const doc = useAsync(() => api.layout(), []);
  const [draft, setDraft] = useState<HomeLayout>(EMPTY_LAYOUT);
  const [layer, setLayer] = useState<LayerKey>("global");
  const [spotCheck, setSpotCheck] = useState<number | null>(null);
  const [seenSectionParam, setSeenSectionParam] = useState<string | null>(urlSectionParam);
  const [ctx, setCtx] = useState<ViewContext>(() =>
    initialContext(parseDesignerSection(urlSectionParam)),
  );
  const [saving, setSaving] = useState(false);
  const [nonce, setNonce] = useState(0);
  const [tools, setTools] = useState("layout");
  const [focusRow, setFocusRow] = useState<string | null>(null);
  const [preview, setPreview] = useState<ViewerHome | null>(null);

  if (urlSectionParam !== seenSectionParam) {
    setSeenSectionParam(urlSectionParam);
    const nextSection = parseDesignerSection(urlSectionParam);
    if (nextSection !== ctx.section) {
      setCtx((cur) => ({ ...cur, section: nextSection }));
      setFocusRow(null);
    }
  }

  const updateCtx = (next: ViewContext) => {
    setCtx(next);
    if (next.section !== ctx.section) {
      setFocusRow(null);
      const params = new URLSearchParams(searchParams);
      if (next.section === "home") {
        params.delete("section");
      } else {
        params.set("section", next.section);
      }
      setSeenSectionParam(params.get("section"));
      setSearchParams(params, { replace: true });
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

  // i18n: memo holds no copy
  const customisedLibraries = useMemo(() => {
    if (ctx.section !== "movies" && ctx.section !== "shows") return [];
    return (doc.data?.libraries ?? []).filter(
      (lib) => lib.basePage === ctx.section && hasLibraryOverride(draft, lib.section),
    );
  }, [ctx.section, doc.data?.libraries, draft]);

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
    onChange: setDraft,
    layer,
    section: ctx.section,
    viewers: viewers ?? [],
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
                <ContextBar ctx={ctx} onChange={updateCtx} className="shrink-0" />
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
              {(ctx.section === "movies" || ctx.section === "shows") && (
                <div className="space-y-1.5 rounded-lg border border-border bg-surface px-3 py-2.5 text-xs text-fg-muted">
                  <p>
                    {ctx.section === "shows"
                      ? t(
                          "The standard Recommended rows for every TV library. A library can use its own instead under Libraries › name › Tabs.",
                        )
                      : t(
                          "The standard Recommended rows for every Movies library. A library can use its own instead under Libraries › name › Tabs.",
                        )}
                  </p>
                  {customisedLibraries.length > 0 ? (
                    <p className="flex flex-wrap items-center gap-x-1.5 gap-y-1 text-fg">
                      <span className="text-fg-muted">{t("Libraries with their own rows:")}</span>
                      {customisedLibraries.map((lib, idx) => (
                        <span key={lib.id}>
                          <Link
                            to={`/libraries/${lib.id}/tabs`}
                            className="font-medium text-primary hover:underline"
                          >
                            {lib.name}
                          </Link>
                          {idx < customisedLibraries.length - 1 ? (
                            <span className="text-fg-muted">,</span>
                          ) : null}
                        </span>
                      ))}
                    </p>
                  ) : null}
                </div>
              )}
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
          viewers={viewers ?? []}
          viewerId={viewerId}
          onSpotCheck={setSpotCheck}
          refreshNonce={nonce}
          onChange={setDraft}
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
