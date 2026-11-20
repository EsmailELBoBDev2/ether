import { VelaDistribution } from "./VelaDistribution";
import { VelaSkins } from "./VelaSkins";
import {
  Button,
  Input,
  SettingsSection,
  SearchSelect,
  Switch,
  confirmDialog,
} from "@braedonsaunders/appkit-ui";
import { FileUp, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useApp } from "../../app-context";
import { ErrorPanel, LoadingRows } from "../../components/feedback";
import { MoreOptions } from "../../components/ConfigSection";
import { DomainPage, useRouteTab } from "../../components/layout";
import { SaveBar, useDraft } from "../../components/SaveBar";
import { SettingsRow } from "../../components/SettingsRow";
import { t } from "../../i18n";
import { useOwnerViewerPath } from "../../components/pickers";
import { api, apiBlob, apiFetch, imageNeedsFetch } from "../../lib/api";
import { isPlainObject } from "../../lib/format";
import { useAsync, useInterval } from "../../lib/hooks";
import { viewerStatus } from "../viewers/ViewerStatus";
import { XtreamLogins } from "./XtreamLogins";
import { AwayFromHome } from "./AwayFromHome";
import { PlexApp } from "./PlexApp";
import { appLabel, appPath, appRank } from "./catalog";
import { AddressChip, StatusHelp } from "../../components/AppStatusList";
import {
  AppAddressCard,
  AppOverviewRow,
  ConnectDeviceCard,
  DEDICATED_PORT_APPS,
  fetchEtherConfig,
  OverviewPortConflictChip,
  ServerAddressPanel,
  type HubFrontendInfo,
} from "./ConnectionHub";
import { usableAddresses } from "./reachability";
import { validateCustomUrls } from "./plex-account";

type FrontendInfo = HubFrontendInfo;

export type VelaWordmark = {
  url: string;
  width: number;
  height: number;
  dark?: string;
};
type Settings = {
  port?: number;
  publicUrl?: string;
  serverName?: string;
  discovery?: boolean;
  quickConnect?: boolean;
  publicViewerPicker?: boolean;
  viewerId?: number;
  defaultSkinId?: string;
  force?: boolean;
  appName?: string;
  wordmark?: VelaWordmark | null;
  homeRows?: boolean;
  homeRowCount?: number;
  homeRowKinds?: string[];
};

const VELA_MAX_APP_NAME_CHARS = 40;
const VELA_MAX_WORDMARK_BYTES = 512 * 1024;
const VELA_WORDMARK_ACCEPT = "image/png,image/svg+xml,image/webp,.png,.svg,.webp";

function isSupportedWordmarkFile(file: File): boolean {
  if (["image/png", "image/svg+xml", "image/webp"].includes(file.type)) return true;
  return /\.(png|svg|webp)$/i.test(file.name);
}

const HOME_ROW_KINDS = [
  ["because-you-watched", () => t("Because you watched")],
  ["trending", () => t("Trending")],
  ["top-10", () => t("Top 10 for you")],
  ["fans-also-loved", () => t("Fans also loved")],
  ["new-releases", () => t("New releases")],
  ["hidden-gems", () => t("Hidden gems")],
  ["more-from-person", () => t("People you watch")],
  ["popular-on-server", () => t("Popular on this server")],
] as const;
const pathFor = appPath;
const protocolFor = (tab: string) => (tab === "files" ? "webdav" : tab);
const nameFor = (info: FrontendInfo) => appLabel(info.descriptor.id, info.descriptor.name);

function viewerPortalAddress(chosen?: string): string {
  if (chosen) {
    try {
      const u = new URL(chosen);
      const prefix = u.pathname.replace(/\/(jellyfin|emby|silo)\/?$/, "");
      if (prefix !== u.pathname) {
        return `${u.origin}${prefix}/ether/me`;
      }
    } catch {
      // ignore invalid URL
    }
  }
  if (
    typeof window !== "undefined" &&
    window.location?.origin &&
    window.location.origin !== "null"
  ) {
    return `${window.location.origin}/ether/me`;
  }
  return "/ether/me";
}

