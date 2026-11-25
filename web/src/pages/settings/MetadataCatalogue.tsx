import { Badge, Button } from "@braedonsaunders/appkit-ui";
import { Tv } from "lucide-react";
import { useState } from "react";
import { Drawer } from "../../components/Drawer";
import { secretConfigured } from "../../components/IntegrationAccount";
import { IntegrationAppCard } from "../../components/IntegrationAppCard";
import { TmdbMark } from "../../components/integration-brands";
import { MetadataKeyFields } from "../../components/MetadataKeyFields";
import type { TestState } from "../../components/fields";
import { t } from "../../i18n";
import { builtinMetadataSources, type BuiltinMetadataSource } from "../../lib/metadataSources";

export function MetadataCatalogue({
  source,
  identity,
  storedKey,
  bundled,
  off,
  onToggle,
  locked,
  onSave,
}: {
  source: BuiltinMetadataSource;
  identity: { id: string; name: string };
  storedKey: string;
  bundled: boolean;
  off: boolean;
  onToggle?: (enabled: boolean) => Promise<void>;
  locked: boolean;
  onSave: (key: string) => Promise<void>;
}) {
  const [key, setKey] = useState<string | null>(null);
  const [test, setTest] = useState<TestState>({ status: "idle" });
  const [saving, setSaving] = useState(false);
  const [saveFailed, setSaveFailed] = useState(false);
  const provider = builtinMetadataSources[source];
  const configured = !off && (bundled || secretConfigured(storedKey));
  const busy = saving || test.status === "running";

  async function save() {
    if (key === null) return;
    setSaving(true);
    setSaveFailed(false);
    try {
      await onSave(key.trim());
      setKey(null);
    } catch {
      setSaveFailed(true);
    } finally {
      setSaving(false);
    }
  }

  async function toggle() {
    if (!onToggle) return;
    setSaving(true);
    setSaveFailed(false);
    try {
      await onToggle(off);
    } catch {
      setSaveFailed(true);
    } finally {
      setSaving(false);
    }
  }

  async function runTest() {
    if (key === null || (!bundled && !key.trim())) return;
    setTest({ status: "running" });
    const failed: TestState = { status: "fail", message: t("Could not verify the API key.") };
    try {
      const result = await provider.test(key.trim());
      setTest(result.ok ? { status: "ok", durationMs: result.durationMs } : failed);
    } catch {
      setTest(failed);
    }
  }

  return (
    <>
      <IntegrationAppCard
        identity={identity}
        logo={source === "tmdb" ? <TmdbMark /> : <Tv size={24} aria-hidden />}
        description={provider.description()}
        status={
          <Badge variant={configured ? "success" : "secondary"}>
            {off
              ? t("Off")
              : bundled
                ? t("Built in")
                : configured
                  ? t("Key saved")
                  : t("Not configured")}
          </Badge>
        }
        action={
          <>
            <Button
              size="sm"
              variant="outline"
              disabled={locked || busy}
              onClick={() => {
                setKey(storedKey);
                setTest({ status: "idle" });
                setSaveFailed(false);
              }}
            >
              {bundled
                ? t("Use your own key")
                : secretConfigured(storedKey)
                  ? t("Manage {app}", { app: provider.name })
                  : t("Set up {app}", { app: provider.name })}
            </Button>
            {onToggle && (configured || off) ? (
              <Button
                size="sm"
                variant="ghost"
                disabled={locked || busy}
                onClick={() => void toggle()}
              >
                {off ? t("Turn on") : t("Turn off")}
              </Button>
            ) : null}
            {saveFailed && key === null ? (
              <p role="alert" className="w-full text-sm text-danger">
                {t("Couldn't save the change")}
              </p>
            ) : null}
          </>
        }
      />
      {key !== null ? (
        <Drawer
          open
          title={t("Manage {app}", { app: provider.name })}
          onClose={() => {
            if (!busy) setKey(null);
          }}
          footer={
            <div className="flex justify-end gap-2">
              <Button variant="ghost" disabled={busy} onClick={() => setKey(null)}>
                {t("Cancel")}
              </Button>
              <Button
                disabled={locked || busy || key.trim() === storedKey}
                onClick={() => void save()}
              >
                {t("Save")}
              </Button>
            </div>
          }
        >
          <MetadataKeyFields
            source={source}
            value={key}
            test={test}
            disabled={locked || saving}
            allowEmptyTest={bundled}
            onChange={(value) => {
              setKey(value);
              setTest({ status: "idle" });
            }}
            onTest={() => void runTest()}
          />
          {source === "tvdb" && configured && !key.trim() ? (
            <p className="mt-3 text-sm text-fg-muted">
              {t("Saving an empty key disconnects this source.")}
            </p>
          ) : null}
          {saveFailed ? (
            <p role="alert" className="mt-3 text-danger">
              {t("Couldn't save the change")}
            </p>
          ) : null}
        </Drawer>
      ) : null}
    </>
  );
}
