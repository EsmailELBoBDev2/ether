import { copyText } from "../../components/CopyButton";
import { Button, Input, SettingsSection, Spinner } from "@braedonsaunders/appkit-ui";
import { StremioMark } from "../sources/marks";
import { ChevronDown, Copy } from "lucide-react";
import { useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import {
  PlexMark,
  JellyfinMark,
  EmbyMark,
  SiloMark,
  VelaMark,
  householdIcon,
} from "../../components/integration-brands";
import { ErrorPanel } from "../../components/feedback";
import { SaveBar, useDraft } from "../../components/SaveBar";
import { MoreOptions } from "../../components/ConfigSection";
import { SchemaField } from "./SchemaForm";
import { SERVER } from "./schema";
import { t } from "../../i18n";
import { api, apiFetch } from "../../lib/api";
import { useAsync } from "../../lib/hooks";
import { isLoopbackAddress, usableAddresses, type ReachabilityStatus } from "../apps/reachability";
import { appInstalled, appLabel, appRank } from "../apps/catalog";

type Frontend = {
  descriptor: { id: string; name: string; defaultPort?: number };
  enabled: boolean;
  status: ReachabilityStatus & { webPlayerReady?: boolean };
};

type ParsedAddress = {
  raw: string;
  protocol: string;
  host: string;
  port: string;
  pathname: string;
  isIPv6: boolean;
  isTailscale: boolean;
  isLocal: boolean;
  isPlexDirect: boolean;
};

function parseAddress(address: string): ParsedAddress | null {
  const trimmed = address.trim();
  if (!trimmed || isLoopbackAddress(trimmed)) return null;
  try {
    const url = new URL(trimmed.includes("://") ? trimmed : `http://${trimmed}`);
    const host = url.hostname.replace(/^\[|\]$/g, "").toLowerCase();
    const isIPv6 = host.includes(":");
    const isTailscale =
      /^100[.-](6[4-9]|[7-9]\d|1[01]\d|12[0-7])[.-]/.test(host) ||
      host.startsWith("fd7a:115c:a1e0:") ||
      host.endsWith(".ts.net");
    const isLocal =
      !isTailscale &&
      (/^(192[.-]168[.-]|10[.-]|172[.-](1[6-9]|2\d|3[01])[.-]|169[.-]254[.-])/.test(host) ||
        /^(f[cd][0-9a-f]{0,2}|fe[89ab][0-9a-f]):/i.test(host) ||
        host.endsWith(".local") ||
        (!isIPv6 && !host.includes(".")));
    return {
      raw: trimmed,
      protocol: url.protocol,
      host,
      port: url.port || (url.protocol === "https:" ? "443" : "80"),
      pathname: url.pathname || "/",
      isIPv6,
      isTailscale,
      isLocal,
      isPlexDirect: host.endsWith(".plex.direct"),
    };
  } catch {
    return null;
  }
}

function addressPort(address?: string): string {
  if (!address) return "";
  return parseAddress(address)?.port ?? "";
}

function localRank(item: ParsedAddress, preferredPort: string): number {
  let score = 0;
  if (item.isIPv6) score += 100;
  if (item.isPlexDirect) score += 20;
  if (/^192[.-]168[.-]/.test(item.host)) score += 0;
  else if (/^10[.-]255[.-]/.test(item.host)) score += 30;
  else if (/^10[.-]/.test(item.host)) score += 2;
  else if (/^172[.-]17[.-]/.test(item.host)) score += 25;
  else if (/^172[.-](1[6-9]|2\d|3[01])[.-]/.test(item.host)) score += 4;
  else score += 10;
  if (preferredPort && item.port === preferredPort) score -= 5;
  else if (item.pathname !== "/") score += 5;
  return score;
}

function secondaryLabel(item: ParsedAddress, primaryLocal?: ParsedAddress): string {
  const sharedPath = /^\/(jellyfin|emby|silo)(\/|$)/.test(item.pathname);
  if (item.isTailscale) {
    if (item.isIPv6) return t("Tailscale (IPv6)");
    if (item.protocol === "https:") return t("Tailscale (HTTPS)");
    if (sharedPath) return t("Tailscale (shared Ether port)");
    return t("Tailscale");
  }
  if (item.isLocal) {
    if (item.isPlexDirect) return t("Secure local URL");
    if (item.isIPv6) return t("Local network (IPv6)");
    if (primaryLocal && item.host === primaryLocal.host) {
      return sharedPath ? t("Shared Ether port") : t("Alternate port");
    }
    return t("Other network interface");
  }
  return t("Alternate remote URL");
}

function classifyAddresses(
  rawAddresses: string[],
  options: { preferredPort?: string; configuredRemote?: string },
): {
  primaryLocal?: ParsedAddress;
  primaryRemote?: ParsedAddress;
  others: { address: string; label: string }[];
} {
  const seen = new Set<string>();
  const parsed: ParsedAddress[] = [];
  for (const candidate of rawAddresses) {
    const item = parseAddress(candidate);
    if (!item || seen.has(item.raw)) continue;
    seen.add(item.raw);
    parsed.push(item);
  }

  const localCandidates = parsed
    .filter((item) => item.isLocal)
    .sort(
      (a, b) =>
        localRank(a, options.preferredPort ?? "") - localRank(b, options.preferredPort ?? ""),
    );
  const primaryLocal = localCandidates[0];

  const configuredRemote = options.configuredRemote ? parseAddress(options.configuredRemote) : null;
  let primaryRemote: ParsedAddress | undefined;
  if (configuredRemote && !configuredRemote.isLocal) {
    primaryRemote = configuredRemote;
  } else {
    primaryRemote = parsed.find((item) => !item.isLocal && !item.isTailscale);
  }

  const all = [...parsed];
  if (primaryRemote && !seen.has(primaryRemote.raw)) {
    seen.add(primaryRemote.raw);
    all.push(primaryRemote);
  }

  const others = all
    .filter((item) => item.raw !== primaryLocal?.raw && item.raw !== primaryRemote?.raw)
    .map((item) => ({
      address: item.raw,
      label: secondaryLabel(item, primaryLocal),
    }));

  return { primaryLocal, primaryRemote, others };
}

export function AppConnections() {
  const state = useAsync(() => apiFetch<Frontend[]>("/frontends"), []);
  return (
    <SettingsSection>
      <div className="flex flex-wrap items-start justify-between gap-3 border-b border-border px-5 py-4">
        <div>
          <h2 data-setting="how-apps-connect" className="text-sm font-medium">
            {t("How apps connect")}
          </h2>
          <p className="mt-1 text-sm text-fg-muted">
            {t("Use the address for your network. Port changes need a restart.")}
          </p>
        </div>
        <Link className="text-sm text-primary hover:underline" to="/apps">
          {t("Turn on more apps")}
        </Link>
      </div>
      {state.error ? (
        <ErrorPanel error={state.error} onRetry={() => void state.reload()} />
      ) : !state.data ? (
        <Spinner />
      ) : (
        <div className="grid items-start gap-4 p-4 xl:grid-cols-2">
          {[...state.data]
            .filter((app) => app.enabled && appInstalled(app))
            .sort((a, b) => appRank(a.descriptor.id) - appRank(b.descriptor.id))
            .map((app) => (
              <Connection key={app.descriptor.id} app={app} />
            ))}
        </div>
      )}
      <NetworkOptions
        plexEnabled={!!state.data?.some((app) => app.enabled && app.descriptor.id === "plex")}
      />
    </SettingsSection>
  );
}

function AddressLine({
  label,
  address,
  setting,
  copied,
  onCopy,
}: {
  label: string;
  address: string;
  setting?: string;
  copied: string;
  onCopy: (address: string) => void;
}) {
  return (
    <div className="flex min-w-0 items-center gap-2">
      <div className="min-w-0 flex-1">
        <p data-setting={setting} className="text-xs text-fg-muted">
          {label}
        </p>
        <p className="break-all font-mono text-sm">{address}</p>
      </div>
      <Button
        size="sm"
        variant="ghost"
        aria-label={t("Copy {address}", { address })}
        onClick={() => onCopy(address)}
      >
        <Copy size={16} />
        {copied === address ? t("Copied") : null}
      </Button>
    </div>
  );
}

function Connection({ app }: { app: Frontend }) {
  const { id, defaultPort } = app.descriptor;
  const name = appLabel(id, app.descriptor.name);
  const state = useAsync(
    async () =>
      id === "plex"
        ? (await api.getConfig()).server
        : apiFetch<Record<string, unknown>>(`/frontends/${id}/settings`),
    [id],
  );
  const { draft, setDraft, dirty, reset } = useDraft(state.data);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>();
  const [copied, setCopied] = useState("");
  const [showOther, setShowOther] = useState(false);
  const logos: Record<string, ReactNode> = {
    vela: <VelaMark />,
    plex: <PlexMark />,
    jellyfin: <JellyfinMark />,
    emby: <EmbyMark />,
    silo: <SiloMark />,
    stremio: <StremioMark />,
  };
  const addresses = [...new Set([...usableAddresses(app.status)])];
  const preferredPort = String(draft?.port ?? defaultPort ?? "");
  const rawPublicUrl =
    id !== "plex" && state.data && "publicUrl" in state.data ? state.data.publicUrl : undefined;
  const configuredPublicUrl = typeof rawPublicUrl === "string" ? rawPublicUrl.trim() : "";
  const wanCandidate = app.status.wanAddress?.trim() || "";
  const configuredRemote =
    configuredPublicUrl ||
    (wanCandidate && !parseAddress(wanCandidate)?.isTailscale ? wanCandidate : "");
  const { primaryLocal, primaryRemote, others } = classifyAddresses(addresses, {
    preferredPort,
    configuredRemote,
  });

  function handleCopy(address: string) {
    void copyText(address)
      .then((ok) => {
        if (ok) setCopied(address);
      })
      .catch((e: unknown) => setError(e));
  }

  return (
    <div className="min-w-0 space-y-3 rounded-xl border border-border bg-surface p-4">
      <div className="flex min-h-9 flex-wrap items-center gap-3">
        {logos[id] ?? householdIcon(id)}
        <div className="min-w-0 flex-1">
          <h3 className="font-medium">{name}</h3>
          {id === "plex" ? (
            <p className="text-xs text-fg-muted">
              {t("Also used by Stremio, DLNA, Files and M3U")}
            </p>
          ) : null}
        </div>
        {["plex", "jellyfin", "emby", "silo"].includes(id) ? (
          <label className="flex items-center gap-2 text-sm text-fg-muted">
            <span className="hidden sm:inline" data-setting="port">
              {t("Port")}
            </span>
            <Input
              className="w-20"
              aria-label={t("{app} port", { app: name })}
              type="number"
              min={1}
              max={65535}
              value={String(draft?.port ?? defaultPort ?? addressPort(addresses[0]))}
              disabled={!draft}
              onChange={(e) => draft && setDraft({ ...draft, port: Number(e.currentTarget.value) })}
            />
          </label>
        ) : (
          <span className="text-xs text-fg-muted">
            {t("Uses Ether’s port {port}", { port: addressPort(addresses[0]) })}
          </span>
        )}
      </div>
      {state.error ? <ErrorPanel error={state.error} onRetry={() => void state.reload()} /> : null}
      {primaryLocal ? (
        <AddressLine
          label={t("Local URL")}
          address={primaryLocal.raw}
          setting="local-url"
          copied={copied}
          onCopy={handleCopy}
        />
      ) : null}
      {primaryRemote ? (
        <AddressLine
          label={t("Remote URL")}
          address={primaryRemote.raw}
          setting="remote-url"
          copied={copied}
          onCopy={handleCopy}
        />
      ) : addresses.length > 0 ? (
        <p data-setting="remote-url" className="text-xs text-fg-muted">
          {t("Remote URL not reported")}
        </p>
      ) : null}
      {!primaryLocal && !primaryRemote && others.length === 0 ? (
        <p className="text-sm text-fg-muted">{t("No connection address reported yet")}</p>
      ) : null}
      {others.length > 0 ? (
        <div className="border-t border-border pt-2">
          <button
            type="button"
            className="inline-flex items-center gap-1.5 text-xs font-medium text-primary hover:underline"
            aria-expanded={showOther}
            onClick={() => setShowOther((v) => !v)}
          >
            <span>{t("Other addresses")}</span>
            <span className="text-fg-muted">({others.length})</span>
            <ChevronDown
              size={14}
              aria-hidden
              className={showOther ? "rotate-180 transition-transform" : "transition-transform"}
            />
          </button>
          {showOther ? (
            <div className="mt-2.5 space-y-2.5">
              {others.map((item) => (
                <AddressLine
                  key={item.address}
                  label={item.label}
                  address={item.address}
                  copied={copied}
                  onCopy={handleCopy}
                />
              ))}
            </div>
          ) : null}
        </div>
      ) : null}
      <SaveBar
        dirty={dirty}
        saving={saving}
        error={error}
        onDiscard={reset}
        onSave={async () => {
          if (!draft) return;
          setSaving(true);
          setError(undefined);
          try {
            if (id === "plex") await api.putSection("server", { port: draft.port });
            else
              await apiFetch(`/frontends/${id}/settings`, {
                method: "PUT",
                body: JSON.stringify(draft),
              });
            await state.reload();
          } catch (e) {
            setError(e);
          } finally {
            setSaving(false);
          }
        }}
      />
    </div>
  );
}

function NetworkOptions({ plexEnabled }: { plexEnabled: boolean }) {
  const state = useAsync(async () => (await api.getConfig()).server, []);
  const { draft, setDraft, dirty, reset } = useDraft(state.data);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>();
  const fields = SERVER.find((group) => group.id === "connect")?.more ?? [];
  return (
    <MoreOptions setting="connect" label={t("More options: How apps connect")}>
      {!plexEnabled && draft ? (
        <label className="flex items-center justify-between gap-4 p-4 text-sm">
          {t("Ether port")}
          <Input
            aria-label={t("Ether port")}
            type="number"
            min={1}
            max={65535}
            value={draft.port}
            onChange={(e) => setDraft({ ...draft, port: Number(e.currentTarget.value) })}
          />
        </label>
      ) : null}
      {draft
        ? fields.map((field) => (
            <SchemaField
              key={field.key}
              def={field}
              value={(draft as unknown as Record<string, unknown>)[field.key]}
              path={`server.${field.key}`}
              errors={{}}
              onChange={(value) => setDraft({ ...draft, [field.key]: value })}
            />
          ))
        : null}
      <SaveBar
        dirty={dirty}
        saving={saving}
        error={error ?? state.error}
        onDiscard={reset}
        onSave={async () => {
          if (!draft) return;
          setSaving(true);
          try {
            const values = draft as unknown as Record<string, unknown>;
            await api.putSection(
              "server",
              Object.fromEntries([
                ...fields.map((f) => [f.key, values[f.key]]),
                ...(!plexEnabled ? [["port", draft.port]] : []),
              ]),
            );
            await state.reload();
          } catch (e) {
            setError(e);
          } finally {
            setSaving(false);
          }
        }}
      />
    </MoreOptions>
  );
}
