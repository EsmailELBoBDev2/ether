import {
  Alert,
  AlertDescription,
  AlertTitle,
  Badge,
  Button,
  EmptyState,
  SearchSelect,
  SettingsSection,
  Switch,
  confirmDialog,
  toast,
} from "@braedonsaunders/appkit-ui";
import { Download, Plus, RefreshCw, SlidersHorizontal } from "lucide-react";
import { useState } from "react";
import { useAsync } from "../../lib/hooks";
import { librariesApi } from "../libraries/api";
import { ErrorPanel, LoadingRows } from "../../components/feedback";
import { ChipGroup, NumberInput } from "../../components/fields";
import { Section, SubtabNav } from "../../components/layout";
import { SaveBar, useDraft } from "../../components/SaveBar";
import { SettingsRow } from "../../components/SettingsRow";
import { localized, t, tn } from "../../i18n";
import { searchSelectLabels } from "../../i18n/appkit-labels";
import { ApiError, api } from "../../lib/api";
import { relativeTime } from "../../lib/format";
import { itemName, resolutionGroups } from "../../lib/qualityRanking";
import { releaseConsole, type QualitySyncReport } from "../../lib/releases";
import type { CustomFormatDefinition, QualityProfile } from "../../lib/types";
import { parseDuration } from "../settings/duration";
import { SourceOrderSection } from "../sources/EqualQuality";
import { RemoteBitrateSection } from "./RemoteBitrateSection";
import { ConfigSection } from "../../components/ConfigSection";
import { PLAY_VERSIONS } from "../settings/schema";
import {
  BoolRow,
  RecordDrawer,
  RecordItem,
  TextRow,
  recordErrors,
  type Errors,
} from "../sources/form";
import { FilterRulesEditor, releaseRuleConflict, useRankingRegistry } from "./ReleasePreferences";
import { CustomFormatsSection, LanguageSection, QualityOrderSection } from "./QualityRanking";
import {
  formatsJSON,
  mergeCustomFormats,
  parseFormats,
  sameFormats,
  type ResolverPolicy,
} from "./resolverDraft";
import { TrashImport } from "./TrashImport";
import { settleQualityRules, useQuality, type Quality } from "./useQuality";
import { LibraryProfilesSection, librariesUsing } from "./LibraryProfiles";
/**
 * A new profile starts as a copy of the first profile (its quality order and
 * format scores), so it ranks like the owner's own until changed.
 */
function blankProfile(profiles: QualityProfile[]): QualityProfile {
  const taken = (name: string) => profiles.some((p) => p.name === name);
  let n = profiles.length + 1;
  while (taken(t("Profile {n}", { n }))) n++;
  const base = profiles.find((p) => !p.import);
  return {
    name: t("Profile {n}", { n }),
    maxHeight: 1080,
    minHeight: 720,
    allowHdr: false,
    allowDolbyVision: false,
    qualityOrder: resolutionGroups(1080, 720),
    customFormatScores: Object.fromEntries(
      Object.entries(base?.customFormatScores ?? {}).filter(([id]) => !id.startsWith("rule:")),
    ),
    minSizeMbPerMin: 5,
    maxSizeMbPerMin: 150,
    episodeMinSizeMbPerMin: 0,
    episodeMaxSizeMbPerMin: 0,
    preferredLanguages: null,
    audioLanguageMode: "preferred",
    allowCam: false,
  };
}

const RESOLUTIONS = localized(() => [
  { value: "0", label: t("Any") },
  { value: "480", label: "480p" },
  { value: "720", label: "720p" },
  { value: "1080", label: "1080p" },
  { value: "2160", label: t("4K (2160p)") },
]);

const resolutionLabel = (h: number) =>
  RESOLUTIONS.find((x) => x.value === String(h))?.label ?? `${h}p`;

