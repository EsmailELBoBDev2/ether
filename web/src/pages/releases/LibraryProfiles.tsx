import { Badge, SearchSelect, toast } from "@braedonsaunders/appkit-ui";
import { useState } from "react";
import { Link } from "react-router-dom";
import { Section } from "../../components/layout";
import { t } from "../../i18n";
import { searchSelectLabels } from "../../i18n/appkit-labels";
import { errorMessage } from "../../lib/api";
import { librariesApi, type Library } from "../libraries/api";
import type { QualityProfile } from "../../lib/types";

/**
 * Whether Ether's resolver picks releases for a library's titles, so the
 * library chooses a release profile (core.Library.UsesReleaseProfiles).
 * A plugin library's plugin chooses its own releases.
 */
export function usesReleaseProfiles(lib: Pick<Library, "source">): boolean {
  return lib.source === "builtin" || lib.source === "catalogue" || lib.source === "collection";
}

/** The libraries that chose a profile, for viewers at home or away. */
export function librariesUsing(libraries: Library[], name: string): Library[] {
  return libraries.filter(
    (lib) =>
      usesReleaseProfiles(lib) && (lib.qualityProfile === name || lib.awayQualityProfile === name),
  );
}

/** A library's profile picker: "Default (…)" or one of the profiles. */
export function ProfilePicker({
  value,
  onChange,
  profiles,
  fallback,
  ariaLabel,
  disabled,
}: {
  value: string;
  onChange: (value: string) => void;
  profiles: QualityProfile[];
  /** What "" means, e.g. "Default (4K)". */
  fallback: string;
  ariaLabel: string;
  disabled?: boolean;
}) {
  const names = profiles.map((p) => p.name);
  return (
    <div className="w-48">
      <SearchSelect
        {...searchSelectLabels()}
        value={value && names.includes(value) ? value : ""}
        onChange={onChange}
        options={[{ value: "", label: fallback }, ...names.map((n) => ({ value: n, label: n }))]}
        ariaLabel={ariaLabel}
        searchable={false}
        disabled={disabled}
      />
    </div>
  );
}

/** "Default (4K)" for the At home picker. */
export function homeFallback(defaultName: string): string {
  return defaultName ? t("Default ({profile})", { profile: defaultName }) : t("Default");
}

/** "Default away (…)" or "Same as at home" for the Away picker. */
export function awayFallback(remoteName: string): string {
  return remoteName ? t("Default away ({profile})", { profile: remoteName }) : t("Same as at home");
}

/** The line a plugin library shows instead of a profile picker. */
export function PluginReleaseChoice({ library }: { library: Library }) {
  const plugin = library.pluginName?.trim() || library.pluginId?.trim() || "";
  if (!plugin) {
    return <span className="text-sm text-fg-muted">{t("Its plugin chooses its releases.")}</span>;
  }
  // One translatable sentence; the plugin name becomes the link.
  const mark = "\u0000";
  const [before, after = ""] = t("Release choice is set by {plugin}", { plugin: mark }).split(mark);
  return (
    <span className="text-sm text-fg-muted" data-testid="plugin-release-choice">
      {before}
      {library.pluginId ? (
        <Link
          className="font-medium text-primary hover:underline"
          to={`/plugins/installed?plugin=${encodeURIComponent(library.pluginId)}`}
        >
          {plugin}
        </Link>
      ) : (
        plugin
      )}
      {after}
    </span>
  );
}

/**
 * "Used by": every library with the profile it uses at home and away. A
 * change saves at once. Libraries whose plugin chooses its own releases say
 * so instead of showing pickers.
 */
export function LibraryProfilesSection({
  libraries,
  profiles,
  defaultName,
  remoteName,
  onChanged,
}: {
  libraries: Library[];
  profiles: QualityProfile[];
  defaultName: string;
  remoteName: string;
  onChanged: () => Promise<void> | void;
}) {
  const [saving, setSaving] = useState<number | null>(null);
  const save = async (lib: Library, body: Record<string, string>) => {
    setSaving(lib.id);
    try {
      await librariesApi.patch(lib.id, body);
      toast.success(t("{library} updated", { library: lib.name }));
      await onChanged();
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      setSaving(null);
    }
  };
  return (
    <Section
      title={t("Used by")}
      description={t(
        "Each library picks its profile for viewers at home and away. A library left on Default uses the Default profile.",
      )}
      bodyClassName=""
    >
      <ul className="divide-y divide-border">
        {libraries.map((lib) => (
          <li
            key={lib.id}
            data-library={lib.key}
            className="flex flex-wrap items-center justify-between gap-3 px-5 py-3"
          >
            <span className="min-w-0">
              <span className="block truncate text-sm font-medium text-fg">{lib.name}</span>
              {lib.hidden ? (
                <Badge variant="secondary" className="mt-1">
                  {t("Hidden")}
                </Badge>
              ) : null}
            </span>
            {usesReleaseProfiles(lib) ? (
              <span className="flex flex-wrap items-center gap-3">
                <span className="flex items-center gap-2">
                  <span className="text-xs font-medium text-fg-muted">{t("At home")}</span>
                  <ProfilePicker
                    value={lib.qualityProfile ?? ""}
                    onChange={(v) => void save(lib, { qualityProfile: v })}
                    profiles={profiles}
                    fallback={homeFallback(defaultName)}
                    ariaLabel={t("{library} at home", { library: lib.name })}
                    disabled={saving === lib.id}
                  />
                </span>
                <span className="flex items-center gap-2">
                  <span className="text-xs font-medium text-fg-muted">{t("Away")}</span>
                  <ProfilePicker
                    value={lib.awayQualityProfile ?? ""}
                    onChange={(v) => void save(lib, { awayQualityProfile: v })}
                    profiles={profiles}
                    fallback={awayFallback(remoteName)}
                    ariaLabel={t("{library} away", { library: lib.name })}
                    disabled={saving === lib.id}
                  />
                </span>
              </span>
            ) : (
              <PluginReleaseChoice library={lib} />
            )}
          </li>
        ))}
      </ul>
    </Section>
  );
}
