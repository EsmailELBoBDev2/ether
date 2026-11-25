import { Button } from "@braedonsaunders/appkit-ui";
import { RefreshCw, Search } from "lucide-react";
import { useState, type FormEvent } from "react";
import { Artwork } from "../../../components/Artwork";
import { Drawer } from "../../../components/Drawer";
import { t } from "../../../i18n";
import { apiFetch } from "../../../lib/api";
import type { TitleCard, TitleDetail } from "../../../lib/contract";

export function TitleMetadataActions({
  title,
  onRefresh,
  onMatch,
}: {
  title: TitleDetail;
  onRefresh: () => Promise<unknown>;
  onMatch: (key: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [refreshStatus, setRefreshStatus] = useState<"done" | "failed" | null>(null);
  const path = `/catalog/title/${encodeURIComponent(title.key)}`;
  async function refresh() {
    setRefreshing(true);
    setRefreshStatus(null);
    try {
      await apiFetch(path + "/refresh", { method: "POST" });
      await onRefresh();
      setRefreshStatus("done");
    } catch {
      setRefreshStatus("failed");
    } finally {
      setRefreshing(false);
    }
  }
  return (
    <>
      <Button variant="secondary" size="sm" disabled={refreshing} onClick={() => void refresh()}>
        <RefreshCw size={15} className={refreshing ? "animate-spin" : undefined} />
        {t("Refresh metadata")}
      </Button>
      <Button variant="secondary" size="sm" disabled={refreshing} onClick={() => setOpen(true)}>
        <Search size={15} />
        {t("Fix match")}
      </Button>
      {refreshStatus === "done" ? (
        <span role="status" className="text-sm text-fg-muted">
          {t("Metadata refreshed")}
        </span>
      ) : refreshStatus === "failed" ? (
        <span role="alert" className="text-sm text-fg-muted">
          {t("Could not refresh metadata")}
        </span>
      ) : null}
      {open ? (
        <FixMatchDrawer
          title={title}
          path={path}
          onClose={() => setOpen(false)}
          onMatch={onMatch}
        />
      ) : null}
    </>
  );
}

function FixMatchDrawer({
  title,
  path,
  onClose,
  onMatch,
}: {
  title: TitleDetail;
  path: string;
  onClose: () => void;
  onMatch: (key: string) => void;
}) {
  const [query, setQuery] = useState(title.title);
  const [year, setYear] = useState("");
  const [results, setResults] = useState<(TitleCard & { tmdbId: number })[] | null>(null);
  const [picked, setPicked] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<"search" | "apply" | null>(null);
  async function search(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError(null);
    setPicked(null);
    setResults(null);
    try {
      const params = new URLSearchParams({ q: query.trim(), year });
      setResults(await apiFetch(`${path}/matches?${params}`));
    } catch {
      setError("search");
    } finally {
      setBusy(false);
    }
  }
  async function apply() {
    if (picked === null) return;
    setBusy(true);
    setError(null);
    try {
      const next = await apiFetch<{ key: string }>(path + "/match", {
        method: "PUT",
        body: JSON.stringify({ tmdbId: picked }),
      });
      onClose();
      onMatch(next.key);
    } catch {
      setError("apply");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Drawer open onClose={busy ? () => {} : onClose} title={t("Fix match")} size="md">
      <div className="space-y-4">
        <p className="text-sm text-fg-muted">
          {t(
            "Search TMDB for the correct title. Applying a match moves viewing history and saved releases to that title.",
          )}
        </p>
        <form className="space-y-3" onSubmit={(event) => void search(event)}>
          <div className="grid grid-cols-[minmax(0,1fr)_6rem] gap-3">
            <label className="grid gap-1 text-sm">
              {t("Title")}
              <input
                className="min-w-0 rounded-md border border-border bg-surface px-3 py-2"
                value={query}
                maxLength={300}
                onChange={(event) => setQuery(event.target.value)}
                required
              />
            </label>
            <label className="grid gap-1 text-sm">
              {t("Year")}
              <input
                className="min-w-0 rounded-md border border-border bg-surface px-3 py-2"
                value={year}
                type="number"
                min={1800}
                max={2200}
                onChange={(event) => setYear(event.target.value)}
              />
            </label>
          </div>
          <Button type="submit" variant="secondary" disabled={busy || !query.trim()}>
            <Search size={15} />
            {t("Search")}
          </Button>
        </form>
        {error ? (
          <p role="alert" className="text-sm text-fg-muted">
            {error === "search" ? t("Could not find matches") : t("Could not apply match")}
          </p>
        ) : null}
        {results?.length === 0 ? <p role="status">{t("No matches found")}</p> : null}
        {results?.length ? (
          <div className="space-y-2">
            {results.map((match) => (
              <label
                key={match.key}
                className="flex cursor-pointer items-center gap-3 rounded-lg border border-border p-2 has-checked:bg-bg-subtle"
              >
                <Artwork src={match.poster} alt="" className="aspect-[2/3] w-10 shrink-0 rounded" />
                <span className="min-w-0 flex-1">
                  <span className="block font-medium">{match.title}</span>
                  <span className="text-sm text-fg-muted">{match.year || ""}</span>
                </span>
                <input
                  type="radio"
                  name="tmdb-match"
                  checked={picked === match.tmdbId}
                  disabled={busy}
                  onChange={() => setPicked(match.tmdbId)}
                />
              </label>
            ))}
            <Button disabled={busy || picked === null} onClick={() => void apply()}>
              {t("Apply")}
            </Button>
          </div>
        ) : null}
      </div>
    </Drawer>
  );
}
