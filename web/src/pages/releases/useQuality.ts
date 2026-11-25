import { toast } from "@braedonsaunders/appkit-ui";
import { useCallback } from "react";
import { useApp } from "../../app-context";
import { api } from "../../lib/api";
import { useAsync } from "../../lib/hooks";
import type { EtherConfig, QualityRule } from "../../lib/types";

export type Quality = EtherConfig["quality"];

/**
 * Repoints any client rules (other than `keepIndex`) whose profile no longer
 * exists to the Default profile, keeping device matchers and limits intact
 * so unrelated dangling rules never block a quality save.
 */
export function settleQualityRules(
  quality: Quality,
  keepIndex = -1,
): { rules: QualityRule[]; keptAt: number } {
  const profiles = quality.profiles ?? [];
  const rules = quality.rules ?? [];
  if (profiles.length === 0) return { rules, keptAt: keepIndex };
  const exists = (name: string) => Boolean(name) && profiles.some((p) => p.name === name);
  const home = exists(quality.default) ? quality.default : profiles[0]!.name;
  return {
    rules: rules.map((r, i) =>
      i === keepIndex || !r.profile || exists(r.profile) ? r : { ...r, profile: home },
    ),
    keptAt: keepIndex,
  };
}

/** The quality section (profiles, client rules, defaults) with a save that reloads it. */
export function useQuality() {
  const { refreshDashboard } = useApp();
  const config = useAsync(() => api.getConfig(), []);
  const quality = config.data?.quality ?? null;
  const commit = useCallback(
    async (next: Quality, message: string) => {
      await api.putSection("quality", next);
      toast.success(message);
      await config.reload();
      void refreshDashboard();
    },
    [config, refreshDashboard],
  );
  /** Reload after a change saved elsewhere, such as a profile import. */
  const refresh = useCallback(async () => {
    await config.reload();
    void refreshDashboard();
  }, [config, refreshDashboard]);
  return { config, quality, commit, refresh };
}
