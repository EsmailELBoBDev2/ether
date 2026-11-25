import { Badge, SearchSelect, SettingsSection, cn } from "@braedonsaunders/appkit-ui";
import {
  CheckCircle2,
  ChevronRight,
  Globe,
  Home,
  MonitorCheck,
  Plug,
  ShieldCheck,
} from "lucide-react";
import { Fragment, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { ErrorPanel, LoadingRows } from "../../components/feedback";
import { Segmented } from "../../components/fields";
import { Section } from "../../components/layout";
import { useViewers, viewerName } from "../../components/pickers";
import { t } from "../../i18n";
import { searchSelectLabels } from "../../i18n/appkit-labels";
import { api } from "../../lib/api";
import { formatNumber } from "../../lib/format";
import { useAsync } from "../../lib/hooks";
import type {
  QualityAnswer,
  QualityClient,
  QualityLayer,
  QualitySummaryException,
  QualitySummaryRow,
} from "../../lib/types";
import {
  ExceptionPill,
  SummaryCellView,
  buildSummaryDeviceOptions,
  createRuleResolver,
  ruleHref,
  summaryKindLabel,
  type RuleResolver,
} from "./qualitySummaryUi";
import { TitlePicker } from "./Search";

type Where = "home" | "away";

function resolutionName(h: number | undefined): string {
  if (!h) return t("Any resolution");
  return h === 2160 ? t("4K") : `${h}p`;
}

function rateName(kbps: number): string {
  return t("{rate} Mbps", { rate: formatNumber(kbps / 1000, 1) });
}

function explainKindLabel(kind: string | undefined): string {
  if (kind === "movie") return t("Movies");
  if (kind === "episode" || kind === "tv") return t("Episodes");
  return "";
}

/** The big line: "1080p · HDR off", plus bitrate/stereo caps when set. */
export function answerHeadline(a: QualityAnswer): string {
  if (a.blocked) return t("Can't play away from home");
  const hdr = a.dolbyVision ? t("HDR and Dolby Vision") : a.hdr ? t("HDR on") : t("HDR off");
  const parts = [resolutionName(a.maxHeight), hdr];
  if (a.bitrateKbps) parts.push(t("Up to {rate}", { rate: rateName(a.bitrateKbps) }));
  if (a.stereo) parts.push(t("Stereo"));
  return parts.join(" · ");
}

export function layerText(l: QualityLayer, _viewer: string, resolveRule?: RuleResolver): string {
  const ref = l.ruleId && resolveRule ? resolveRule(l.ruleId) : undefined;
  const n = ref?.number || l.rule || 0;
  switch (l.kind) {
    case "profile":
      if (l.source === "rule") {
        return n > 0
          ? t("Rule {n} · {profile}", { n, profile: l.profile ?? "" })
          : t("Rule · {profile}", { profile: l.profile ?? "" });
      }
      if (l.source === "defaultAway") {
        return l.profile
          ? t("Default away · {profile}", { profile: l.profile })
          : t("Default away");
      }
      return l.profile ? t("Default · {profile}", { profile: l.profile }) : t("Default at home");
    case "device":
      if (!l.decisive) return t("No limit");
      return (
        [
          l.maxHeight ? t("Up to {height}", { height: resolutionName(l.maxHeight) }) : "",
          l.hdr === false ? t("No HDR") : l.dolbyVision === false ? t("No Dolby Vision") : "",
        ]
          .filter(Boolean)
          .join(" · ") || t("No limit")
      );
    case "rule-limit": {
      if (l.ignored) return t("Device resolution is fixed");
      const limits = [
        l.maxHeight ? t("up to {height}", { height: resolutionName(l.maxHeight) }) : "",
        l.bitrateKbps ? rateName(l.bitrateKbps) : "",
        l.stereo ? t("Stereo") : "",
      ]
        .filter(Boolean)
        .join(" · ");
      if (!limits) return t("No limit");
      return n > 0 ? t("Rule {n} · {limits}", { n, limits }) : limits;
    }
    case "remote-blocked":
      return t("Away playback disabled");
    case "android-lift":
      return n > 0 ? t("Rule {n} lifts the device limit", { n }) : t("Rule lifts the device limit");
  }
}

function layerEditHref(l: QualityLayer): string {
  if (l.kind === "device") return "/playback/devices/apps";
  if (l.kind === "remote-blocked") return "/viewers";
  if (l.kind === "profile" && l.source === "defaultAway") return "/releases/rules?default=away";
  if (l.kind === "profile" && l.source !== "rule") return "/releases/rules?default=home";
  if (l.ruleId) return ruleHref(l.ruleId);
  return "/releases/rules";
}

function Answer({
  answer,
  viewer,
  resolveRule,
}: {
  answer: QualityAnswer;
  viewer: string;
  resolveRule: RuleResolver;
}) {
  const labels: Record<string, string> = {
    profile: t("Profile"),
    device: t("Device"),
    "rule-limit": t("Rule limit"),
    "android-lift": t("Rule"),
    "remote-blocked": t("Access"),
  };
  const kindText = explainKindLabel(answer.kind);

  return (
    <div className="space-y-5 border-t border-border p-5" aria-live="polite">
      <div className="flex items-center gap-4 rounded-xl border border-primary/20 bg-primary/5 p-5">
        <MonitorCheck size={36} className="shrink-0 text-primary" />
        <div className="space-y-2">
          <p className="text-xs font-medium text-fg-muted">{t("Available quality")}</p>
          {kindText || answer.profile ? (
            <div className="flex flex-wrap items-center gap-2" data-testid="quality-media">
              {kindText ? <Badge variant="secondary">{kindText}</Badge> : null}
              {answer.profile ? <Badge variant="outline">{answer.profile}</Badge> : null}
            </div>
          ) : null}
          <div data-testid="quality-headline" className="flex flex-wrap items-center gap-2">
            <span className="text-3xl font-semibold tracking-tight">
              {answer.blocked ? t("Can't play away from home") : resolutionName(answer.maxHeight)}
            </span>
            {!answer.blocked ? (
              <>
                <Badge variant="secondary">{answer.hdr ? t("HDR") : t("SDR")}</Badge>
                {answer.dolbyVision ? <Badge variant="secondary">{t("Dolby Vision")}</Badge> : null}
                {answer.bitrateKbps ? (
                  <Badge variant="outline">
                    {t("Up to {rate}", { rate: rateName(answer.bitrateKbps) })}
                  </Badge>
                ) : null}
                {answer.stereo ? <Badge variant="outline">{t("Stereo")}</Badge> : null}
              </>
            ) : null}
          </div>
        </div>
      </div>
      <ol className="flex flex-col gap-6 sm:flex-row" aria-label={t("What decided it")}>
        {answer.layers.map((original, i) => {
          const profile = answer.layers.find((step) => step.kind === "profile");
          const hdrCap =
            original.kind === "device" &&
            ((profile?.hdr === true && original.hdr === false) ||
              (profile?.dolbyVision === true && original.dolbyVision === false));
          const l = hdrCap
            ? {
                ...original,
                decisive: true,
                maxHeight: original.decisive ? original.maxHeight : undefined,
              }
            : original;
          return (
            <li
              key={`${l.kind}-${l.ruleId ?? i}`}
              className={cn(
                "relative min-w-0 flex-1 space-y-3 rounded-xl border border-border bg-surface p-4",
                l.ignored && "opacity-60",
                l.decisive && "border-primary/40",
              )}
            >
              {i > 0 ? (
                <ChevronRight
                  aria-hidden
                  size={18}
                  className="absolute -left-6 top-1/2 hidden text-fg-muted sm:block"
                />
              ) : null}
              <div className="flex items-center gap-2 text-sm font-medium">
                <span className="text-fg-muted">{i + 1}</span>
                {labels[l.kind] ?? t("Limit")}
              </div>
              <p className={cn("text-sm text-fg-muted", l.ignored && "line-through")}>
                {layerText(l, viewer, resolveRule)}
              </p>
              {l.decisive ? (
                <Badge variant="info">
                  <ShieldCheck size={12} />
                  {t("Sets the limit")}
                </Badge>
              ) : (
                <CheckCircle2
                  aria-label={t("No additional limit")}
                  size={16}
                  className="text-success"
                />
              )}
              {l.edited ? <Badge variant="secondary">{t("Edited")}</Badge> : null}
              <div>
                <Link className="text-sm text-primary underline" to={layerEditHref(l)}>
                  {l.decisive ? t("Edit") : t("Set limit")}
                </Link>
              </div>
            </li>
          );
        })}
      </ol>
      <p className="text-xs text-fg-muted">
        {t("Apps can ask for less in their own quality settings.")}
      </p>
    </div>
  );
}

interface ExplainChoice {
  value: string;
  label: string;
  clientId: string;
}

function partitionExceptions(
  rows: QualitySummaryRow[],
  unfiltered: boolean,
  resolveRule: RuleResolver,
): {
  globalExceptions: QualitySummaryException[];
  globalRuleIds: Set<string>;
} {
  if (!unfiltered || rows.length === 0) {
    return { globalExceptions: [], globalRuleIds: new Set() };
  }
  const nonPluginRows = rows.filter((r) => !r.chosenByPlugin);
  const byId = new Map<string, QualitySummaryException>();

  for (const row of rows) {
    for (const ex of row.exceptions) {
      const ref = resolveRule(ex.ruleId);
      const isGlobal = ref.rule
        ? !ref.rule.libraries?.length
        : nonPluginRows.length > 0
          ? nonPluginRows.every((r) => r.exceptions.some((e) => e.ruleId === ex.ruleId))
          : rows.every((r) => r.exceptions.some((e) => e.ruleId === ex.ruleId));
      if (!isGlobal) continue;
      const prev = byId.get(ex.ruleId);
      if (!prev || (!prev.profile && ex.profile)) {
        byId.set(ex.ruleId, ex);
      }
    }
  }
  return {
    globalExceptions: [...byId.values()],
    globalRuleIds: new Set(byId.keys()),
  };
}

/**
 * "Who gets what": per-library summary across At home and Away with Viewer and
 * Device filters, followed by "Check one play" for tracing a single play's layers.
 */
export function WhoGetsWhat() {
  const { viewers, error: viewersError } = useViewers();
  const config = useAsync(() => api.getConfig(), []);
  const quality = config.data?.quality ?? null;
  const devices = useAsync((signal) => api.qualityDevices(signal), []);
  const clients = useAsync<QualityClient[]>(() => api.qualityClients(), []);

  // Summary filters ("Anyone", "Any device").
  const [filterViewer, setFilterViewer] = useState("");
  const [filterDevice, setFilterDevice] = useState("");
  const summary = useAsync(
    (signal) =>
      api.qualitySummary(
        {
          ...(filterViewer ? { viewer: filterViewer } : {}),
          ...(filterDevice ? { device: filterDevice } : {}),
        },
        signal,
      ),
    [filterViewer, filterDevice],
  );

  const resolveRule = useMemo(
    () =>
      createRuleResolver({
        rules: quality?.rules,
        rows: summary.data?.rows,
        viewers,
      }),
    [quality?.rules, summary.data?.rows, viewers],
  );

  const unfiltered = !filterViewer && !filterDevice;
  const { globalExceptions, globalRuleIds } = useMemo(
    () => partitionExceptions(summary.data?.rows ?? [], unfiltered, resolveRule),
    [summary.data?.rows, unfiltered, resolveRule],
  );

  // "Check one play" state.
  const [pickedViewer, setPickedViewer] = useState(0);
  const [pickedClient, setPickedClient] = useState("");
  const [where, setWhere] = useState<Where>("home");
  const [title, setTitle] = useState<{ key: string; name: string } | null>(null);
  const [libraryId, setLibraryId] = useState(0);
  const [kind, setKind] = useState<"" | "movie" | "episode">("");

  const explainOptions = useMemo<ExplainChoice[]>(() => {
    if (!clients.data || !devices.data) return [];
    const out: ExplainChoice[] = [];
    const seenIds = new Set<string>();
    for (const c of clients.data) {
      seenIds.add(c.id);
      out.push({ value: c.id, label: c.name, clientId: c.id });
    }
    for (const seen of devices.data.clients ?? []) {
      if (seenIds.has(seen.id)) continue;
      out.push({ value: seen.id, label: seen.name, clientId: seen.id });
    }
    out.push({ value: "any", label: t("Any app"), clientId: "any" });
    return out;
  }, [clients.data, devices.data]);

  const viewerId = pickedViewer || viewers?.find((v) => v.isOwner)?.id || viewers?.[0]?.id || 0;
  const activeExplain = useMemo(() => {
    if (explainOptions.length === 0) return null;
    return explainOptions.find((o) => o.value === pickedClient) ?? explainOptions[0]!;
  }, [explainOptions, pickedClient]);

  const remote = where === "away";
  const answer = useAsync<QualityAnswer | null>(
    (signal) =>
      viewerId > 0 && activeExplain
        ? api.explainQuality(
            {
              userId: viewerId,
              clientId: activeExplain.clientId,
              ...(title ? { titleKey: title.key } : {}),
              ...(!title && libraryId ? { libraryId } : {}),
              ...(!title && kind ? { kind } : {}),
              remote,
            },
            signal,
          )
        : Promise.resolve(null),
    [viewerId, activeExplain, remote, title?.key, libraryId, kind],
  );
  const viewer = viewers?.find((v) => v.id === viewerId);

  const summaryError = summary.error ?? devices.error ?? viewersError;
  const checkerError = viewersError ?? clients.error ?? devices.error;

  const summaryDeviceOptions = useMemo(
    () => buildSummaryDeviceOptions(devices.data),
    [devices.data],
  );

  const summaryViewerOptions = useMemo(
    () => [
      { value: "", label: t("Anyone") },
      ...(viewers ?? []).map((v) => ({ value: String(v.id), label: viewerName(v) })),
    ],
    [viewers],
  );

  const libraryOptions = useMemo(
    () => [
      { value: "0", label: t("Any library") },
      ...(summary.data?.rows ?? []).map((r) => ({
        value: String(r.libraryId),
        label: r.library,
      })),
    ],
    [summary.data?.rows],
  );

  return (
    <div className="space-y-6">
      <div data-setting="who-gets-what">
        <Section
          title={t("Who gets what")}
          description={t(
            "The profile and limits each library plays with. Change them under Rules.",
          )}
          bodyClassName="p-0"
          actions={
            viewers && devices.data ? (
              <>
                <div className="w-36 sm:w-44">
                  <SearchSelect
                    {...searchSelectLabels()}
                    value={filterViewer}
                    onChange={setFilterViewer}
                    options={summaryViewerOptions}
                    ariaLabel={t("Filter by viewer")}
                  />
                </div>
                <div className="w-36 sm:w-48">
                  <SearchSelect
                    {...searchSelectLabels()}
                    value={filterDevice}
                    onChange={setFilterDevice}
                    options={summaryDeviceOptions}
                    ariaLabel={t("Filter by device")}
                  />
                </div>
              </>
            ) : null
          }
        >
          {summaryError ? (
            <div className="p-5">
              <ErrorPanel
                error={summaryError}
                onRetry={() => {
                  void summary.reload();
                  void devices.reload();
                }}
              />
            </div>
          ) : !summary.data ? (
            <LoadingRows rows={4} />
          ) : summary.data.rows.length === 0 ? (
            <p className="px-5 py-6 text-sm text-fg-muted">{t("No libraries yet.")}</p>
          ) : (
            <>
              {globalExceptions.length > 0 ? (
                <div
                  data-testid="global-exceptions"
                  className="space-y-2 border-b border-border px-5 py-3.5"
                >
                  <p className="text-xs text-fg-muted">
                    {t("Changes for some people or devices, in every library")}
                  </p>
                  <div className="flex flex-wrap gap-2">
                    {globalExceptions.map((ex) => (
                      <ExceptionPill key={ex.ruleId} exception={ex} resolveRule={resolveRule} />
                    ))}
                  </div>
                </div>
              ) : null}

              {/* Desktop summary table */}
              <div className="hidden sm:block">
                <table className="w-full border-collapse text-left text-sm">
                  <thead>
                    <tr className="border-b border-border text-xs font-medium text-fg-muted">
                      <th className="w-[34%] px-5 py-2.5 font-medium">{t("Library")}</th>
                      <th className="w-[33%] px-5 py-2.5 font-medium">
                        <span className="inline-flex items-center gap-1.5">
                          <Home size={13} aria-hidden />
                          {t("At home")}
                        </span>
                      </th>
                      <th className="w-[33%] px-5 py-2.5 font-medium">
                        <span className="inline-flex items-center gap-1.5">
                          <Globe size={13} aria-hidden />
                          {t("Away")}
                        </span>
                      </th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-border">
                    {summary.data.rows.map((row) => {
                      const rowExceptions = row.exceptions.filter(
                        (ex) => !globalRuleIds.has(ex.ruleId),
                      );
                      const split = row.cells.length > 1;
                      return (
                        <Fragment key={row.libraryId}>
                          {row.cells.map((pair, idx) => (
                            <tr key={`${row.libraryId}-${pair.kind || idx}`} className="align-top">
                              <td className="px-5 py-3.5">
                                <div className="flex flex-wrap items-baseline gap-1.5">
                                  <span
                                    className={cn(
                                      "font-medium",
                                      split && idx > 0 ? "text-fg-muted" : "text-fg",
                                    )}
                                  >
                                    {row.library}
                                  </span>
                                  {split && pair.kind ? (
                                    <span className="text-xs text-fg-muted">
                                      · {summaryKindLabel(pair.kind)}
                                    </span>
                                  ) : null}
                                </div>
                                {row.chosenByPlugin && idx === 0 ? (
                                  <p className="mt-1 flex items-center gap-1.5 text-xs text-fg-muted">
                                    <Plug size={12} aria-hidden />
                                    {t("Releases chosen by {plugin}", {
                                      plugin: row.plugin || t("plugin"),
                                    })}
                                  </p>
                                ) : null}
                              </td>
                              <td className="px-5 py-3.5">
                                <SummaryCellView
                                  cell={pair.home}
                                  chosenByPlugin={row.chosenByPlugin}
                                  resolveRule={resolveRule}
                                />
                              </td>
                              <td className="px-5 py-3.5">
                                <SummaryCellView
                                  cell={pair.away}
                                  chosenByPlugin={row.chosenByPlugin}
                                  resolveRule={resolveRule}
                                />
                              </td>
                            </tr>
                          ))}
                          {rowExceptions.length > 0 ? (
                            <tr className="bg-bg-subtle/40">
                              <td colSpan={3} className="px-5 py-2.5">
                                <div className="flex flex-wrap items-center gap-2">
                                  <span className="text-xs text-fg-muted">{t("Exceptions:")}</span>
                                  {rowExceptions.map((ex) => (
                                    <ExceptionPill
                                      key={ex.ruleId}
                                      exception={ex}
                                      resolveRule={resolveRule}
                                    />
                                  ))}
                                </div>
                              </td>
                            </tr>
                          ) : null}
                        </Fragment>
                      );
                    })}
                  </tbody>
                </table>
              </div>

              {/* Mobile stacked cards */}
              <div className="divide-y divide-border sm:hidden">
                {summary.data.rows.map((row) => {
                  const rowExceptions = row.exceptions.filter(
                    (ex) => !globalRuleIds.has(ex.ruleId),
                  );
                  const split = row.cells.length > 1;
                  return (
                    <div key={row.libraryId} className="space-y-3 px-4 py-4">
                      <div>
                        <p className="font-medium text-fg">{row.library}</p>
                        {row.chosenByPlugin ? (
                          <p className="mt-0.5 flex items-center gap-1.5 text-xs text-fg-muted">
                            <Plug size={12} aria-hidden />
                            {t("Releases chosen by {plugin}", {
                              plugin: row.plugin || t("plugin"),
                            })}
                          </p>
                        ) : null}
                      </div>
                      {row.cells.map((pair, idx) => (
                        <div key={`${row.libraryId}-m-${pair.kind || idx}`} className="space-y-3">
                          {split && pair.kind ? (
                            <p className="text-xs font-medium text-fg-muted">
                              {summaryKindLabel(pair.kind)}
                            </p>
                          ) : null}
                          <div className="space-y-1">
                            <p className="text-xs text-fg-muted">{t("At home")}</p>
                            <SummaryCellView
                              cell={pair.home}
                              chosenByPlugin={row.chosenByPlugin}
                              resolveRule={resolveRule}
                            />
                          </div>
                          <div className="space-y-1">
                            <p className="text-xs text-fg-muted">{t("Away")}</p>
                            <SummaryCellView
                              cell={pair.away}
                              chosenByPlugin={row.chosenByPlugin}
                              resolveRule={resolveRule}
                            />
                          </div>
                        </div>
                      ))}
                      {rowExceptions.length > 0 ? (
                        <div className="flex flex-wrap gap-1.5">
                          {rowExceptions.map((ex) => (
                            <ExceptionPill
                              key={ex.ruleId}
                              exception={ex}
                              resolveRule={resolveRule}
                            />
                          ))}
                        </div>
                      ) : null}
                    </div>
                  );
                })}
              </div>
            </>
          )}
        </Section>
      </div>

      <SettingsSection
        title={t("Check one play")}
        description={t("See the quality a viewer gets on an app, and why.")}
      >
        {checkerError ? (
          <ErrorPanel
            error={checkerError}
            onRetry={() => {
              void clients.reload();
              void devices.reload();
            }}
          />
        ) : !viewers || !activeExplain ? (
          <LoadingRows rows={3} />
        ) : (
          <>
            <div data-setting="check-quality" className="grid gap-4 px-5 py-4 sm:grid-cols-3">
              <div className="min-w-0 space-y-2">
                <p className="text-xs font-medium text-fg-muted">{t("Viewer")}</p>
                <div className="w-full">
                  <SearchSelect
                    {...searchSelectLabels()}
                    value={String(viewerId)}
                    onChange={(v) => setPickedViewer(Number(v))}
                    options={viewers.map((v) => ({ value: String(v.id), label: viewerName(v) }))}
                    ariaLabel={t("Viewer")}
                  />
                </div>
              </div>
              <div className="min-w-0 space-y-2">
                <p className="text-xs font-medium text-fg-muted">{t("App")}</p>
                <div className="w-full">
                  <SearchSelect
                    {...searchSelectLabels()}
                    value={activeExplain.value}
                    onChange={setPickedClient}
                    options={explainOptions.map((o) => ({ value: o.value, label: o.label }))}
                    ariaLabel={t("App")}
                  />
                </div>
              </div>
              <div className="min-w-0 space-y-2">
                <p className="text-xs font-medium text-fg-muted">{t("Where")}</p>
                <Segmented
                  ariaLabel={t("Where")}
                  size="sm"
                  value={where}
                  onChange={setWhere}
                  options={[
                    { value: "home", label: t("At home") },
                    { value: "away", label: t("Away") },
                  ]}
                />
              </div>
            </div>
            <div className="space-y-3 px-5 pb-4">
              <div className="space-y-2">
                <p className="text-xs font-medium text-fg-muted">
                  {t("Movie or episode (optional)")}
                </p>
                {title ? (
                  <div className="flex items-center gap-2 text-sm">
                    <span>{title.name}</span>
                    <button
                      type="button"
                      className="text-primary underline"
                      onClick={() => setTitle(null)}
                    >
                      {t("Clear")}
                    </button>
                  </div>
                ) : (
                  <TitlePicker onPick={(key, name) => setTitle({ key, name })} />
                )}
              </div>
              {!title ? (
                <div className="flex flex-wrap items-center gap-4 pt-1">
                  {libraryOptions.length > 1 ? (
                    <div className="flex items-center gap-2">
                      <span className="text-xs text-fg-muted">{t("Library")}</span>
                      <div className="w-48">
                        <SearchSelect
                          {...searchSelectLabels()}
                          value={String(libraryId)}
                          onChange={(v) => setLibraryId(Number(v))}
                          options={libraryOptions}
                          ariaLabel={t("Library")}
                        />
                      </div>
                    </div>
                  ) : null}
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-xs text-fg-muted">{t("What")}</span>
                    <Segmented
                      ariaLabel={t("What")}
                      size="sm"
                      value={kind}
                      onChange={setKind}
                      options={[
                        { value: "", label: t("Movies and episodes") },
                        { value: "movie", label: t("Movies") },
                        { value: "episode", label: t("Episodes") },
                      ]}
                    />
                  </div>
                </div>
              ) : (
                <p className="text-xs text-fg-muted">
                  {t(
                    "Minimum score and preferred qualities guide ranking. If none match, Ether uses the best available release. Hard limits still apply.",
                  )}
                </p>
              )}
            </div>
            {answer.error ? (
              <ErrorPanel error={answer.error} onRetry={() => void answer.reload()} />
            ) : answer.data ? (
              <Answer
                answer={answer.data}
                viewer={viewer ? viewerName(viewer) : t("This viewer")}
                resolveRule={resolveRule}
              />
            ) : (
              <LoadingRows rows={2} />
            )}
          </>
        )}
      </SettingsSection>
    </div>
  );
}
