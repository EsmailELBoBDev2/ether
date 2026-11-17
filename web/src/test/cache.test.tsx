import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { presetForFreeBytes } from "../pages/cache/presets";
import { calls, renderApp } from "./harness";
import { renderAppWith } from "./routes";

const WAIT = { timeout: 4000 };
async function chooseProfile(name: RegExp) {
  fireEvent.click(await screen.findByRole("button", { name: /Choose a cache profile/ }, WAIT));
  fireEvent.click(await screen.findByRole("button", { name }, WAIT));
  fireEvent.click(screen.getByRole("button", { name: "Review profile" }));
}

async function pickToolbarOption(label: string, optionName: string | RegExp) {
  fireEvent.click(await screen.findByRole("button", { name: label }, WAIT));
  const menu = await screen.findByRole("menu", { name: label }, WAIT);
  fireEvent.click(within(menu).getByRole("menuitemradio", { name: optionName }));
}

describe("cache preset suggestion", () => {
  it("picks Everyday when the disk can hold 32 GiB, and Feather when it cannot", () => {
    expect(presetForFreeBytes(1.1 * 1024 ** 4)?.key).toBe("everyday");
    expect(presetForFreeBytes(13 * 1024 ** 3)?.key).toBe("feather");
  });
});

describe("Cache management", () => {
  afterEach(() => {
    vi.useRealTimers();
  });
  it("keeps a preset as a draft until saved and discards it without a request", async () => {
    const { fake } = renderApp("/cache");
    expect(await screen.findByRole("main", { name: "Cache overview" }, WAIT)).toBeInTheDocument();
    expect(screen.queryByLabelText("Search cached releases")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Encore/ })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Media disk budget")).not.toBeInTheDocument();
    await chooseProfile(/Encore/);
    expect(screen.getByLabelText("Media disk budget")).toHaveValue(250);
    expect(calls(fake, "PUT", "/config/cache")).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Discard changes" }));
    expect(calls(fake, "PUT", "/config/cache")).toHaveLength(0);
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Save policy" })).not.toBeInTheDocument(),
    );
    await chooseProfile(/Encore/);
    fireEvent.click(screen.getByRole("button", { name: "Save policy" }));
    await waitFor(() => expect(calls(fake, "PUT", "/config/cache")).toHaveLength(1));
    expect(fake.state.config.cache.budgetBytes).toBe(250 * 1024 ** 3);
    expect(fake.state.config.cache.mode).toBe("adaptive");
  });
  it("returns from the saved policy editor to presets and reviews a replacement", async () => {
    const { fake } = renderApp("/cache");
    fireEvent.click(await screen.findByRole("button", { name: "Edit policy" }, WAIT));
    fireEvent.click(screen.getByRole("button", { name: "Back to presets" }));
    fireEvent.click(await screen.findByRole("button", { name: /Feather/ }, WAIT));
    fireEvent.click(screen.getByRole("button", { name: "Review profile" }));
    expect(screen.getByLabelText("Media disk budget")).toHaveValue(0.25);
    fireEvent.click(screen.getByRole("button", { name: "Back to presets" }));
    fireEvent.click(await screen.findByRole("button", { name: /Encore/ }, WAIT));
    fireEvent.click(screen.getByRole("button", { name: "Review profile" }));
    expect(screen.getByLabelText("Media disk budget")).toHaveValue(250);
    expect(calls(fake, "PUT", "/config/cache")).toHaveLength(0);
  });
  it("shares NAS policy in a cluster and preserves the node mount path", async () => {
    const { fake } = renderApp("/cache", { cluster: true });
    fireEvent.click(await screen.findByRole("button", { name: "Edit policy" }, WAIT));
    fireEvent.click(screen.getByRole("radio", { name: /Shared NAS/ }));
    fireEvent.change(screen.getByLabelText("NAS mount path on this node"), {
      target: { value: "/mnt/ether-cache" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save policy" }));
    await waitFor(() => expect(fake.state.config.cache.storage).toBe("shared"));
    expect(fake.state.config.cache.dir).toBe("/mnt/ether-cache");
  });
  it("allows NAS storage on a standalone server without cluster setup", async () => {
    const { fake } = renderApp("/cache");
    fireEvent.click(await screen.findByRole("button", { name: "Edit policy" }, WAIT));
    const nas = screen.getByRole("radio", { name: /Shared NAS/ });
    expect(nas).toBeEnabled();
    fireEvent.click(nas);
    expect(screen.getByText(/No cluster is required/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("NAS mount path on this node"), {
      target: { value: "/Volumes/media/ether-cache" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save policy" }));
    await waitFor(() => expect(fake.state.config.cache.storage).toBe("shared"));
    expect(fake.state.config.cache.dir).toBe("/Volumes/media/ether-cache");
  });
  it("disables speculation controls in no-media mode and saves the off policy", async () => {
    const { fake } = renderApp("/cache");
    fireEvent.click(await screen.findByRole("button", { name: "Edit policy" }, WAIT));
    await pickToolbarOption("Retention strategy", "No persistent media");
    expect(screen.getByRole("switch", { name: "Warm detail pages" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Save policy" }));
    await waitFor(() => expect(fake.state.config.cache.mode).toBe("off"));
  });
  it("saves the playback read-ahead depth and disables it without media caching", async () => {
    const { fake } = renderApp("/cache");
    fireEvent.click(await screen.findByRole("button", { name: "Edit policy" }, WAIT));
    const ahead = await screen.findByRole("button", { name: "Read ahead while playing" }, WAIT);
    expect(ahead).toHaveTextContent("5 minutes (recommended)");
    await pickToolbarOption("Read ahead while playing", "10 minutes");
    fireEvent.click(screen.getByRole("button", { name: "Save policy" }));
    await waitFor(() => expect(fake.state.config.cache.playbackReadAheadMinutes).toBe(10));
    fireEvent.click(await screen.findByRole("button", { name: "Edit policy" }, WAIT));
    await pickToolbarOption("Retention strategy", "No persistent media");
    expect(screen.getByRole("button", { name: "Read ahead while playing" })).toBeDisabled();
  });
  it("filters retained titles and updates an owner pin", async () => {
    const { fake } = renderApp("/cache/explorer");
    expect(await screen.findByText("Arrival", {}, WAIT)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Search cached releases"), {
      target: { value: "Interstellar" },
    });
    expect(screen.queryByText("Arrival")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Pin Interstellar" }));
    await waitFor(() => expect(calls(fake, "POST", "/cache/pin")).toHaveLength(1));
    expect((await screen.findAllByText("Kept by you")).length).toBeGreaterThan(0);
  });
  it("saves a household blend and rolling TV window with explanations", async () => {
    const { fake } = renderApp("/cache");
    await chooseProfile(/Cinema node/);
    expect(screen.getByRole("button", { name: "Selection strategy" })).toHaveTextContent(
      "Smart household blend",
    );
    expect(screen.getByRole("button", { name: "Include in your shelf" })).toHaveTextContent(
      "Movies & TV episodes",
    );
    await pickToolbarOption("Include in your shelf", "TV episodes only");
    fireEvent.change(screen.getByLabelText("Household influence"), { target: { value: "80" } });
    fireEvent.change(screen.getByLabelText("Episode window per show"), { target: { value: "6" } });
    await pickToolbarOption("Learn from", "All active shared viewers");
    expect(calls(fake, "PUT", "/config/cache")).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Save policy" }));
    await waitFor(() => expect(fake.state.config.cache.preloadEpisodesAhead).toBe(6));
    expect(fake.state.config.cache.preloadContent).toBe("tv");
    expect(fake.state.config.cache.preloadHouseholdWeight).toBe(80);
    expect(fake.state.config.cache.preloadHouseholdScope).toBe("all");
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Save policy" })).not.toBeInTheDocument(),
    );
    fireEvent.click(screen.getByRole("tab", { name: "Preloading" }));
    expect((await screen.findAllByText(/Severance · S02E04/)).length).toBeGreaterThan(0);
    expect(screen.getByText(/Next in a household viewing sequence/)).toBeInTheDocument();
  });
  it("explains releases by reason, file name, filters, and drawer detail", async () => {
    renderApp("/cache/explorer");
    expect(await screen.findByText("Arrival", {}, WAIT)).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "Reason" })).toBeInTheDocument();
    expect(screen.queryByRole("columnheader", { name: "Plays" })).not.toBeInTheDocument();
    expect(screen.getAllByText("Unfinished").length).toBeGreaterThan(0);

    fireEvent.change(screen.getByLabelText("Search cached releases"), {
      target: { value: "Interstellar.2014" },
    });
    expect(await screen.findByText("Interstellar.2014.1080p.mkv")).toBeInTheDocument();
    expect(screen.queryByText("Arrival")).not.toBeInTheDocument();

    await pickToolbarOption("Filter cached releases", "All releases");
    fireEvent.change(screen.getByLabelText("Search cached releases"), { target: { value: "" } });
    expect(
      await screen.findByText("No reason was recorded for this older entry."),
    ).toBeInTheDocument();
    expect(
      await screen.findByText("The.Americans.2013.S03E01.1080p.BRRip.x264-HiQVE"),
    ).toBeInTheDocument();
    expect(await screen.findByText("Written by a quality check or probe.")).toBeInTheDocument();

    fireEvent.click(await screen.findByText("Interstellar.2014.1080p.mkv"));
    const dialog = await screen.findByRole("dialog", { name: "Interstellar" }, WAIT);
    expect(within(dialog).getByText("What's cached")).toBeInTheDocument();
    expect(within(dialog).getByText("Segment map")).toBeInTheDocument();
    expect(within(dialog).getByText("Where")).toBeInTheDocument();
    expect(within(dialog).getByText("Why it's cached")).toBeInTheDocument();
    expect(within(dialog).getByText("When")).toBeInTheDocument();
    expect(within(dialog).getByText("Fetched because")).toBeInTheDocument();
    expect(within(dialog).getByText("Kept because")).toBeInTheDocument();
    expect(within(dialog).getByText("Play history")).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    await pickToolbarOption("Filter cached releases", "Unfinished");
    expect(await screen.findByText(/Severance · S02E03/)).toBeInTheDocument();
    expect(screen.queryByText("Arrival")).not.toBeInTheDocument();
  });
  it("opens the release drawer via deep-link and keyboard navigation with segment and shelf details", async () => {
    const { fake } = renderApp("/cache/explorer?release=demo-dune-2160");
    const dialog = await screen.findByRole("dialog", { name: "Dune: Part Two" }, WAIT);
    expect(within(dialog).getByText("4K")).toBeInTheDocument();
    expect(within(dialog).getByText("DV · HDR10")).toBeInTheDocument();
    expect(within(dialog).getByText("Remux")).toBeInTheDocument();
    expect(within(dialog).getByText("72/100")).toBeInTheDocument();
    expect(within(dialog).getByText("Usenet")).toBeInTheDocument();
    expect(await within(dialog).findByText("Segments 0–1,999")).toBeInTheDocument();
    expect(calls(fake, "GET", "/cache/releases/demo-dune-2160").length).toBeGreaterThan(0);

    fireEvent.click(within(dialog).getByRole("button", { name: "Keep in cache" }));
    await waitFor(() => expect(calls(fake, "POST", "/cache/pin")).toHaveLength(1));

    fireEvent.click(within(dialog).getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    const row = (await screen.findByText(/Severance · S02E03/)).closest("tr")!;
    fireEvent.keyDown(row, { key: "Enter" });
    const nextDialog = await screen.findByRole("dialog", { name: /Severance · S02E03/ }, WAIT);
    expect(await within(nextDialog).findByText("Segments 0–31, 260–767")).toBeInTheDocument();
  });
  it("breaks protected bytes down by cause and names the current warm", async () => {
    renderApp("/cache/overview");
    expect(await screen.findByText(/Playing now: /, {}, WAIT)).toBeInTheDocument();
    expect(screen.getByText(/Unfinished: /)).toBeInTheDocument();
    expect(screen.getByText(/Shelf: /)).toBeInTheDocument();
    expect(screen.getByText(/Woe's Hollow.*Next episode/)).toBeInTheDocument();
  });
  it("shows cached, queued, and failed shelf states", async () => {
    const { fake } = renderApp("/cache/preloading");
    expect(await screen.findByText("Preload selection", {}, WAIT)).toBeInTheDocument();
    expect(screen.getByText("Shelf manifest")).toBeInTheDocument();
    expect(screen.getAllByText("Cached").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Queued").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Failed").length).toBeGreaterThan(0);
    expect(screen.getByText(/Titles chosen for the local shelf/)).toBeInTheDocument();
    expect(calls(fake, "GET", "/cache/").length).toBeGreaterThan(0);
  });
  it("refreshes explorer releases and totals without losing the filter", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let count = 0;
    const { fake } = renderAppWith("/cache/explorer", (method, path) => {
      if (method !== "GET" || path !== "/cache/") return undefined;
      count++;
      return {
        releases:
          count === 1
            ? []
            : [
                {
                  ReleaseID: "new",
                  Title: "New arrival",
                  Bytes: 100,
                  Size: 100,
                  Plays: 0,
                  Pins: [],
                },
              ],
        preload: {},
      };
    });
    expect(await screen.findByText("No retained media yet", {}, WAIT)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Search cached releases"), {
      target: { value: "arrival" },
    });
    const before = calls(fake, "GET", "/cache/").length;
    await act(async () => {
      vi.advanceTimersByTime(5000);
    });
    expect(calls(fake, "GET", "/cache/").length).toBeGreaterThan(before);
    expect(await screen.findByText("New arrival", {}, WAIT)).toBeInTheDocument();
    expect(screen.getByText("1 release")).toBeInTheDocument();
    expect(screen.getByLabelText("Search cached releases")).toHaveValue("arrival");
  });
  it("backs off polling after a cache load fails", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { fake } = renderAppWith("/cache", (method, path) =>
      method === "GET" && path === "/cache/"
        ? new Response(JSON.stringify({ error: { message: "Cache offline" } }), { status: 503 })
        : undefined,
    );
    expect(await screen.findByText(/Cache offline/, {}, WAIT)).toBeInTheDocument();
    await act(async () => {});
    const before = calls(fake, "GET", "/cache/").length;
    await act(async () => {
      vi.advanceTimersByTime(5000);
    });
    expect(calls(fake, "GET", "/cache/")).toHaveLength(before);
    await act(async () => {
      vi.advanceTimersByTime(5000);
    });
    expect(calls(fake, "GET", "/cache/").length).toBeGreaterThan(before);
  });
  it("navigates between compact overview and the dedicated explorer", async () => {
    renderApp("/cache");
    expect(await screen.findByRole("tab", { name: "Overview" }, WAIT)).toHaveAttribute(
      "aria-selected",
      "true",
    );
    fireEvent.click(screen.getByRole("tab", { name: /Explorer/ }));
    expect(await screen.findByRole("main", { name: "Cache explorer" })).toBeInTheDocument();
    expect(screen.getByLabelText("Search cached releases")).toBeInTheDocument();
    expect(screen.queryByText("Playback readiness")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: "Overview" }));
    expect(await screen.findByText("Playback readiness")).toBeInTheDocument();
    expect(screen.queryByLabelText("Search cached releases")).not.toBeInTheDocument();
  });
});
