import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { expect, it } from "vitest";
import { calls, renderApp } from "./harness";
import { RELEASE_SEARCH } from "../pages/settings/schema";
import { destinations } from "../lib/sitemap";
import { settingsIndex } from "../lib/settings-index";

it("gives the checker its own searchable destination and inline controls", async () => {
  renderApp("/releases/check");
  const result = await screen.findByTestId("quality-headline", {}, { timeout: 10000 });
  expect(result).toHaveTextContent("4K");
  expect(screen.getAllByTestId("quality-headline")).toHaveLength(1);
  // The step bar shows here too, with Check quality as the current step.
  const steps = screen.getByRole("navigation", { name: "How quality works" });
  expect(steps.querySelector("[aria-current=step]")).toHaveTextContent("Check quality");
  const viewer = screen.getByRole("button", { name: "Viewer" });
  const row = viewer.closest(".grid")!;
  expect(row).toHaveClass("sm:grid-cols-3");
  expect(within(row as HTMLElement).getByRole("button", { name: "App" })).toBeVisible();
  expect(within(row as HTMLElement).getByRole("radio", { name: "Away" })).toBeVisible();
  expect(destinations().some((d) => d.href === "/releases/check")).toBe(true);
  expect(settingsIndex().find((d) => d.title === "Check quality")?.href).toBe("/releases/check");
});

it("keeps the four search settings visible", () => {
  expect(RELEASE_SEARCH[0]!.more).toBeUndefined();
  expect(RELEASE_SEARCH[0]!.fields.map((f) => f.key)).toEqual([
    "maxCandidates",
    "healthSamples",
    "searchCacheTtl",
    "unavailableCooloff",
  ]);
});

it("orders profile sections before away, source order, and versions", async () => {
  renderApp("/releases/profiles");
  await screen.findByRole("button", { name: "Add profile" }, { timeout: 10000 });
  await screen.findByText("Same-quality versions to offer", {}, { timeout: 10000 });
  const flow = screen.getByRole("navigation", { name: "How quality works" });
  expect(flow.querySelector("[aria-current=step]")).toHaveTextContent("Profiles");
  expect(within(flow).queryByRole("link", { name: /Profiles/ })).not.toBeInTheDocument();
  const headings = screen.getAllByRole("heading").map((h) => h.textContent);
  const order = ["Profiles", "Which source goes first", "Version menu"].map((s) =>
    headings.indexOf(s),
  );
  expect(order.every((i) => i >= 0)).toBe(true);
  expect(order).toEqual([...order].sort((a, b) => a - b));
  expect(screen.queryByTestId("quality-headline")).not.toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: "Import a preset quality profile" }),
  ).not.toBeInTheDocument();
});

it("collapses guide sync to one line until a profile is imported", async () => {
  renderApp("/releases/profiles");
  expect(
    await screen.findByText(
      "Applies once you import a profile from TRaSH Guides or Profilarr.",
      {},
      { timeout: 10000 },
    ),
  ).toBeInTheDocument();
  expect(
    screen.queryByRole("switch", { name: "Keep imported profiles in sync" }),
  ).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Sync now" })).not.toBeInTheDocument();
});

it("saves the automatic sync cadence as a patch and runs it", async () => {
  const { fake } = renderApp("/releases/profiles", {}, (f) => {
    f.state.config.quality.profiles![0]!.import = {
      source: "trash",
      app: "radarr",
      profile: "UHD Bluray + WEB",
    };
  });
  await screen.findByRole("switch", { name: "Keep imported profiles in sync" }, { timeout: 10000 });
  // The title appears once, in the card header with its switch.
  expect(screen.getAllByText("Keep imported profiles in sync")).toHaveLength(1);
  expect(screen.getByText("Never synced")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Check for changes" })).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("switch", { name: "Keep imported profiles in sync" }));
  fireEvent.click(screen.getByRole("button", { name: "Check for changes" }));
  fireEvent.click(await screen.findByRole("option", { name: "Every 3 days" }, { timeout: 4000 }));
  fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
  await waitFor(() => expect(calls(fake, "PUT", "/config/quality")).toHaveLength(1));
  expect(calls(fake, "PUT", "/config/quality")[0]!.body).toEqual({
    sync: { enabled: true, interval: "72h" },
  });
  await waitFor(() => expect(fake.state.config.quality.sync?.interval).toBe("72h"));

  fireEvent.click(screen.getByRole("button", { name: "Sync now" }));
  await waitFor(() => expect(calls(fake, "POST", "/releases/quality/sync")).toHaveLength(1));
  expect(await screen.findByText("2 formats added")).toBeInTheDocument();
});

it("shows a sync failure without a settings save bar and clears it on retry", async () => {
  let fail = true;
  renderApp("/releases/profiles", {}, (f) => {
    f.state.config.quality.profiles![0]!.import = {
      source: "trash",
      app: "radarr",
      profile: "UHD Bluray + WEB",
    };
    const original = f.fetch;
    f.fetch = async (input, init) => {
      if (String(input).endsWith("/releases/quality/sync") && init?.method === "POST" && fail) {
        fail = false;
        return new Response(
          JSON.stringify({ error: { code: "validation", message: "Sync failed" } }),
          {
            status: 400,
          },
        );
      }
      return original(input, init);
    };
  });
  const sync = await screen.findByRole("button", { name: "Sync now" });
  fireEvent.click(sync);
  expect(await screen.findByText("Sync failed")).toBeVisible();
  expect(screen.queryByRole("region", { name: "Unsaved changes" })).not.toBeInTheDocument();
  fireEvent.click(sync);
  expect(await screen.findByText("2 formats added")).toBeVisible();
  expect(screen.queryByText("Sync failed")).not.toBeInTheDocument();
});

it("settles dangling client rules when saving a profile", async () => {
  const { fake } = renderApp("/releases/profiles", {}, (server) => {
    server.state.config.quality.default = "Gone";
    server.state.config.quality.rules = [
      { product: "Plex for Apple TV", profile: "4K" },
      { product: "Roku", profile: "TRaSH UHD", maxHeight: 1080 },
      { platform: "iOS", mediaType: "movie", profile: "TRaSH Remux" },
      { userId: 7, mediaType: "tv", profile: "TRaSH WEB" },
    ];
  });
  fireEvent.click(await screen.findByRole("button", { name: "Edit 4K" }, { timeout: 10000 }));
  const drawer = await screen.findByRole("dialog", { name: "4K" });
  fireEvent.click(within(drawer).getByRole("button", { name: "Save profile" }));
  await waitFor(() => expect(calls(fake, "PUT", "/config/quality")).toHaveLength(1));
  // A Default or rule naming a missing profile falls back to the first one.
  expect(fake.state.config.quality.default).toBe("4K");
  expect(fake.state.config.quality.rules).toEqual([
    { product: "Plex for Apple TV", profile: "4K" },
    { product: "Roku", profile: "4K", maxHeight: 1080 },
    { platform: "iOS", mediaType: "movie", profile: "4K" },
    { userId: 7, mediaType: "tv", profile: "4K" },
  ]);
});