function WordmarkImage({
  src,
  alt,
  width,
  height,
  className,
}: {
  src: string;
  alt: string;
  width?: number;
  height?: number;
  className?: string;
}) {
  const needsFetch = imageNeedsFetch(src);
  const [blobUrl, setBlobUrl] = useState<string>();
  useEffect(() => {
    if (!needsFetch) {
      setBlobUrl(undefined);
      return;
    }
    const controller = new AbortController();
    let created: string | undefined;
    void apiBlob(src, controller.signal)
      .then((blob) => {
        if (!controller.signal.aborted) {
          created = URL.createObjectURL(blob);
          setBlobUrl(created);
        }
      })
      .catch(() => setBlobUrl(undefined));
    return () => {
      controller.abort();
      if (created) URL.revokeObjectURL(created);
    };
  }, [needsFetch, src]);
  const resolved = needsFetch ? blobUrl : src;
  if (!resolved) return null;
  return <img src={resolved} alt={alt} width={width} height={height} className={className} />;
}

function VelaSurfacePreview({
  mode,
  displayName,
  wordmarkUrl,
  width,
  height,
}: {
  mode: "light" | "dark";
  displayName: string;
  wordmarkUrl?: string;
  width?: number;
  height?: number;
}) {
  const dark = mode === "dark";
  return (
    <div
      data-testid={`vela-preview-${mode}`}
      className={
        dark
          ? "overflow-hidden rounded-xl border border-white/10 bg-[#0b0d12] text-[#f5f7fa]"
          : "overflow-hidden rounded-xl border border-black/10 bg-[#f6f7f9] text-[#111318]"
      }
    >
      <div
        className={
          dark
            ? "flex items-center justify-between border-b border-white/10 bg-[#131720] px-3 py-1.5 text-xs text-[#9aa3b5]"
            : "flex items-center justify-between border-b border-black/10 bg-[#ebedf2] px-3 py-1.5 text-xs text-[#525866]"
        }
      >
        <div className="flex min-w-0 items-center gap-2">
          <span className="flex items-center gap-1" aria-hidden="true">
            <span className="size-2 rounded-full bg-current opacity-25" />
            <span className="size-2 rounded-full bg-current opacity-25" />
            <span className="size-2 rounded-full bg-current opacity-25" />
          </span>
          <span
            className={
              dark
                ? "truncate rounded-md bg-[#0b0d12] px-2 py-0.5 font-medium text-[#e6eaf2]"
                : "truncate rounded-md bg-white px-2 py-0.5 font-medium text-[#1c2028]"
            }
          >
            {displayName}
          </span>
        </div>
        <span className="shrink-0 text-[11px] uppercase tracking-wider opacity-75">
          {dark ? t("Dark") : t("Light")}
        </span>
      </div>
      <div className="flex items-center justify-between gap-4 px-4 py-3.5">
        <div className="flex min-h-8 min-w-0 items-center">
          {wordmarkUrl ? (
            <WordmarkImage
              src={wordmarkUrl}
              alt={displayName}
              width={width}
              height={height}
              className="max-h-7 w-auto max-w-[180px] object-contain"
            />
          ) : (
            <span className="truncate text-base font-semibold tracking-tight">{displayName}</span>
          )}
        </div>
        <div
          aria-hidden="true"
          className={
            dark
              ? "flex shrink-0 items-center gap-2.5 text-xs text-[#9aa3b5]"
              : "flex shrink-0 items-center gap-2.5 text-xs text-[#596070]"
          }
        >
          <span
            className={
              dark
                ? "rounded-full bg-white/10 px-2.5 py-0.5 font-medium text-[#f5f7fa]"
                : "rounded-full bg-black/8 px-2.5 py-0.5 font-medium text-[#111318]"
            }
          >
            {t("Home")}
          </span>
          <span>{t("Movies")}</span>
          <span>{t("Shows")}</span>
        </div>
      </div>
    </div>
  );
}

