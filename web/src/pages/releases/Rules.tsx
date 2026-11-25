import {
  Alert,
  AlertDescription,
  Badge,
  Button,
  SearchSelect,
  SettingsSection,
} from "@braedonsaunders/appkit-ui";
import {
  ArrowRight,
  Clapperboard,
  Globe,
  Home,
  Library as LibraryIcon,
  Lock,
  Monitor,
  MonitorSmartphone,
  Pencil,
  Plus,
  Smartphone,
  Tv,
  User,
  X,
} from "lucide-react";
import { useEffect, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { ErrorPanel, LoadingRows } from "../../components/feedback";
import { Section } from "../../components/layout";
import { SettingsRow } from "../../components/SettingsRow";
import { DragHandle, SortableList } from "../../components/Sortable";
import { t } from "../../i18n";
import { searchSelectLabels } from "../../i18n/appkit-labels";
import { api } from "../../lib/api";
import { useAsync } from "../../lib/hooks";
import {
  deviceIconGroup,
  ruleConditions,
  ruleLimits,
  ruleTitle,
  shadowedBy,
  type ConditionKind,
} from "../../lib/qualityRules";
import type { QualityDevices, QualityRule } from "../../lib/types";
import { OrderButtons, RecordDrawer, toastError } from "../sources/form";
import { QualityHelp } from "./QualityHelp";
import { RuleEditor, localizedRule, useRuleNames, type RuleTarget } from "./RuleDrawer";
import { useQuality } from "./useQuality";

function conditionIcon(kind: ConditionKind, rule: QualityRule, devices: QualityDevices | null) {
  switch (kind) {
    case "library":
      return LibraryIcon;
    case "viewer":
      return User;
    case "device": {
      const group = deviceIconGroup(rule.devices, devices);
      if (group === "tv") return Tv;
      if (group === "mobile") return Smartphone;
      if (group === "computer") return Monitor;
      return MonitorSmartphone;
    }
    case "where":
      return rule.where === "away" ? Globe : Home;
    case "kind":
      return Clapperboard;
  }
}

function Outcome({
  profile,
  limits,
  emptyLabel,
}: {
  profile: string;
  limits: string[];
  emptyLabel?: string;
}) {
  return (
    <span className="flex flex-wrap items-center gap-1.5">
      {profile ? (
        <span className="inline-flex items-center gap-1.5 text-sm font-medium text-fg">
          <ArrowRight aria-hidden size={14} className="shrink-0 text-fg-muted" />
          <span>{profile}</span>
        </span>
      ) : (
        <span className="text-sm text-fg-muted">{emptyLabel ?? t("No change")}</span>
      )}
      {limits.map((limit) => (
        <Badge key={limit} variant="secondary">
          {limit}
        </Badge>
      ))}
    </span>
  );
}

/**
 * Releases › Rules: the one ordered list that decides who gets which profile,
 * with the two default rows fixed at the bottom.
 */
export function RulesTab() {
  const { config, quality, commit } = useQuality();
  const names = useRuleNames();
  const devices = useAsync((signal) => api.qualityDevices(signal), []);
  const [params, setParams] = useSearchParams();
  const [target, setTarget] = useState<RuleTarget | null>(null);
  const [defaultRow, setDefaultRow] = useState<"home" | "away" | null>(null);
  const [defaultDraft, setDefaultDraft] = useState("");
  const [defaultError, setDefaultError] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);
  /** The order being saved, shown until the list reloads. */
  const [moved, setMoved] = useState<QualityRule[] | null>(null);
  const [migratedDismissed, setMigratedDismissed] = useState(false);

  // Links from other pages: ?rule=<id>, ?default=home|away, or
  // ?new=1&library=<id>&viewer=<id>&profile=<name>.
  useEffect(() => {
    if (!quality) return;
    const rules = quality.rules ?? [];
    const ruleId = params.get("rule");
    const row = params.get("default");
    if (ruleId) {
      const index = rules.findIndex((r) => r.id === ruleId);
      if (index >= 0) setTarget({ index, rule: rules[index]! });
    } else if (row === "home" || row === "away") {
      setDefaultDraft(row === "home" ? quality.default : (quality.defaultAway ?? ""));
      setDefaultRow(row);
    } else if (params.get("new")) {
      const library = Number(params.get("library")) || undefined;
      const viewer = Number(params.get("viewer")) || undefined;
      setTarget({
        index: rules.length,
        rule: {
          id: "",
          ...(library ? { libraries: [library] } : {}),
          ...(viewer ? { viewers: [viewer] } : {}),
          profile: params.get("profile") ?? "",
        },
        lockLibrary: library,
        lockViewer: viewer,
      });
    } else {
      return;
    }
    setParams({}, { replace: true });
  }, [quality, params, setParams]);

  if (config.error) return <ErrorPanel error={config.error} onRetry={() => void config.reload()} />;
  if (!quality) return <LoadingRows rows={4} />;

  const rules = moved ?? quality.rules ?? [];
  const profiles = quality.profiles ?? [];
  const showMigratedNote = Boolean(quality.rulesMigrationNote) && !migratedDismissed;

  function dismissMigratedNote() {
    setMigratedDismissed(true);
    if (!quality?.rulesMigrationNote) return;
    void api
      .putSection("quality", { ...quality, rulesMigrationNote: false })
      .then(() => config.reload())
      .catch(() => {
        // Optimistic dismissal stays in place for this session.
      });
  }

  async function reorder(next: QualityRule[]) {
    if (!quality) return;
    setMoved(next);
    try {
      await commit({ ...quality, rules: next }, t("Rule order saved"));
    } catch (e) {
      toastError(t("Couldn't reorder rules"), e);
    } finally {
      setMoved(null);
    }
  }

  function openDefault(row: "home" | "away") {
    if (!quality) return;
    setDefaultDraft(row === "home" ? quality.default : (quality.defaultAway ?? ""));
    setDefaultError(null);
    setDefaultRow(row);
  }

  async function saveDefault() {
    if (!quality || !defaultRow) return;
    setSaving(true);
    setDefaultError(null);
    try {
      await commit(
        defaultRow === "home"
          ? { ...quality, default: defaultDraft }
          : { ...quality, defaultAway: defaultDraft },
        t("Default saved"),
      );
      setDefaultRow(null);
    } catch (e) {
      setDefaultError(e);
    } finally {
      setSaving(false);
    }
  }

  const fixedRow = (row: "home" | "away") => {
    const where = row === "home" ? t("At home") : t("Away");
    const profile = row === "home" ? quality.default : (quality.defaultAway ?? "");
    return (
      <li className="flex flex-wrap items-center gap-3 bg-bg-subtle/40 px-4 py-3 sm:px-5">
        <span className="flex w-10 shrink-0 items-center justify-center text-fg-muted">
          <Lock aria-hidden size={14} />
        </span>
        <div className="min-w-0 flex-1 basis-56">
          <p className="text-sm font-medium text-fg">
            {t("Everything else")} · {where}
          </p>
          <p className="text-xs text-fg-muted">{t("When no rule picks a profile")}</p>
        </div>
        <Outcome
          profile={profile}
          limits={[]}
          emptyLabel={row === "away" ? t("Same as at home") : undefined}
        />
        <Button
          variant="outline"
          size="sm"
          onClick={() => openDefault(row)}
          aria-label={row === "home" ? t("Edit the at home default") : t("Edit the away default")}
        >
          <Pencil size={13} /> {t("Edit")}
        </Button>
      </li>
    );
  };

  return (
    <>
      {showMigratedNote ? (
        <Alert variant="info" role="status" className="flex items-start justify-between gap-3">
          <AlertDescription>
            {t("Your quality settings were moved into rules. Nothing plays differently.")}
          </AlertDescription>
          <button
            type="button"
            aria-label={t("Dismiss")}
            onClick={dismissMigratedNote}
            className="shrink-0 rounded p-1 text-fg-muted hover:text-fg"
          >
            <X size={14} />
          </button>
        </Alert>
      ) : null}

      <Section
        title={t("Rules")}
        description={
          <QualityHelp
            summary={t(
              "Checked from the top. The first rule with a profile picks it; every matching limit applies.",
            )}
            detail={t(
              "A rule can pick a profile, set limits, or both. Limits only ever lower quality, and the strictest one wins. When no rule picks a profile, the two rows at the bottom decide.",
            )}
          />
        }
        actions={
          <Button
            size="sm"
            onClick={() => setTarget({ index: rules.length, rule: { id: "" } })}
            disabled={!profiles.length}
          >
            <Plus size={14} /> {t("Add rule")}
          </Button>
        }
        bodyClassName=""
      >
        {rules.length === 0 ? (
          <p className="border-b border-border px-5 py-4 text-sm text-fg-muted">
            {t("No rules yet. Everything uses the defaults below.")}
          </p>
        ) : (
          <SortableList
            items={rules}
            getId={(r) => r.id || `new-${rules.indexOf(r)}`}
            onReorder={(next) => void reorder(next)}
            renderItem={(raw, handle, i) => {
              const r = localizedRule(raw);
              const conditions = ruleConditions(r, names);
              const title = ruleTitle(r, names);
              const label = t("rule {n}", { n: i + 1 });
              const never = shadowedBy(rules, i, devices.data ?? null) >= 0;
              return (
                <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-border bg-surface px-3 py-3 sm:px-5">
                  <span className="flex w-10 shrink-0 items-center gap-1">
                    <DragHandle handle={handle} label={t("Reorder {name}", { name: label })} />
                    <span className="w-5 text-right text-sm tabular-nums text-fg-muted">
                      {i + 1}
                    </span>
                  </span>
                  <div className="min-w-0 flex-1 basis-56">
                    <p className="truncate text-sm font-medium text-fg">{title}</p>
                    <div className="mt-1.5 flex flex-wrap gap-1.5">
                      {conditions.length === 0 ? (
                        <Badge variant="outline">{t("Everything")}</Badge>
                      ) : (
                        conditions.map((c) => {
                          const Icon = conditionIcon(c.kind, raw, devices.data ?? null);
                          return (
                            <Badge key={c.kind} variant="outline" className="gap-1">
                              <Icon aria-hidden size={12} />
                              {c.label}
                            </Badge>
                          );
                        })
                      )}
                    </div>
                  </div>
                  <span className="flex flex-wrap items-center gap-2">
                    <Outcome profile={r.profile ?? ""} limits={ruleLimits(r)} />
                    {never ? <Badge variant="warning">{t("Never picks a profile")}</Badge> : null}
                  </span>
                  <span className="flex items-center gap-1">
                    <OrderButtons
                      label={label}
                      i={i}
                      n={rules.length}
                      disabled={moved !== null}
                      onMove={(d) => {
                        const next = [...rules];
                        [next[i], next[i + d]] = [next[i + d]!, next[i]!];
                        void reorder(next);
                      }}
                    />
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => setTarget({ index: i, rule: raw })}
                      aria-label={t("Edit rule {n}", { n: i + 1 })}
                    >
                      <Pencil size={13} /> {t("Edit")}
                    </Button>
                  </span>
                </div>
              );
            }}
          />
        )}
        <ul className="divide-y divide-border">
          {fixedRow("home")}
          {fixedRow("away")}
        </ul>
      </Section>
      <p className="text-sm text-fg-muted">
        {t("What a device can play is set in")}{" "}
        <Link className="text-primary underline" to="/playback/devices">
          {t("Playback › Devices")}
        </Link>
      </p>

      <RuleEditor
        quality={quality}
        commit={commit}
        target={target}
        onClose={() => setTarget(null)}
        onOpenRule={(index) => {
          const rule = (quality.rules ?? [])[index];
          if (rule) setTarget({ index, rule });
        }}
      />

      <RecordDrawer
        open={defaultRow !== null}
        onClose={() => !saving && setDefaultRow(null)}
        title={defaultRow === "away" ? t("Everything else, away") : t("Everything else, at home")}
        description={t("The profile when no rule picks one.")}
        onSave={() => void saveDefault()}
        saving={saving}
        saveLabel={t("Save")}
        error={defaultError}
      >
        <SettingsSection>
          <SettingsRow
            title={t("Profile")}
            control={
              <div className="w-72 max-w-full">
                <SearchSelect
                  {...searchSelectLabels()}
                  value={defaultDraft}
                  onChange={setDefaultDraft}
                  options={[
                    ...(defaultRow === "away" ? [{ value: "", label: t("Same as at home") }] : []),
                    ...profiles.map((p) => ({ value: p.name, label: p.name })),
                  ]}
                  ariaLabel={t("Profile")}
                />
              </div>
            }
          />
        </SettingsSection>
      </RecordDrawer>
    </>
  );
}
