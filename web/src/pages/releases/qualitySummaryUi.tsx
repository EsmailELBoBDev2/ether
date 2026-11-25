import { Badge } from "@braedonsaunders/appkit-ui";
import { Fragment } from "react";
import { viewerName } from "../../components/pickers";
import { t } from "../../i18n";
import type { Viewer } from "../../lib/contract";
import {
  fallbackOfRuleId,
  fallbackTitle,
  mbps,
  resolutionName,
  ruleTitle,
  type FallbackRow,
  type RuleNames,
  type RuleRef,
} from "../../lib/qualityRules";
import type {
  QualityDevices,
  QualityRule,
  QualitySummaryCell,
  QualitySummaryException,
  QualitySummaryRow,
} from "../../lib/types";
import { localizedRule } from "./RuleDrawer";

export interface ResolvedRuleRef {
  id: string;
  /** 1-based rule position in `config.quality.rules`, or 0 when unknown. */
  number: number;
  /** Human-readable rule name or condition summary; never a raw rule ID. */
  summary: string;
  rule?: QualityRule;
  /** Set when the id names a fallback row ("default" / "defaultAway"), not a rule. */
  fallback?: FallbackRow;
}

/** Opens a rule or a fallback row in the rule drawer, in place. */
export type OpenRule = (ref: RuleRef) => void;

function refOf(ruleId: string): RuleRef {
  const fallback = fallbackOfRuleId(ruleId);
  return fallback ? { fallback } : { rule: ruleId };
}

export type RuleResolver = (ruleId: string | undefined) => ResolvedRuleRef;

export function formatResolutionCap(height: number): string {
  return `≤ ${resolutionName(height)}`;
}

export function formatBitrateCap(kbps: number): string {
  return t("≤ {n} Mbps", { n: mbps(kbps) });
}

export function formatExceptionEffect(ex: QualitySummaryException): string {
  const parts: string[] = [];
  if (ex.profile) parts.push(ex.profile);
  if (ex.maxHeight > 0) parts.push(formatResolutionCap(ex.maxHeight));
  if (ex.maxBitrateKbps > 0) parts.push(formatBitrateCap(ex.maxBitrateKbps));
  if (ex.stereo) parts.push(t("Stereo"));
  return parts.join(" · ");
}

export function summaryKindLabel(kind: "" | "movie" | "episode"): string {
  if (kind === "movie") return t("Movies");
  if (kind === "episode") return t("Episodes");
  return "";
}

export function createRuleResolver(opts: {
  rules?: QualityRule[] | null;
  rows?: QualitySummaryRow[] | null;
  viewers?: Viewer[] | null;
  libraryName?: (id: number) => string;
}): RuleResolver {
  const list = opts.rules ?? [];
  const rowLibs = new Map<number, string>();
  for (const r of opts.rows ?? []) {
    rowLibs.set(r.libraryId, r.library);
  }
  const names: RuleNames = {
    library: (id) => opts.libraryName?.(id) ?? rowLibs.get(id) ?? t("Unavailable library"),
    viewer: (id) => {
      const v = opts.viewers?.find((x) => x.id === id);
      return v ? viewerName(v) : t("Unavailable viewer");
    },
  };

  return (ruleId) => {
    if (!ruleId) return { id: "", number: 0, summary: t("Rule") };
    const fallback = fallbackOfRuleId(ruleId);
    if (fallback) return { id: ruleId, number: 0, summary: fallbackTitle(fallback), fallback };
    const directIdx = list.findIndex((r) => r.id === ruleId);
    if (directIdx >= 0) {
      const rule = list[directIdx]!;
      return {
        id: ruleId,
        number: directIdx + 1,
        summary: ruleTitle(localizedRule(rule), names),
        rule,
      };
    }
    const synthetic = /^r_(\d+)$/.exec(ruleId);
    if (synthetic) {
      const n = Number(synthetic[1]);
      const rule = n >= 1 && n <= list.length ? list[n - 1] : undefined;
      if (rule) {
        return {
          id: ruleId,
          number: n,
          summary: ruleTitle(localizedRule(rule), names),
          rule,
        };
      }
      return { id: ruleId, number: n, summary: t("Rule {n}", { n }) };
    }
    const viewerCap = /^r_viewer_(\d+)$/.exec(ruleId);
    if (viewerCap) {
      const uid = Number(viewerCap[1]);
      return { id: ruleId, number: 0, summary: names.viewer(uid) };
    }
    return { id: ruleId, number: 0, summary: t("Rule") };
  };
}