function VelaBranding({
  appName,
  wordmark,
  onAppNameChange,
  onWordmarkChange,
}: {
  appName?: string;
  wordmark?: VelaWordmark;
  onAppNameChange: (name: string) => void;
  onWordmarkChange: (wordmark?: VelaWordmark) => void;
}) {
  const lightInputRef = useRef<HTMLInputElement>(null);
  const darkInputRef = useRef<HTMLInputElement>(null);
  const [uploading, setUploading] = useState<"light" | "dark" | null>(null);
  const [uploadError, setUploadError] = useState<unknown>();

  async function handleWordmarkUpload(variant: "light" | "dark", file?: File) {
    if (!file) return;
    if (!isSupportedWordmarkFile(file)) {
      setUploadError(new Error(t("Wordmark must be a PNG, SVG, or WebP image.")));
      return;
    }
    if (file.size > VELA_MAX_WORDMARK_BYTES) {
      setUploadError(new Error(t("Wordmark must be 512 KB or smaller.")));
      return;
    }
    setUploading(variant);
    setUploadError(undefined);
    try {
      const form = new FormData();
      form.append("file", file);
      const uploaded = await apiFetch<{ url: string; width: number; height: number }>(
        "/frontends/vela/wordmark",
        { method: "POST", body: form },
      );
      if (variant === "light") {
        onWordmarkChange({
          url: uploaded.url,
          width: uploaded.width,
          height: uploaded.height,
          ...(wordmark?.dark ? { dark: wordmark.dark } : {}),
        });
      } else if (wordmark) {
        onWordmarkChange({
          ...wordmark,
          dark: uploaded.url,
        });
      }
    } catch (err) {
      setUploadError(err);
    } finally {
      setUploading(null);
    }
  }

  const displayName = (appName ?? "").trim() || "Vela";

  return (
    <SettingsSection title={t("Branding")}>
      <SettingsRow
        title={<label htmlFor="vela-app-name">{t("App name")}</label>}
        description={t(
          "Shown instead of Vela in the app, browser tab, and installed web app. Up to 40 characters.",
        )}
        control={
          <Input
            id="vela-app-name"
            aria-label={t("App name")}
            maxLength={VELA_MAX_APP_NAME_CHARS}
            autoComplete="off"
            placeholder="Vela"
            className="w-72"
            value={appName ?? ""}
            onChange={(e) => onAppNameChange(e.currentTarget.value)}
          />
        }
      />
      <SettingsRow
        stacked
        title={t("Wordmark")}
        description={t(
          "Replaces the text name in Vela's header and sign-in screen. PNG, SVG, or WebP up to 512 KB and 1024×256 px.",
        )}
        control={
          <div className="space-y-3 pt-1">
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="flex flex-col justify-between gap-3 rounded-xl border border-border bg-surface/60 p-3.5">
                <div className="flex items-center justify-between gap-2">
                  <span className="text-sm font-medium text-fg">{t("Light background")}</span>
                  <span className="text-xs text-fg-muted">
                    {wordmark ? `${wordmark.width}×${wordmark.height} px` : t("Not set")}
                  </span>
                </div>
                <div className="flex h-14 items-center justify-center rounded-lg border border-black/10 bg-[#f6f7f9] px-3">
                  {wordmark ? (
                    <WordmarkImage
                      src={wordmark.url}
                      alt={t("Light wordmark")}
                      width={wordmark.width}
                      height={wordmark.height}
                      className="max-h-8 w-auto max-w-full object-contain"
                    />
                  ) : (
                    <span className="text-xs text-[#596070]">{t("Uses app name")}</span>
                  )}
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    disabled={uploading != null}
                    onClick={() => lightInputRef.current?.click()}
                  >
                    <FileUp size={14} />
                    {uploading === "light"
                      ? t("Uploading…")
                      : wordmark
                        ? t("Replace")
                        : t("Upload")}
                  </Button>
                  {wordmark ? (
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      disabled={uploading != null}
                      aria-label={t("Remove light wordmark")}
                      onClick={() => {
                        setUploadError(undefined);
                        onWordmarkChange(undefined);
                      }}
                    >
                      <X size={14} />
                      {t("Remove")}
                    </Button>
                  ) : null}
                  <input
                    ref={lightInputRef}
                    type="file"
                    accept={VELA_WORDMARK_ACCEPT}
                    className="sr-only"
                    aria-label={t("Upload light wordmark")}
                    onChange={(e) => {
                      const file = e.currentTarget.files?.[0];
                      e.currentTarget.value = "";
                      void handleWordmarkUpload("light", file);
                    }}
                  />
                </div>
              </div>

              <div className="flex flex-col justify-between gap-3 rounded-xl border border-border bg-surface/60 p-3.5">
                <div className="flex items-center justify-between gap-2">
                  <span className="text-sm font-medium text-fg">{t("Dark background")}</span>
                  <span className="text-xs text-fg-muted">
                    {wordmark?.dark
                      ? t("Custom dark image")
                      : wordmark
                        ? t("Optional · uses light image")
                        : t("Optional")}
                  </span>
                </div>
                <div className="flex h-14 items-center justify-center rounded-lg border border-white/10 bg-[#0b0d12] px-3">
                  {wordmark?.dark ? (
                    <WordmarkImage
                      src={wordmark.dark}
                      alt={t("Dark wordmark")}
                      width={wordmark.width}
                      height={wordmark.height}
                      className="max-h-8 w-auto max-w-full object-contain"
                    />
                  ) : wordmark ? (
                    <WordmarkImage
                      src={wordmark.url}
                      alt={t("Light wordmark fallback")}
                      width={wordmark.width}
                      height={wordmark.height}
                      className="max-h-8 w-auto max-w-full object-contain opacity-85"
                    />
                  ) : (
                    <span className="text-xs text-[#9aa3b5]">{t("Uses app name")}</span>
                  )}
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    disabled={!wordmark || uploading != null}
                    onClick={() => darkInputRef.current?.click()}
                  >
                    <FileUp size={14} />
                    {uploading === "dark"
                      ? t("Uploading…")
                      : wordmark?.dark
                        ? t("Replace")
                        : t("Upload")}
                  </Button>
                  {wordmark?.dark ? (
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      disabled={uploading != null}
                      aria-label={t("Remove dark wordmark")}
                      onClick={() => {
                        setUploadError(undefined);
                        const { dark: _dark, ...lightOnly } = wordmark;
                        onWordmarkChange(lightOnly);
                      }}
                    >
                      <X size={14} />
                      {t("Remove")}
                    </Button>
                  ) : null}
                  <input
                    ref={darkInputRef}
                    type="file"
                    accept={VELA_WORDMARK_ACCEPT}
                    className="sr-only"
                    aria-label={t("Upload dark wordmark")}
                    onChange={(e) => {
                      const file = e.currentTarget.files?.[0];
                      e.currentTarget.value = "";
                      void handleWordmarkUpload("dark", file);
                    }}
                  />
                </div>
              </div>
            </div>
            {uploadError != null ? <ErrorPanel error={uploadError} /> : null}
          </div>
        }
      />
      <SettingsRow
        stacked
        title={t("Live preview")}
        description={t("How your name and wordmark appear on light and dark Vela surfaces.")}
        control={
          <div data-testid="vela-branding-preview" className="grid gap-3 pt-1 sm:grid-cols-2">
            <VelaSurfacePreview
              mode="light"
              displayName={displayName}
              wordmarkUrl={wordmark?.url}
              width={wordmark?.width}
              height={wordmark?.height}
            />
            <VelaSurfacePreview
              mode="dark"
              displayName={displayName}
              wordmarkUrl={wordmark ? (wordmark.dark ?? wordmark.url) : undefined}
              width={wordmark?.width}
              height={wordmark?.height}
            />
          </div>
        }
      />
    </SettingsSection>
  );
}

