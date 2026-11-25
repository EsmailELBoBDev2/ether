import { Badge, Button } from "@braedonsaunders/appkit-ui";
import { Tv } from "lucide-react";
import { useState } from "react";
import { Drawer } from "../../components/Drawer";
import { Field, SecretInput, TestButton, type TestState } from "../../components/fields";
import { secretConfigured } from "../../components/IntegrationAccount";
import { IntegrationAppCard } from "../../components/IntegrationAppCard";
import { t } from "../../i18n";
import { api } from "../../lib/api";
import {
  catalogueCapNote,
  catalogueCountLabel,
  tvdbSourceError,
} from "../libraries/CatalogueSources";
import type { CatalogueSource } from "../libraries/api";

export function TvdbCatalogue({
  storedKey,
  source,
  locked,
  onSave,
}: {
  storedKey: string;
  source?: CatalogueSource;
  locked: boolean;
  onSave: (key: string) => Promise<void>;
}) {
  const [key, setKey] = useState<string | null>(null);
  const [test, setTest] = useState<TestState>({ status: "idle" });
  const [saving, setSaving] = useState(false);
  const [saveFailed, setSaveFailed] = useState(false);
  const configured = secretConfigured(storedKey);
  const busy = saving || test.status === "running";
  const save = async () => {
    if (key == null) return;
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
  };
  const runTest = async () => {
    if (key == null) return;
    setTest({ status: "running" });
    try {
      const result = await api.testTVDB(key.trim());
      setTest(
        result.ok
          ? { status: "ok", durationMs: result.durationMs }
          : { status: "fail", message: tvdbSourceError(result.error) },
      );
    } catch {
      setTest({ status: "fail", message: tvdbSourceError() });
    }
  };

  return (
    <>
      <IntegrationAppCard
        identity={{ id: "tvdb", name: "TheTVDB" }}
        logo={<Tv size={24} aria-hidden />}
        description={t("Adds TV shows linked to TMDB as a library source.")}
        status={
          configured ? (
            <Badge variant={source?.lastError ? "warning" : "success"}>
              {source?.lastError ? t("Needs attention") : t("Key saved")}
            </Badge>
          ) : (
            <Badge variant="warning">{t("Needs an API key")}</Badge>
          )
        }
        details={
          configured && source?.lastError ? (
            <p className="text-sm text-danger">{tvdbSourceError(source.lastError)}</p>
          ) : source?.itemCount !== undefined ? (
            <div className="text-sm text-fg-muted">
              <p>{catalogueCountLabel(source)}</p>
              <p>{catalogueCapNote(source)}</p>
            </div>
          ) : undefined
        }
        action={
          <Button
            size="sm"
            variant="outline"
            disabled={locked}
            onClick={() => {
              setKey(storedKey);
              setTest({ status: "idle" });
              setSaveFailed(false);
            }}
          >
            {configured
              ? t("Manage {app}", { app: "TheTVDB" })
              : t("Set up {app}", { app: "TheTVDB" })}
          </Button>
        }
      />
      <Drawer
        open={key !== null}
        title={t("Manage {app}", { app: "TheTVDB" })}
        onClose={() => {
          if (!busy) setKey(null);
        }}
        footer={
          <div className="flex justify-end gap-2">
            <Button variant="ghost" disabled={busy} onClick={() => setKey(null)}>
              {t("Cancel")}
            </Button>
            <Button
              disabled={locked || busy || key?.trim() === storedKey}
              onClick={() => void save()}
            >
              {t("Save")}
            </Button>
          </div>
        }
      >
        <div className="space-y-4">
          <Field label={t("TheTVDB API key")}>
            <SecretInput
              ariaLabel={t("TheTVDB API key")}
              value={key ?? ""}
              disabled={locked || busy}
              onChange={(value) => {
                setKey(value);
                setTest({ status: "idle" });
              }}
            />
          </Field>
          <TestButton
            state={test}
            onTest={() => void runTest()}
            disabled={locked || !key?.trim()}
          />
          {saveFailed ? (
            <p role="alert" className="text-danger">
              {t("Couldn't save the change")}
            </p>
          ) : null}
        </div>
      </Drawer>
    </>
  );
}