export function ruleBadgeLabel(ref: ResolvedRuleRef): string {
  if (ref.fallback) return fallbackTitle(ref.fallback);
  return ref.number > 0 ? t("Rule {n}", { n: ref.number }) : t("Rule");
}

function RuleTrigger({
  ruleId,
  label,
  summary,
  onOpenRule,
}: {
  ruleId: string;
  label: string;
  /** What the rule covers, shown on hover: "Phones & tablets · Away". */
  summary?: string;
  onOpenRule: OpenRule;
}) {
  return (
    <button
      type="button"
      title={summary && summary !== label ? summary : undefined}
      onClick={() => onOpenRule(refOf(ruleId))}
      className="text-left text-primary hover:underline"
    >
      {label}
    </button>
  );
}

export function LimitBadges({
  maxHeight,
  maxBitrateKbps,
  stereo,
}: {
  maxHeight: number;
  maxBitrateKbps: number;
  stereo: boolean;
}) {
  if (!maxHeight && !maxBitrateKbps && !stereo) return null;
  return (
    <>
      {maxHeight > 0 ? <Badge variant="outline">{formatResolutionCap(maxHeight)}</Badge> : null}
      {maxBitrateKbps > 0 ? (
        <Badge variant="outline">{formatBitrateCap(maxBitrateKbps)}</Badge>
      ) : null}
      {stereo ? <Badge variant="outline">{t("Stereo")}</Badge> : null}
    </>
  );
}

export function ExceptionPill({
  exception,
  resolveRule,
  onOpenRule,
}: {
  exception: QualitySummaryException;
  resolveRule: RuleResolver;
  onOpenRule: OpenRule;
}) {
  const ref = resolveRule(exception.ruleId);
  const effect = formatExceptionEffect(exception);
  return (
    <button
      type="button"
      onClick={() => onOpenRule(refOf(exception.ruleId))}
      className="inline-flex items-center gap-1.5 rounded-full border border-border bg-bg-subtle px-2.5 py-1 text-left text-xs transition-colors hover:border-primary/40 hover:bg-primary/5"
    >
      <span className="text-fg-muted">{ruleBadgeLabel(ref)}</span>
      <span className="text-fg">{effect ? `${ref.summary} → ${effect}` : ref.summary}</span>
    </button>
  );
}

function LimitRuleLinks({
  ruleIds,
  resolveRule,
  onOpenRule,
}: {
  ruleIds: string[];
  resolveRule: RuleResolver;
  onOpenRule: OpenRule;
}) {
  if (ruleIds.length === 0) return null;
  return (
    <span className="inline-flex flex-wrap items-center gap-1 text-fg-muted">
      <span>{t("limits from")}</span>
      {ruleIds.map((rid, idx) => (
        <Fragment key={rid}>
          {idx > 0 ? <span>,</span> : null}
          <RuleTrigger
            ruleId={rid}
            label={ruleBadgeLabel(resolveRule(rid))}
            summary={resolveRule(rid).summary}
            onOpenRule={onOpenRule}
          />
        </Fragment>
      ))}
    </span>
  );
}