function ProtocolSettings({
  id,
  info,
  hidden,
  serverPort,
  serverName,
  toggleBusy,
  onToggle,
  onReloadFrontends,
}: {
  id: string;
  info?: FrontendInfo;
  hidden: boolean;
  serverPort: number;
  serverName: string;
  toggleBusy: boolean;
  onToggle: (enabled: boolean) => void;
  onReloadFrontends: () => Promise<unknown>;
}) {
  const state = useAsync(() => apiFetch<Settings>(`/frontends/${id}/settings`), [id]);
  const { draft, setDraft, dirty, reset } = useDraft(state.data);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>();
  const editablePort = DEDICATED_PORT_APPS.has(id);

  async function save() {
    if (!draft) return;
    const publicUrlError = validateCustomUrls(draft.publicUrl ? [draft.publicUrl] : []);
    if (publicUrlError) {
      setError(new Error(publicUrlError));
      return;
    }
    const { serverName: _serverName, port: rawPort, ...body } = draft;
    if (editablePort) {
      const nextPort = rawPort ?? info?.descriptor.defaultPort;
      if (nextPort == null || !Number.isInteger(nextPort) || nextPort < 1 || nextPort > 65535) {
        setError(new Error(t("Enter a port between 1 and 65535.")));
        return;
      }
      (body as Settings).port = nextPort;
    }
    if (id === "vela") {
      const trimmedName = (body.appName ?? "").trim();
      if ([...trimmedName].length > VELA_MAX_APP_NAME_CHARS) {
        setError(new Error(t("App name must be 40 characters or fewer.")));
        return;
      }
      if (trimmedName) body.appName = trimmedName;
      else delete body.appName;
      if (!body.wordmark) delete body.wordmark;
    }
    setSaving(true);
    setError(undefined);
    try {
      await apiFetch(`/frontends/${id}/settings`, { method: "PUT", body: JSON.stringify(body) });
      await Promise.all([state.reload(), onReloadFrontends()]);
    } catch (err) {
      setError(err);
    } finally {
      setSaving(false);
    }
  }

  const connectionCards =
    !hidden && info ? (
      <>
        <AppAddressCard
          info={info}
          configuredRemote={draft?.publicUrl}
          port={draft ? draft.port : info.descriptor.defaultPort}
          onPortChange={draft && editablePort ? (port) => setDraft({ ...draft, port }) : undefined}
          serverPort={serverPort}
          busy={toggleBusy}
          onToggle={onToggle}
        />
        <ConnectDeviceCard
          info={info}
          configuredRemote={draft?.publicUrl}
          preferredPort={String(draft?.port ?? info.descriptor.defaultPort ?? serverPort)}
          serverName={serverName}
        />
      </>
    ) : null;

  if (id === "vela" && !info?.enabled && !info?.status.webPlayerReady) {
    return (
      <>
        <div hidden={hidden} className="space-y-6">
          {connectionCards}
          {!hidden ? <VelaDistribution /> : null}
        </div>
      </>
    );
  }
  if (state.error) {
    return (
      <>
        <div hidden={hidden} className="space-y-6">
          {connectionCards}
          {!hidden ? <ErrorPanel error={state.error} onRetry={() => void state.reload()} /> : null}
        </div>
      </>
    );
  }
  if (!draft) {
    return (
      <>
        <div hidden={hidden} className="space-y-6">
          {connectionCards}
          {!hidden ? <LoadingRows /> : null}
        </div>
      </>
    );
  }

  const named = ["jellyfin", "emby", "silo", "dlna"].includes(id);
  const lanViewer = info?.descriptor.auth === "none_lan";
  const more: {
    key: "discovery" | "publicViewerPicker";
    label: string;
    help: string;
  }[] = [];
  if (named) {
    more.push({
      key: "discovery",
      label: t("Let apps find this server"),
      help: t("Apps on your network can find this server without typing an address."),
    });
  }
  if (id === "jellyfin" || id === "emby") {
    more.push({
      key: "publicViewerPicker",
      label: t("Show local viewer names on the sign-in screen"),
      help: t("Leave this off on a network you do not trust."),
    });
  }

  const awayFromHome =
    id !== "plex" &&
    id !== "dlna" &&
    !lanViewer &&
    (id !== "vela" || info?.status.webPlayerReady) ? (
      <SettingsSection title={t("Away from home")}>
        <SettingsRow
          title={<label htmlFor={`${id}-public-url`}>{t("Public address")}</label>}
          description={t(
            "The address viewers use to reach this app from outside your network, e.g. https://jelly.example.com",
          )}
          control={
            <Input
              id={`${id}-public-url`}
              aria-label={t("Public address")}
              type="url"
              inputMode="url"
              autoComplete="off"
              spellCheck={false}
              className="w-72"
              placeholder={`https://${id === "jellyfin" ? "jelly" : id}.example.com`}
              value={draft.publicUrl ?? ""}
              onChange={(e) => setDraft({ ...draft, publicUrl: e.currentTarget.value })}
            />
          }
        />
      </SettingsSection>
    ) : null;

  return (
    <>
      <div hidden={hidden} className="space-y-6">
        {connectionCards}
        {id === "vela" ? awayFromHome : null}
        {id === "vela" && !hidden ? (
          <>
            <VelaDistribution />
            <VelaBranding
              appName={draft.appName}
              wordmark={draft.wordmark ?? undefined}
              onAppNameChange={(next) => {
                const nextDraft = { ...draft };
                if (next) nextDraft.appName = next;
                else delete nextDraft.appName;
                setDraft(nextDraft);
              }}
              onWordmarkChange={(next) => {
                const nextDraft = { ...draft };
                if (next) nextDraft.wordmark = next;
                else delete nextDraft.wordmark;
                setDraft(nextDraft);
              }}
            />
            <VelaSkins
              defaultSkinId={draft.defaultSkinId ?? "cinematic"}
              force={Boolean(draft.force)}
              onDefaultSkinChange={(defaultSkinId) => setDraft({ ...draft, defaultSkinId })}
              onForceChange={(force) => setDraft({ ...draft, force })}
            />
          </>
        ) : null}
        {["jellyfin", "emby", "silo"].includes(id) ? (
          <SignInSection
            info={info}
            quickConnect={
              id === "jellyfin"
                ? {
                    checked: Boolean(draft.quickConnect),
                    onChange: (next) => setDraft({ ...draft, quickConnect: next }),
                  }
                : undefined
            }
          />
        ) : null}
        {id === "xtream" && info?.enabled ? <XtreamLogins /> : null}
        {id !== "vela" ? awayFromHome : null}
        {id === "jellyfin" || id === "emby" ? (
          <SettingsSection
            title={t("Home")}
            description={t("{app} apps show this viewer's For You rows.", {
              app: protocolName(id),
            })}
          >
            <SettingsRow
              title={t("Personalized rows")}
              description={t("Each viewer gets their own rows, filled from what they watch.")}
              control={
                <Switch
                  checked={draft.homeRows !== false}
                  aria-label={t("Personalized rows")}
                  onChange={(e) => setDraft({ ...draft, homeRows: e.currentTarget.checked })}
                />
              }
            />
            <MoreOptions label={t("More options: {title}", { title: t("Home") })}>
              <SettingsRow
                title={<label htmlFor={`${id}-home-rows`}>{t("How many rows")}</label>}
                description={t(
                  "About four is enough for a home screen. The most relevant rows come first.",
                )}
                control={
                  <Input
                    id={`${id}-home-rows`}
                    aria-label={t("How many rows")}
                    type="number"
                    min={0}
                    max={12}
                    value={draft.homeRowCount ?? 4}
                    onChange={(e) =>
                      setDraft({ ...draft, homeRowCount: Number(e.currentTarget.value) })
                    }
                  />
                }
              />
              <SettingsRow
                title={t("Which rows")}
                description={t(
                  "Leave them all on to use every personalized row. Turn one off to hide that kind.",
                )}
              />
              {HOME_ROW_KINDS.map(([kind, label]) => {
                const selected = draft.homeRowKinds ?? HOME_ROW_KINDS.map(([id]) => id);
                return (
                  <SettingsRow
                    key={kind}
                    title={label()}
                    control={
                      <Switch
                        checked={selected.includes(kind)}
                        aria-label={label()}
                        onChange={(e) => {
                          const current = new Set(
                            draft.homeRowKinds ?? HOME_ROW_KINDS.map(([id]) => id),
                          );
                          if (e.currentTarget.checked) current.add(kind);
                          else current.delete(kind);
                          const next = HOME_ROW_KINDS.map(([id]) => id).filter((id) =>
                            current.has(id),
                          );
                          setDraft({
                            ...draft,
                            homeRowKinds: next.length === HOME_ROW_KINDS.length ? undefined : next,
                          });
                        }}
                      />
                    }
                  />
                );
              })}
            </MoreOptions>
          </SettingsSection>
        ) : null}
        {lanViewer && id !== "dlna" ? (
          <SettingsSection title={t("Browse as")}>
            <LANViewer
              value={draft.viewerId}
              onChange={(viewerId) => setDraft((old) => (old ? { ...old, viewerId } : old))}
            />
          </SettingsSection>
        ) : null}
        {named ? (
          <SettingsSection
            title={id === "dlna" ? t("What the TV shows") : t("Find this server")}
            description={
              id === "dlna"
                ? t("DLNA players browse the library as one viewer.")
                : t("How {app} apps find this server.", { app: protocolName(id) })
            }
          >
            {id === "dlna" ? (
              <LANViewer
                createLivingRoomGuest
                value={draft.viewerId}
                onChange={(viewerId) => setDraft((old) => (old ? { ...old, viewerId } : old))}
              />
            ) : null}
            {more.length ? (
              <MoreOptions
                label={t("More options: {title}", {
                  title: id === "dlna" ? t("What the TV shows") : t("Find this server"),
                })}
              >
                {more.map((field) => (
                  <SettingsRow
                    key={field.key}
                    title={field.label}
                    description={field.help}
                    control={
                      <Switch
                        checked={Boolean(draft[field.key])}
                        aria-label={field.label}
                        onChange={(e) =>
                          setDraft({ ...draft, [field.key]: e.currentTarget.checked })
                        }
                      />
                    }
                  />
                ))}
              </MoreOptions>
            ) : null}
          </SettingsSection>
        ) : null}
      </div>
      <SaveBar dirty={dirty} saving={saving} onSave={save} onDiscard={reset} error={error} />
    </>
  );
}