function profileSummary(p: QualityProfile) {
  const range =
    p.minHeight === p.maxHeight
      ? resolutionLabel(p.maxHeight)
      : t("{min} to {max}", {
          min: resolutionLabel(p.minHeight),
          max: resolutionLabel(p.maxHeight),
        });
  const first = p.qualityOrder?.[0];
  const formats = Object.values(p.customFormatScores ?? {}).filter((v) => v !== 0).length;
  return [
    range,
    p.allowDolbyVision ? t("HDR and Dolby Vision") : p.allowHdr ? t("HDR allowed") : t("SDR only"),
    first ? t("{quality} first", { quality: itemName(first) }) : "",
    formats ? tn(formats, "{count} custom format", "{count} custom formats") : "",
  ]
    .filter(Boolean)
    .join(" · ");
}

/**
 * Rename profile `from` to `to` in the Default, Default away and every client
 * rule. Libraries that chose it are renamed by the caller (librariesApi).
 */
function withRenamedProfile(quality: Quality, from: string, to: string): Quality {
  return {
    ...quality,
    default: quality.default === from ? to : quality.default,
    remote: quality.remote === from ? to : quality.remote,
    rules: (quality.rules ?? []).map((r) => (r.profile === from ? { ...r, profile: to } : r)),
  };
}

/**
 * The server names a refused profile by its position in the list sent. Name
 * the profile instead, so a change blocked by another profile (removing one
 * while two others clash) says which one to fix.
 */
function withProfileNames(e: unknown, sent: QualityProfile[]): unknown {
  if (!(e instanceof ApiError) || !e.fields?.length) return e;
  const fields = e.fields.map((f) => {
    const at = /^quality\.profiles\[(\d+)\]/.exec(f.field);
    const p = at ? sent[Number(at[1])] : undefined;
    return p ? { ...f, field: p.name } : f;
  });
  return new ApiError(e.status, e.code, e.message, fields);
}

/**
 * Keep the Default and Default away pointing at a profile that exists. A
 * Default that outlives its profile falls back to the first profile; a
 * Default away falls back to each library's At home profile.
 */
function settleDefaults(next: Quality): Quality {
  const profiles = next.profiles ?? [];
  if (profiles.length > 0) {
    if (!profiles.some((p) => p.name === next.default)) next.default = profiles[0]!.name;
    if (next.remote && !profiles.some((p) => p.name === next.remote)) next.remote = "";
    if (next.rules) next.rules = settleQualityRules(next).rules;
  }
  return next;
}

const SYNC_INTERVALS = localized(() => [
  { value: "6", label: t("Every 6 hours") },
  { value: "12", label: t("Every 12 hours") },
  { value: "24", label: t("Every 24 hours") },
  { value: "72", label: t("Every 3 days") },
  { value: "168", label: t("Every 7 days") },
]);

/** Hours in a Go duration string, defaulting to 24. */
function syncHours(raw: string | undefined): string {
  if (!raw) return "24";
  const { amount, unit } = parseDuration(raw);
  const n = Number(amount);
  if (!Number.isFinite(n)) return "24";
  const hours = unit === "d" ? n * 24 : unit === "h" ? n : unit === "m" ? n / 60 : n / 3600;
  return hours > 0 ? String(hours) : "24";
}

/**
 * Automatic update of imported profiles and their format definitions. "Sync
 * now" stays available while automatic sync is off: a manual pull is how an
 * owner takes guide changes only when they choose to.
 */
