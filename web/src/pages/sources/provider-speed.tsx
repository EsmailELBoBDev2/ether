import { Button, Spinner } from "@braedonsaunders/appkit-ui";
import { useEffect, useRef, useState } from "react";
import { t } from "../../i18n";
import { api } from "../../lib/api";
import { fixed, formatBitrate, formatNumber } from "../../lib/format";
import type { ProviderSpeedResult } from "../../lib/types";

type Kind = "usenet" | "debrid";

export function useProviderSpeed(kind: Kind, id: string | undefined) {
  const [result, setResult] = useState<ProviderSpeedResult>();
  const [running, setRunning] = useState(false);
  const request = useRef<AbortController | undefined>(undefined);

  useEffect(() => {
    setResult(undefined);
    setRunning(false);
    return () => {
      request.current?.abort();
      request.current = undefined;
    };
  }, [id]);

  async function run() {
    if (!id || request.current) return;
    const controller = new AbortController();
    request.current = controller;
    setResult(undefined);
    setRunning(true);
    try {
      const next = await api.testProviderSpeed(kind, id, controller.signal);
      if (!controller.signal.aborted) setResult(next);
    } catch {
      if (!controller.signal.aborted) setResult({ status: "unavailable" });
    } finally {
      if (request.current === controller) {
        request.current = undefined;
        setRunning(false);
      }
    }
  }

  return { result, running, run };
}

export function ProviderSpeedButton({
  running,
  disabled,
  run,
}: {
  running: boolean;
  disabled: boolean;
  run: () => Promise<void>;
}) {
  return (
    <Button
      size="sm"
      variant="secondary"
      disabled={disabled || running}
      onClick={() => void run()}
      title={t("Uses the saved provider settings")}
    >
      {running && <Spinner size={14} />}
      {t("Test speed")}
    </Button>
  );
}

function outcome(status: string): string {
  switch (status) {
    case "empty_vault":
      return t("The NZB vault is empty. Play a Usenet title and try again.");
    case "no_readable_nzb":
      return t("Ether could not read a recent NZB from the vault.");
    case "no_recent_file":
      return t("Play a cached file on this debrid account and try again.");
    case "paused":
      return t("This provider is temporarily paused. Try again later.");
    case "busy":
      return t("No spare connection is available. Try again when playback is idle.");
    case "already_running":
      return t("A speed test is already running for this provider.");
    default:
      return t("Ether could not measure this provider’s speed.");
  }
}

function speedRate(mbps: number | undefined): string {
  return t("{rate} MB/s", { rate: formatNumber(mbps ?? 0) });
}

export function ProviderSpeedReport({
  kind,
  result,
  running,
}: {
  kind: Kind;
  result?: ProviderSpeedResult;
  running: boolean;
}) {
  return (
    <div role="status" aria-live="polite" className={running || result ? "w-full" : "contents"}>
      {running && (
        <p className="text-sm text-muted">{t("Measuring speed for about 10 seconds…")}</p>
      )}
      {result && (
        <div className="space-y-1 rounded-md border border-border px-3 py-2 text-sm">
          {result.status === "ok" ? (
            <>
              <p className="font-medium">
                {t("Enough for streams up to about {rate}", {
                  rate: formatBitrate((result.streamMbps ?? 0) * 1e6),
                })}
              </p>
              <p className="text-muted">
                {t("{total} total · {perConnection} per connection", {
                  total: speedRate(result.mbps),
                  perConnection: speedRate(result.mbpsPerConnection),
                })}
              </p>
              {!!result.typicalMbps && (
                <p className="text-muted">
                  {(result.streamMbps ?? 0) >= result.typicalMbps
                    ? t("Above your recent playback rate of {rate}", {
                        rate: formatBitrate(result.typicalMbps * 1e6),
                      })
                    : t("Below your recent playback rate of {rate}", {
                        rate: formatBitrate(result.typicalMbps * 1e6),
                      })}
                </p>
              )}
            </>
          ) : (
            <p>{outcome(result.status)}</p>
          )}
          {!!result.seconds && (
            <p className="text-muted">
              {t("Connections used: {used} · Opened: {opened} · Refused: {refused}", {
                used: fixed(result.connections ?? 0, 0),
                opened: fixed(result.opened ?? 0, 0),
                refused: fixed(result.refused ?? 0, 0),
              })}
              {kind === "usenet" && (
                <>
                  {" · "}
                  {t("Article misses: {count}", { count: fixed(result.misses ?? 0, 0) })}
                </>
              )}
            </p>
          )}
          {result.spareOnly && <p className="text-muted">{t("Spare connections only")}</p>}
        </div>
      )}
    </div>
  );
}