function protocolName(id: string): string {
  switch (id) {
    case "jellyfin":
      return "Jellyfin";
    case "emby":
      return "Emby";
    case "vela":
      return "Vela";
    case "silo":
      return "Silo";
    default:
      return id;
  }
}

function LANViewer({
  value,
  onChange,
  createLivingRoomGuest = false,
}: {
  value?: number;
  onChange: (id?: number) => void;
  createLivingRoomGuest?: boolean;
}) {
  const viewers = useAsync(() => api.viewers(), []);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  async function createGuest() {
    if (
      !(await confirmDialog({
        title: t("Create a living-room guest"),
        message: t("Starts with the Kids preset (PG). You can change it on the guest's profile."),
        confirmLabel: t("Create a living-room guest"),
      }))
    )
      return;
    setBusy(true);
    setError(undefined);
    try {
      const viewer = await apiFetch<{ id: number }>("/frontends/dlna/guest", {
        method: "POST",
        body: "{}",
      });
      await viewers.reload();
      onChange(viewer.id);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="space-y-3 px-5 py-4">
      <SearchSelect
        ariaLabel={t("Browse as")}
        placeholder={t("No viewer selected")}
        value={value == null ? "" : String(value)}
        onChange={(next) => onChange(next ? Number(next) : undefined)}
        clearable
        searchable
        options={(viewers.data ?? [])
          .filter((v) => ["owner", "active"].includes(viewerStatus(v)))
          .map((v) => ({ value: String(v.id), label: v.title || v.username }))}
      />
      {createLivingRoomGuest ? (
        <Button disabled={busy} onClick={() => void createGuest()}>
          {t("Create a living-room guest")}
        </Button>
      ) : null}
      {error != null && <ErrorPanel error={error} />}
    </div>
  );
}

/** How viewers sign in to Jellyfin, Emby and Silo, with Jellyfin's Quick Connect switch. */
function SignInSection({
  info,
  quickConnect,
}: {
  info?: FrontendInfo;
  quickConnect?: { checked: boolean; onChange: (next: boolean) => void };
}) {
  const ownerPath = useOwnerViewerPath();
  const portalURL = viewerPortalAddress(info ? usableAddresses(info.status)[0] : undefined);
  return (
    <SettingsSection title={t("How people sign in")}>
      <SettingsRow
        title={t("Viewer accounts")}
        description={t("People sign in with their Ether viewer account.")}
        control={
          <Button asChild variant="outline" size="sm">
            <Link to="/viewers">{t("Viewers")}</Link>
          </Button>
        }
      />
      <SettingsRow
        title={t("Quick Connect")}
        description={t("Approve the code in the viewer portal.")}
        control={
          <div className="flex flex-wrap items-center gap-3">
            <AddressChip address={portalURL} />
            {quickConnect ? (
              <Switch
                checked={quickConnect.checked}
                aria-label={t("Quick Connect")}
                onChange={(e) => quickConnect.onChange(e.currentTarget.checked)}
              />
            ) : null}
          </div>
        }
      />
      <SettingsRow
        title={
          <span className="flex items-center gap-2">
            {t("App password")}
            <StatusHelp
              text={
                info?.descriptor.id === "emby"
                  ? t("The Emby for TV app isn't supported.")
                  : t("Infuse needs an app password.")
              }
            />
          </span>
        }
        description={t("Create an app password under Connect apps in your account.")}
        control={
          <Button asChild variant="outline" size="sm">
            <Link to={ownerPath}>{t("Open your account")}</Link>
          </Button>
        }
      />
    </SettingsSection>
  );
}

export default function Apps() {
  const { boot, dashboard } = useApp();
  const state = useAsync((signal) => apiFetch<FrontendInfo[]>("/frontends", { signal }), []);
  const configState = useAsync(() => fetchEtherConfig(), []);
  const { tab: routeTab, section: routeSection } = useParams();
  const navigate = useNavigate();
  const apps = [...(state.data ?? [])].sort(
    (a, b) => appRank(a.descriptor.id) - appRank(b.descriptor.id),
  );
  const plexInfo = apps.find((info) => info.descriptor.id === "plex");
  const plexOn = plexInfo?.enabled === true;
  const matchedApp =
    routeTab && routeTab !== "overview"
      ? apps.find((info) => pathFor(info.descriptor.id) === routeTab)
      : undefined;
  const tabs = [
    { key: "overview", label: t("Overview") },
    ...apps
      .filter(
        (info) =>
          info.enabled || info.descriptor.id === "vela" || pathFor(info.descriptor.id) === routeTab,
      )
      .map((info) => ({ key: pathFor(info.descriptor.id), label: nameFor(info) })),
  ];
  const routed = useRouteTab(tabs, "overview");
  const tab = routeTab === "plex" && plexInfo ? "plex" : routed;
  const [visited, setVisited] = useState(() => new Set([tab]));
  useEffect(() => {
    setVisited((old) => (old.has(tab) ? old : new Set([...old, tab])));
  }, [tab]);
  useEffect(() => {
    if (!state.data || !routeTab || routeTab === "overview") return;
    const known = state.data.some((info) => pathFor(info.descriptor.id) === routeTab);
    if (!known) navigate("/apps/overview", { replace: true });
  }, [state.data, routeTab, navigate]);
  useInterval(() => void state.reload(), 10_000);
  const [error, setError] = useState<unknown>();
  const [busy, setBusy] = useState<string>();

  async function toggle(info: FrontendInfo, enabled: boolean) {
    setBusy(info.descriptor.id);
    setError(undefined);
    try {
      await apiFetch(`/frontends/${info.descriptor.id}/enabled`, {
        method: "PUT",
        body: JSON.stringify({ enabled }),
      });
      await state.reload();
    } catch (err) {
      setError(err);
    } finally {
      setBusy(undefined);
    }
  }

  const serverPort = configState.data?.server?.port ?? 32400;
  const serverName = configState.data?.server?.friendlyName || boot?.serverName || "Ether";

  return (
    <DomainPage
      title={t("Apps")}
      base="/apps"
      tabs={tabs}
      active={tab}
      actions={
        tab === "overview" && state.data ? <OverviewPortConflictChip apps={apps} /> : undefined
      }
    >
      {tab === "overview" &&
        (state.error ? (
          <ErrorPanel error={state.error} onRetry={() => void state.reload()} />
        ) : !state.data ? (
          <LoadingRows />
        ) : (
          <div className="space-y-6">
            <ServerAddressPanel
              frontends={apps}
              configData={configState.data}
              configError={configState.error}
              onReloadConfig={() => configState.reload()}
            />
            {plexOn &&
            isPlainObject(dashboard?.sections.remote) &&
            dashboard.sections.remote.claimed === false ? (
              <p className="text-sm text-fg">
                <Link className="text-primary underline" to="/apps/plex/account">
                  {t("Finish connecting Plex")}
                </Link>
              </p>
            ) : null}
            <SettingsSection
              title={t("Apps")}
              description={t(
                "Turn apps on or off, copy their address, or open an app to set up devices and ports.",
              )}
            >
              <div className="divide-y divide-border">
                {apps.map((info) => (
                  <AppOverviewRow
                    key={info.descriptor.id}
                    info={info}
                    busy={busy === info.descriptor.id}
                    disabled={busy != null && busy !== info.descriptor.id}
                    onToggle={(enabled) => void toggle(info, enabled)}
                  />
                ))}
              </div>
            </SettingsSection>
            <AwayFromHome />
          </div>
        ))}
      {(visited.has("plex") || tab === "plex") && (
        <PlexApp
          hidden={tab !== "plex"}
          section={routeSection}
          info={plexInfo}
          busy={busy === "plex"}
          onToggle={plexInfo ? (enabled) => void toggle(plexInfo, enabled) : undefined}
          onReloadFrontends={() => state.reload()}
        />
      )}
      {apps
        .filter((info) => info.descriptor.id !== "plex")
        .filter((info) => {
          const key = pathFor(info.descriptor.id);
          return (
            visited.has(key) || tab === key || matchedApp?.descriptor.id === info.descriptor.id
          );
        })
        .map((info) => {
          const key = pathFor(info.descriptor.id);
          return (
            <div key={key} hidden={tab !== key}>
              <ProtocolSettings
                id={protocolFor(key)}
                info={info}
                hidden={tab !== key}
                serverPort={serverPort}
                serverName={serverName}
                toggleBusy={busy === info.descriptor.id}
                onToggle={(enabled) => void toggle(info, enabled)}
                onReloadFrontends={() => state.reload()}
              />
            </div>
          );
        })}
      {error != null && <ErrorPanel error={error} />}
    </DomainPage>
  );
}