function SyncSection({ quality, onSaved }: { quality: Quality; onSaved: () => Promise<void> }) {
  const sync = quality.sync;
  const hasRun = Boolean(sync?.lastRun) && !sync!.lastRun!.startsWith("0001-");
  const { draft, setDraft, dirty, reset } = useDraft({
    enabled: sync?.enabled ?? false,
    hours: syncHours(sync?.interval),
  });
  const [saving, setSaving] = useState(false);
  const [syncing, setSyncing] = useState(false);
  const [saveError, setSaveError] = useState<unknown>(null);
  const [syncError, setSyncError] = useState<unknown>(null);
  const [report, setReport] = useState<QualitySyncReport | null>(null);
  if (!draft) return null;
  const imported = (quality.profiles ?? []).some((p) => p.import);
  const hours = draft.hours;
  const options = SYNC_INTERVALS.some((o) => o.value === hours)
    ? SYNC_INTERVALS
    : [...SYNC_INTERVALS, { value: hours, label: t("Every {hours} hours", { hours }) }];

  async function save() {
    if (!draft) return;
    setSaving(true);
    setSaveError(null);
    try {
      // The admin endpoint patches the quality section, so omitted run
      // state stays whatever the server recorded while this form was open.
      await api.putSection("quality", {
        sync: { enabled: draft.enabled, interval: `${draft.hours}h` },
      });
      toast.success(t("Sync settings saved"));
      await onSaved();
    } catch (e) {
      setSaveError(e);
    } finally {
      setSaving(false);
    }
  }

  async function syncNow() {
    setSyncing(true);
    setSyncError(null);
    setReport(null);
    try {
      setReport(await releaseConsole.syncQuality());
      toast.success(t("Guide sync finished"));
      await onSaved();
    } catch (e) {
      setSyncError(e);
    } finally {
      setSyncing(false);
    }
  }

  const title = t("Keep imported profiles in sync");
  if (!imported) {
    return (
      <Section title={title}>
        <p className="text-sm text-fg-muted">
          {t("Applies once you import a profile from TRaSH Guides or Profilarr.")}
        </p>
      </Section>
    );
  }

  return (
    <Section
      title={title}
      description={t("Only profiles imported from TRaSH Guides or Profilarr are updated.")}
      actions={
        <Switch
          aria-label={title}
          checked={draft.enabled}
          onChange={(e) => setDraft({ ...draft, enabled: e.currentTarget.checked })}
        />
      }
    >
      {draft.enabled ? (
        <div className="flex flex-wrap items-center justify-between gap-3">
          <span className="text-sm font-medium text-fg">{t("Check for changes")}</span>
          <div className="w-52">
            <SearchSelect
              {...searchSelectLabels()}
              ariaLabel={t("Check for changes")}
              value={hours}
              onChange={(value) => setDraft({ ...draft, hours: value })}
              options={options}
              searchable={false}
            />
          </div>
        </div>
      ) : null}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <span className="text-sm text-fg-muted">
          {sync?.lastError ? (
            <span className="text-danger">{sync.lastError}</span>
          ) : hasRun ? (
            t("Last synced {when}", { when: relativeTime(sync!.lastRun!) })
          ) : (
            t("Never synced")
          )}
        </span>
        <Button variant="outline" size="sm" disabled={syncing} onClick={() => void syncNow()}>
          <RefreshCw size={14} className={syncing ? "animate-spin" : undefined} />
          {syncing ? t("Syncing…") : t("Sync now")}
        </Button>
      </div>
      {syncError ? <ErrorPanel error={syncError} /> : null}
      {report ? (
        <Alert variant="success" role="status">
          <AlertTitle>{t("Guide sync finished")}</AlertTitle>
          <AlertDescription>
            <ul className="list-disc space-y-1 pl-5">
              <li>
                {tn(report.profilesUpdated, "{count} profile updated", "{count} profiles updated")}
              </li>
              <li>{tn(report.formatsAdded, "{count} format added", "{count} formats added")}</li>
              <li>
                {tn(report.formatsUpdated, "{count} format updated", "{count} formats updated")}
              </li>
              <li>
                {tn(report.formatsRemoved, "{count} format removed", "{count} formats removed")}
              </li>
              {(report.warnings ?? []).map((w) => (
                <li key={w}>{w}</li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      ) : null}
      <SaveBar
        dirty={dirty}
        saving={saving}
        error={saveError}
        onSave={() => void save()}
        onDiscard={() => {
          reset();
          setSaveError(null);
        }}
      />
    </Section>
  );
}

export function QualityTab() {
  const { config, quality, commit, refresh } = useQuality();
  if (config.error) return <ErrorPanel error={config.error} onRetry={() => void config.reload()} />;
  if (!quality) return <LoadingRows rows={4} />;
  return (
    <>
      <div className="space-y-5 rounded-xl border border-border p-5">
        <ProfileList
          quality={quality}
          resolver={config.data?.resolver ?? null}
          commit={commit}
          refresh={refresh}
        />
      </div>
      <div className="pt-1">
        <div className="flex items-center gap-3">
          <div className="h-px flex-1 bg-border" />
          <span className="text-[11px] font-semibold uppercase tracking-[0.14em] text-fg-muted">
            {t("For every profile")}
          </span>
          <div className="h-px flex-1 bg-border" />
        </div>
      </div>
      <SyncSection quality={quality} onSaved={refresh} />
      <RemoteBitrateSection />
      <SourceOrderSection />
      <ConfigSection
        configKey="resolver"
        endpoint="resolver"
        groups={PLAY_VERSIONS}
        label={t("Version menu")}
      />
    </>
  );
}

type Commit = (next: Quality, message: string) => Promise<void>;
type ProfileDrawerTab = "tiers" | "limits" | "scoring";

function ProfileList({
  quality,
  resolver,
  commit,
  refresh,
}: {
  quality: Quality;
  resolver: ResolverPolicy | null;
  commit: Commit;
  refresh: () => Promise<void>;
}) {
  const { fields } = useRankingRegistry();
  const libraries = useAsync((signal) => librariesApi.list(signal), []);
  const libs = libraries.data ?? [];
  const profiles = quality.profiles ?? [];
  const homeDefault = quality.default || profiles[0]?.name || "";
  const awayDefault = quality.remote ?? "";
  const [importing, setImporting] = useState(false);
  const [editing, setEditing] = useState<{ index: number; draft: QualityProfile } | null>(null);
  const [defaultFor, setDefaultFor] = useState<{ home: boolean; away: boolean }>({
    home: false,
    away: false,
  });
  /** The shared custom format definitions editor (JSON). */
  const [formatsText, setFormatsText] = useState("[]");
  /** The definitions editor text when the drawer opened; unchanged means no PUT. */
  const [formatBaseline, setFormatBaseline] = useState("");
  /** The shared definitions as they were when the drawer opened (for merging). */
  const [formatSnapshot, setFormatSnapshot] = useState<CustomFormatDefinition[]>([]);
  const [errors, setErrors] = useState<Errors>({});
  const [error, setError] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);
  const [drawerTab, setDrawerTab] = useState<ProfileDrawerTab>("tiers");

  const open = (index: number, draft: QualityProfile) => {
    setErrors({});
    setError(null);
    setDrawerTab("tiers");
    const cloned = structuredClone(draft);
    setEditing({ index, draft: cloned });
    const isExisting = index < profiles.length;
    setDefaultFor({
      // The first profile becomes the Default.
      home: isExisting ? homeDefault === cloned.name : profiles.length === 0,
      away: isExisting && awayDefault === cloned.name,
    });
    const text = formatsJSON(resolver);
    setFormatsText(text);
    setFormatBaseline(text);
    setFormatSnapshot(structuredClone(resolver?.customFormats ?? []));
  };

  async function save() {
    if (!editing) return;
    const { index, draft: profileDraft } = editing;
    let draft: QualityProfile = { ...profileDraft };
    let parsed: CustomFormatDefinition[] | null = null;
    if (formatsText !== formatBaseline) {
      try {
        parsed = parseFormats(formatsText);
      } catch (e) {
        setError(e);
        setDrawerTab("scoring");
        return;
      }
    }
    const old = profiles[index];
    const list =
      index < profiles.length
        ? profiles.map((p, j) => (j === index ? draft : p))
        : [...profiles, draft];
    const renamed = old && old.name !== draft.name ? old.name : null;

    let next: Quality = { ...quality, profiles: list };
    if (renamed) next = withRenamedProfile(next, renamed, draft.name);
    if (defaultFor.home) {
      next.default = draft.name;
    } else if (old && homeDefault === old.name) {
      next.default = list.find((p) => p.name !== draft.name)?.name ?? draft.name;
    }
    // Default away is optional: with none, people away from home get each
    // library's At home profile.
    if (defaultFor.away) {
      next.remote = draft.name;
    } else if (old && awayDefault === old.name) {
      next.remote = "";
    }
    if (!next.default) next.default = draft.name;
    next = settleDefaults(next);

    const conflict = releaseRuleConflict(draft.releaseRules ?? [], fields);
    if (conflict) {
      setErrors({ releaseRules: conflict });
      setDrawerTab("limits");
      return;
    }
    setSaving(true);
    setError(null);
    setErrors({});
    try {
      await commit(next, index < profiles.length ? t("Profile saved") : t("Profile added"));
      if (renamed) await renameLibraryProfiles(renamed, draft.name);
      // Only touch the shared definitions when this drawer actually edited
      // them; otherwise a stale snapshot must not be sent back.
      if (parsed) {
        const latestConfig = await api.getConfig();
        const latest = latestConfig.resolver.customFormats ?? [];
        const merged = mergeCustomFormats({
          original: formatSnapshot,
          edited: parsed,
          latest,
          app: null,
        });
        if (!sameFormats(merged, latest)) {
          await api.putSection("resolver", { customFormats: merged });
          await refresh();
        }
      }
      setEditing(null);
    } catch (e) {
      const nextErrors = recordErrors(e, `quality.profiles[${index}].`);
      setError(withProfileNames(e, list));
      setErrors(nextErrors);
      if (nextErrors.name) {
        setDrawerTab("tiers");
      } else if (
        nextErrors.minHeight ||
        nextErrors.maxHeight ||
        nextErrors.minSizeMbPerMin ||
        nextErrors.maxSizeMbPerMin ||
        nextErrors.episodeMinSizeMbPerMin ||
        nextErrors.episodeMaxSizeMbPerMin ||
        nextErrors.releaseRules
      ) {
        setDrawerTab("limits");
      }
    } finally {
      setSaving(false);
    }
  }

  async function remove() {
    if (!editing) return;
    const { index } = editing;
    const name = profiles[index]?.name ?? editing.draft.name;
    const rest = profiles.filter((_, j) => j !== index);
    // A device rule using it is removed; libraries that chose it go back to
    // the Default.
    const rules = quality.rules ?? [];
    const usedBy = rules.filter((r) => r.profile === name).length;
    let next: Quality = {
      ...quality,
      profiles: rest,
      rules: rules.filter((r) => r.profile !== name),
    };
    if (next.default === name) next.default = rest[0]?.name ?? "";
    if (next.remote === name) next.remote = "";
    next = settleDefaults(next);
    const usingLibraries = librariesUsing(libs, name);
    const consequences = [
      name ? t("{name} will be removed.", { name }) : t("This profile will be removed."),
      usedBy
        ? tn(
            usedBy,
            "The device rule using it will be removed.",
            "{count} device rules using it will be removed.",
          )
        : "",
      usingLibraries.length
        ? tn(
            usingLibraries.length,
            "The library using it will use the Default profile.",
            "{count} libraries using it will use the Default profile.",
          )
        : "",
      homeDefault === name && next.default
        ? t("The Default profile will be {profile}.", { profile: next.default })
        : "",
    ]
      .filter(Boolean)
      .join(" ");
    const ok = await confirmDialog({
      title: t("Remove profile?"),
      message: consequences,
      confirmLabel: t("Remove"),
      tone: "danger",
    });
    if (!ok) return;
    setSaving(true);
    setError(null);
    try {
      await commit(next, t("Profile removed"));
      await renameLibraryProfiles(name, "");
      setEditing(null);
    } catch (e) {
      setError(withProfileNames(e, rest));
    } finally {
      setSaving(false);
    }
  }

  const draft = editing?.draft;
  const isNew = editing ? editing.index >= profiles.length : false;
  const usedByDraft = editing ? librariesUsing(libs, profiles[editing.index]?.name ?? "") : [];
  const patch = (p: Partial<QualityProfile>) =>
    setEditing((e) => (e ? { ...e, draft: { ...e.draft, ...p } } : e));

  /** Point every library that chose profile `from` at `to` ("" = Default). */
  async function renameLibraryProfiles(from: string, to: string) {
    const changes = libs.flatMap((lib) => {
      const body: Record<string, string> = {};
      if (lib.qualityProfile === from) body.qualityProfile = to;
      if (lib.awayQualityProfile === from) body.awayQualityProfile = to;
      return Object.keys(body).length ? [librariesApi.patch(lib.id, body)] : [];
    });
    if (!changes.length) return;
    await Promise.all(changes);
    await libraries.reload();
  }

  return (
    <>
      <Section
        title={t("Profiles")}
        description={t(
          "Resolution, HDR, release type and language rules used to rank releases. Each library picks one.",
        )}
        actions={
          <Button size="sm" onClick={() => open(profiles.length, blankProfile(profiles))}>
            <Plus size={14} /> {t("Add profile")}
          </Button>
        }
        bodyClassName=""
      >
        <div className="flex flex-wrap items-center gap-x-6 gap-y-2 border-b border-border bg-bg-subtle/40 px-5 py-3 text-sm">
          <span className="flex items-center gap-2">
            <span className="text-xs font-medium text-fg-muted">{t("Default profile")}</span>
            <Badge variant="info">{homeDefault || t("First profile")}</Badge>
          </span>
          <span className="flex items-center gap-2">
            <span className="text-xs font-medium text-fg-muted">{t("Default away profile")}</span>
            <Badge variant="info">{awayDefault || t("Same as at home")}</Badge>
          </span>
          <span className="text-xs text-fg-muted">{t("Set these from a profile’s editor.")}</span>
        </div>
        {profiles.length === 0 ? (
          <EmptyState
            icon={<SlidersHorizontal />}
            title={t("No profiles")}
            description={t("Add a profile so Ether knows which releases to prefer.")}
          />
        ) : (
          <ul className="divide-y divide-border">
            {profiles.map((p, i) => {
              const usedBy = (quality.rules ?? []).filter((r) => r.profile === p.name).length;
              return (
                <RecordItem
                  key={`${p.name}-${i}`}
                  title={p.name || t("Profile {n}", { n: i + 1 })}
                  description={profileSummary(p)}
                  meta={
                    <>
                      {homeDefault === p.name ? <Badge variant="info">{t("Default")}</Badge> : null}
                      {awayDefault === p.name ? (
                        <Badge variant="info">{t("Default away")}</Badge>
                      ) : null}
                      {librariesUsing(libs, p.name).map((lib) => (
                        <Badge key={lib.id} variant="secondary">
                          {lib.name}
                        </Badge>
                      ))}
                      {usedBy ? (
                        <Badge variant="secondary">
                          {tn(usedBy, "{count} rule", "{count} rules")}
                        </Badge>
                      ) : null}
                    </>
                  }
                  editLabel={t("Edit {name}", { name: p.name || t("profile {n}", { n: i + 1 }) })}
                  onEdit={() => open(i, p)}
                />
              );
            })}
          </ul>
        )}
      </Section>

      {libraries.data ? (
        <LibraryProfilesSection
          libraries={libs}
          profiles={profiles}
          defaultName={homeDefault}
          remoteName={awayDefault}
          onChanged={() => libraries.reload()}
        />
      ) : null}

      <TrashImport open={importing} onClose={() => setImporting(false)} onImported={refresh} />

      <RecordDrawer
        open={editing !== null}
        onClose={() => !saving && setEditing(null)}
        title={isNew ? t("New profile") : draft?.name || t("Profile")}
        description={t("How releases are filtered and ranked for apps using this profile.")}
        subtabs={
          draft ? (
            <div className="[&>div]:mb-0">
              <SubtabNav
                ariaLabel={t("Profile sections")}
                active={drawerTab}
                onSelect={(k) => setDrawerTab(k as ProfileDrawerTab)}
                tabs={[
                  { key: "tiers", label: t("Tiers") },
                  { key: "limits", label: t("Limits") },
                  { key: "scoring", label: t("Scoring") },
                ]}
              />
            </div>
          ) : undefined
        }
        onSave={() => void save()}
        saving={saving}
        saveLabel={t("Save profile")}
        onRemove={isNew ? undefined : () => void remove()}
        removeLabel={t("Remove profile")}
        removeDisabled={profiles.length <= 1}
        error={error}
      >
        {draft ? (
          drawerTab === "tiers" ? (
            <>
              <SettingsSection title={t("Profile")}>
                {isNew ? (
                  <SettingsRow
                    title={t("Start from a preset")}
                    description={t(
                      "Copy quality order and format scores from TRaSH Guides or Profilarr.",
                    )}
                    control={
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => {
                          setEditing(null);
                          setImporting(true);
                        }}
                      >
                        <Download size={14} />
                        {t("Import a preset quality profile")}
                      </Button>
                    }
                  />
                ) : null}
                <TextRow
                  label={t("Name")}
                  value={draft.name}
                  error={errors.name}
                  onChange={(v) => patch({ name: v })}
                />
                {draft.import ? (
                  <SettingsRow
                    title={t("Imported from")}
                    control={
                      <Badge variant="outline">
                        {draft.import.source === "dictionarry"
                          ? "Profilarr (Dictionarry)"
                          : "TRaSH Guides"}
                      </Badge>
                    }
                  />
                ) : null}
                <SettingsRow
                  title={t("Default for")}
                  description={t(
                    "Used by every library that has not picked a profile. With no Default away, people away from home get each library's At home profile.",
                  )}
                  control={
                    <ChipGroup
                      ariaLabel={t("Default for")}
                      size="sm"
                      options={[
                        { value: "home", label: t("At home") },
                        { value: "away", label: t("Away") },
                      ]}
                      value={[
                        ...(defaultFor.home ? ["home" as const] : []),
                        ...(defaultFor.away ? ["away" as const] : []),
                      ]}
                      onChange={(next) =>
                        setDefaultFor({
                          home: next.includes("home"),
                          away: next.includes("away"),
                        })
                      }
                    />
                  }
                />
                {!isNew ? (
                  <SettingsRow
                    title={t("Used by")}
                    description={t("Libraries pick their profile under Used by on this page.")}
                    control={
                      <span className="flex flex-wrap justify-end gap-1.5">
                        {usedByDraft.length ? (
                          usedByDraft.map((lib) => (
                            <Badge key={lib.id} variant="secondary">
                              {lib.name}
                            </Badge>
                          ))
                        ) : (
                          <span className="text-sm text-fg-muted">{t("No library")}</span>
                        )}
                      </span>
                    }
                  />
                ) : null}
              </SettingsSection>

              <QualityOrderSection draft={draft} patch={patch} />

              <LanguageSection draft={draft} patch={patch} />
            </>
          ) : drawerTab === "limits" ? (
            <SettingsSection
              title={t("Never play")}
              description={t(
                "Maximum limits and blocked releases always apply. Minimum resolution is a preference.",
              )}
            >
              <SettingsRow
                title={t("Resolution")}
                description={
                  errors.minHeight || errors.maxHeight ? (
                    <span className="text-danger">{errors.minHeight || errors.maxHeight}</span>
                  ) : (
                    t("Lowest preferred and highest allowed resolution.")
                  )
                }
                control={
                  <span className="flex items-center gap-2">
                    <span className="w-32">
                      <SearchSelect
                        {...searchSelectLabels()}
                        value={String(draft.minHeight)}
                        onChange={(v) => patch({ minHeight: Number(v) })}
                        options={RESOLUTIONS}
                        ariaLabel={t("Minimum resolution")}
                        searchable={false}
                      />
                    </span>
                    <span className="text-sm text-fg-muted">{t("to")}</span>
                    <span className="w-32">
                      <SearchSelect
                        {...searchSelectLabels()}
                        value={String(draft.maxHeight)}
                        onChange={(v) => patch({ maxHeight: Number(v) })}
                        options={RESOLUTIONS}
                        ariaLabel={t("Maximum resolution")}
                        searchable={false}
                      />
                    </span>
                  </span>
                }
              />
              <div className="grid gap-6 px-5 py-4 sm:grid-cols-2">
                <fieldset className="min-w-0 space-y-2">
                  <legend className="text-sm font-medium text-fg">{t("Size")}</legend>
                  {errors.minSizeMbPerMin || errors.maxSizeMbPerMin ? (
                    <p className="text-xs text-danger">
                      {errors.minSizeMbPerMin || errors.maxSizeMbPerMin}
                    </p>
                  ) : (
                    <p className="text-xs text-fg-muted">
                      {t("Lowest and highest MB per minute of runtime. 0 means no limit.")}
                    </p>
                  )}
                  <span className="flex flex-wrap items-center gap-2">
                    <NumberInput
                      aria-label={t("Minimum MB per minute")}
                      value={draft.minSizeMbPerMin}
                      min={0}
                      className="w-20"
                      onChange={(n) => patch({ minSizeMbPerMin: n })}
                    />
                    <span className="text-sm text-fg-muted">{t("to")}</span>
                    <NumberInput
                      aria-label={t("Maximum MB per minute")}
                      value={draft.maxSizeMbPerMin}
                      min={0}
                      className="w-20"
                      onChange={(n) => patch({ maxSizeMbPerMin: n })}
                    />
                    <span className="text-sm text-fg-muted">MB</span>
                  </span>
                </fieldset>
                <fieldset className="min-w-0 space-y-2">
                  <legend className="text-sm font-medium text-fg">{t("Episodes")}</legend>
                  {errors.episodeMinSizeMbPerMin || errors.episodeMaxSizeMbPerMin ? (
                    <p className="text-xs text-danger">
                      {errors.episodeMinSizeMbPerMin || errors.episodeMaxSizeMbPerMin}
                    </p>
                  ) : (
                    <p className="text-xs text-fg-muted">
                      {t("Only for libraries with episodes. 0 uses the size limits beside it.")}
                    </p>
                  )}
                  <span className="flex flex-wrap items-center gap-2">
                    <NumberInput
                      aria-label={t("Episode minimum MB per minute")}
                      value={draft.episodeMinSizeMbPerMin ?? 0}
                      min={0}
                      className="w-20"
                      onChange={(n) => patch({ episodeMinSizeMbPerMin: n })}
                    />
                    <span className="text-sm text-fg-muted">{t("to")}</span>
                    <NumberInput
                      aria-label={t("Episode maximum MB per minute")}
                      value={draft.episodeMaxSizeMbPerMin ?? 0}
                      min={0}
                      className="w-20"
                      onChange={(n) => patch({ episodeMaxSizeMbPerMin: n })}
                    />
                    <span className="text-sm text-fg-muted">MB</span>
                  </span>
                </fieldset>
              </div>
              <BoolRow
                label={t("Allow HDR")}
                value={draft.allowHdr}
                help={t("Only used when the app supports it.")}
                onChange={(v) => patch({ allowHdr: v })}
              />
              <BoolRow
                label={t("Allow Dolby Vision")}
                value={draft.allowDolbyVision}
                onChange={(v) => patch({ allowDolbyVision: v })}
              />
              <BoolRow
                label={t("Allow CAM and screener releases")}
                value={draft.allowCam}
                onChange={(v) => patch({ allowCam: v })}
              />
              <FilterRulesEditor
                profile={draft}
                onChange={(next) => patch(next)}
                error={errors.releaseRules}
              />
            </SettingsSection>
          ) : (
            <CustomFormatsSection
              draft={draft}
              patch={patch}
              resolver={resolver}
              formatsText={formatsText}
              onFormatsText={setFormatsText}
              revealed={false}
            />
          )
        ) : null}
      </RecordDrawer>
    </>
  );
}