export function SummaryCellView({
  cell,
  where,
  chosenByPlugin,
  resolveRule,
  onOpenRule,
}: {
  cell: QualitySummaryCell;
  /** The column the cell is in: names the fallback row when no rule picked the profile. */
  where: FallbackRow;
  chosenByPlugin: boolean;
  resolveRule: RuleResolver;
  onOpenRule: OpenRule;
}) {
  const hasLimits = Boolean(cell.maxHeight || cell.maxBitrateKbps || cell.stereo);

  if (chosenByPlugin) {
    if (!hasLimits) {
      return <span className="text-xs text-fg-muted">{t("No limits")}</span>;
    }
    return (
      <div className="space-y-1">
        <div className="flex flex-wrap items-center gap-1.5">
          <LimitBadges
            maxHeight={cell.maxHeight}
            maxBitrateKbps={cell.maxBitrateKbps}
            stereo={cell.stereo}
          />
        </div>
        {cell.limitRuleIds.length > 0 ? (
          <div className="text-xs">
            <LimitRuleLinks
              ruleIds={cell.limitRuleIds}
              resolveRule={resolveRule}
              onOpenRule={onOpenRule}
            />
          </div>
        ) : null}
      </div>
    );
  }

  // Limits from the row that also picked the profile are already on its line.
  const extraLimitIds = cell.limitRuleIds.filter(
    (rid) => rid !== cell.profileRuleId && !(cell.source !== "rule" && fallbackOfRuleId(rid)),
  );

  return (
    <div className="space-y-1">
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="text-sm font-medium text-fg">{cell.profile || "—"}</span>
        <LimitBadges
          maxHeight={cell.maxHeight}
          maxBitrateKbps={cell.maxBitrateKbps}
          stereo={cell.stereo}
        />
      </div>
      <div className="flex flex-wrap items-center gap-1.5 text-xs">
        {cell.source === "rule" && cell.profileRuleId ? (
          <RuleTrigger
            ruleId={cell.profileRuleId}
            label={ruleBadgeLabel(resolveRule(cell.profileRuleId))}
            summary={resolveRule(cell.profileRuleId).summary}
            onOpenRule={onOpenRule}
          />
        ) : (
          <button
            type="button"
            onClick={() => onOpenRule({ fallback: where })}
            className="text-left text-fg-muted hover:text-fg hover:underline"
          >
            {where === "away" && cell.source !== "defaultAway"
              ? t("Everyone else, away: same as at home")
              : fallbackTitle(where)}
          </button>
        )}
        {extraLimitIds.length > 0 ? (
          <>
            <span className="text-fg-muted">·</span>
            <LimitRuleLinks
              ruleIds={extraLimitIds}
              resolveRule={resolveRule}
              onOpenRule={onOpenRule}
            />
          </>
        ) : null}
      </div>
      {cell.partialRuleIds.length > 0 ? (
        <div className="flex flex-wrap items-center gap-1 text-xs text-fg-muted">
          <span>{t("Some titles:")}</span>
          {cell.partialRuleIds.map((rid, idx) => (
            <Fragment key={rid}>
              {idx > 0 ? <span>,</span> : null}
              <RuleTrigger
                ruleId={rid}
                label={ruleBadgeLabel(resolveRule(rid))}
                summary={resolveRule(rid).summary}
                onOpenRule={onOpenRule}
              />
            </Fragment>
          ))}
        </div>
      ) : null}
    </div>
  );
}

export function buildSummaryDeviceOptions(
  devices: QualityDevices | null | undefined,
): { value: string; label: string }[] {
  const out: { value: string; label: string }[] = [{ value: "", label: t("Any device") }];
  if (!devices) return out;
  const groupTranslations: Record<string, string> = {
    tv: t("TVs & streaming boxes"),
    mobile: t("Phones & tablets"),
    computer: t("Computers & browsers"),
  };
  for (const g of devices.groups ?? []) {
    out.push({ value: `group:${g.id}`, label: groupTranslations[g.id] ?? g.name });
  }
  for (const c of devices.clients ?? []) {
    out.push({
      value: `client:${c.id}`,
      label: c.viewer ? `${c.name} (${c.viewer})` : c.name,
    });
  }
  const apps = devices.apps ?? [];
  const appNameCounts = new Map<string, number>();
  for (const a of apps) {
    appNameCounts.set(a.name, (appNameCounts.get(a.name) ?? 0) + 1);
  }
  for (const a of apps) {
    const disambiguate = (appNameCounts.get(a.name) ?? 0) > 1 && a.protocol;
    out.push({
      value: `app:${a.id}`,
      label: disambiguate ? `${a.name} (${a.protocol})` : a.name,
    });
  }
  for (const dt of devices.types ?? []) {
    out.push({ value: `type:${dt.id}`, label: dt.name });
  }
  return out;
}
