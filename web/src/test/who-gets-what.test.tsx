import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import type { QualityAnswer } from "../lib/types";
import { createFakeServer } from "../mock/server";
import { createRuleResolver } from "../pages/releases/qualitySummaryUi";
import { answerHeadline, layerText, WhoGetsWhat } from "../pages/releases/WhoGetsWhat";
import { calls, settleUi } from "./harness";

const WAIT = { timeout: 4000 };

async function renderWhoGetsWhat(
  setup?: (fake: ReturnType<typeof createFakeServer>) => Promise<unknown> | void,
) {
  const fake = createFakeServer();
  await setup?.(fake);
  vi.stubGlobal("fetch", vi.fn(fake.fetch));
  render(
    <MemoryRouter>
      <WhoGetsWhat />
    </MemoryRouter>,
  );
  return fake;
}

const headline = () => screen.getByTestId("quality-headline").textContent;

describe("who gets what", () => {
  it("never sends an empty-client explain request on mount (M1)", async () => {
    let releaseDevices!: () => void;
    const devicesGate = new Promise<void>((resolve) => {
      releaseDevices = resolve;
    });
    const explainStatuses: number[] = [];
    let origFetch!: typeof fetch;

    const fake = await renderWhoGetsWhat((f) => {
      origFetch = f.fetch;
      f.fetch = async (input, init) => {
        const url = String(input);
        if (url.includes("/quality/devices") || url.includes("/quality/clients")) {
          await devicesGate;
        }
        const res = await origFetch(input, init);
        if (url.includes("/quality/explain")) {
          explainStatuses.push(res.status);
        }
        return res;
      };
    });

    // Viewers resolve immediately while devices are still in flight; explain must wait.
    await settleUi(() => expect(calls(fake, "GET", "/viewers").length).toBeGreaterThan(0));
    expect(calls(fake, "POST", "/quality/explain")).toHaveLength(0);

    releaseDevices();
    await settleUi(() => expect(headline()).toBe("4KHDRDolby Vision"));
    // Every UI-triggered explain request succeeded with 200 and carried a clientId.
    expect(explainStatuses).toEqual([200]);
    expect(calls(fake, "POST", "/quality/explain")).toHaveLength(1);
    expect(calls(fake, "POST", "/quality/explain")[0]?.body).toEqual({
      userId: 1,
      clientId: "plex-apple-tv",
      remote: false,
    });

    // Direct check that the mock enforces the 400 missing_client guard when clientId is omitted.
    const rawMissing = await origFetch("/ether/api/quality/explain", {
      method: "POST",
      body: JSON.stringify({ userId: 1, remote: false }),
    });
    expect(rawMissing.status).toBe(400);
  });

  it("falls back to clientId 'any' when a fresh server has no seen devices or apps", async () => {
    const fake = await renderWhoGetsWhat((f) => {
      const orig = f.fetch;
      f.fetch = async (input, init) => {
        const url = String(input);
        if (url.includes("/quality/devices")) {
          return new Response(JSON.stringify({ groups: [], types: [], apps: [], clients: [] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          });
        }
        if (url.includes("/quality/clients")) {
          return new Response(JSON.stringify({ clients: [] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          });
        }
        return orig(input, init);
      };
    });

    await settleUi(() => expect(headline()).toBe("1080pSDR"));
    expect(calls(fake, "POST", "/quality/explain").at(-1)?.body).toEqual({
      userId: 1,
      clientId: "any",
      remote: false,
    });
  });

  it("renders the per-library summary, exceptions, partial rules, plugin rows, and filters", async () => {
    const fake = await renderWhoGetsWhat(async (f) => {
      // Create a mixed-kind library (mediaType "") to verify Movies / Episodes split rows.
      await f.fetch("/ether/api/libraries", {
        method: "POST",
        body: JSON.stringify({
          name: "Anime",
          source: "auto",
          pinned: false,
        }),
      });
      f.state.config.quality.migratedRules = 1;
      f.state.config.quality.default = "1080p";
      f.state.config.quality.defaultAway = "";
      f.state.config.quality.profiles = [
        ...structuredClone(f.state.config.quality.profiles ?? []),
        {
          ...structuredClone(f.state.config.quality.profiles![0]!),
          name: "Anime Movies",
        },
        {
          ...structuredClone(f.state.config.quality.profiles![0]!),
          name: "Anime Episodes",
        },
      ];
      f.state.config.quality.rules = [
        {
          id: "r_shield",
          name: "Living room in 4K",
          devices: [{ client: "client-shield-lr", label: "Living room Shield" }],
          profile: "4K",
        },
        {
          id: "r_anime_m",
          libraries: [4],
          kind: "movie",
          profile: "Anime Movies",
        },
        {
          id: "r_anime_e",
          libraries: [4],
          kind: "episode",
          profile: "Anime Episodes",
        },
        {
          id: "r_mobile",
          devices: [{ group: "mobile", label: "Phones & tablets" }],
          profile: "",
          maxHeight: 1080,
        },
        {
          id: "r_away",
          where: "away",
          profile: "",
          maxBitrateKbps: 8000,
        },
      ];
    });

    const globalBox = await screen.findByTestId("global-exceptions", {}, WAIT);
    expect(within(globalBox).getByText(/Living room in 4K → 4K/)).toBeInTheDocument();
    expect(within(globalBox).getByText(/Phones & tablets → ≤ 1080p/)).toBeInTheDocument();

    // Mixed library splits into Movies and Episodes rows with distinct profiles.
    expect(screen.getAllByText("Anime Movies").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Anime Episodes").length).toBeGreaterThan(0);
    // Movies library (id 1) shows partialRuleIds ("Some titles: Rule 2") because Rule 2 names Anime (id 4).
    expect(screen.getAllByText("Rule 2").length).toBeGreaterThan(0);

    // Plugin library row shows "Releases chosen by Demo books" and limits when away.
    expect(screen.getAllByText("Releases chosen by Demo books")[0]).toBeInTheDocument();
    expect(screen.getAllByText("≤ 8 Mbps").length).toBeGreaterThan(0);

    // Filter by device: Phones & tablets -> exceptions disappear and ≤ 1080p applies in cells.
    fireEvent.click(screen.getByRole("button", { name: "Filter by device" }));
    fireEvent.click(await screen.findByRole("option", { name: "Phones & tablets" }, WAIT));
    await settleUi(() =>
      expect(calls(fake, "GET", "/quality/summary").at(-1)?.path).toContain(
        "device=group%3Amobile",
      ),
    );
    await settleUi(() => {
      expect(screen.queryByTestId("global-exceptions")).not.toBeInTheDocument();
      expect(screen.getAllByText("≤ 1080p").length).toBeGreaterThan(0);
    });
  });

  it("traces one play across viewer, app, where, and kind", async () => {
    const fake = await renderWhoGetsWhat((f) =>
      f.fetch("/ether/api/safety/policies/2", {
        method: "PUT",
        body: JSON.stringify({ maxBitrateKbps: 4000, remoteDisabled: true }),
      }),
    );
    await settleUi(() => expect(headline()).toBe("4KHDRDolby Vision"));

    fireEvent.click(screen.getByRole("button", { name: "Viewer" }));
    fireEvent.click(await screen.findByRole("option", { name: "Sam Rivera" }, WAIT));
    fireEvent.click(screen.getByRole("button", { name: "App" }));
    fireEvent.click(await screen.findByRole("option", { name: "Pixel 8" }, WAIT));
    await settleUi(() => expect(headline()).toBe("1080pSDRUp to 4 Mbps"));
    expect(screen.getByText("4 Mbps")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("radio", { name: "Away" }));
    await settleUi(() => expect(headline()).toBe("Can't play away from home"));
    expect(screen.getByText("Away playback disabled")).toBeInTheDocument();
    expect(calls(fake, "POST", "/quality/explain").at(-1)?.body).toEqual({
      userId: 2,
      clientId: "client-pixel-8",
      remote: true,
    });

    // Switch back to At home and choose Episodes kind.
    fireEvent.click(screen.getByRole("radio", { name: "At home" }));
    fireEvent.click(screen.getByRole("radio", { name: "Episodes" }));
    await settleUi(() =>
      expect(calls(fake, "POST", "/quality/explain").at(-1)?.body).toEqual({
        userId: 2,
        clientId: "client-pixel-8",
        kind: "episode",
        remote: false,
      }),
    );
    await settleUi(() => expect(screen.getByTestId("quality-media")).toHaveTextContent("Episodes"));
  });

  it("words every v1.3 layer the server can send", () => {
    const resolveRule = createRuleResolver({
      rules: [
        { id: "r_a", profile: "4K" },
        { id: "r_b", profile: "At home", maxHeight: 720 },
        { id: "r_c", profile: "4K" },
      ],
    });
    const answer: QualityAnswer = {
      profile: "At home",
      profileSource: "rule",
      profileRuleId: "r_b",
      limitRuleIds: ["r_b"],
      maxHeight: 720,
      hdr: true,
      dolbyVision: false,
      bitrateKbps: 0,
      blocked: false,
      layers: [
        {
          kind: "profile",
          reason: "",
          profile: "At home",
          source: "rule",
          ruleId: "r_b",
          maxHeight: 2160,
          decisive: false,
        },
        {
          kind: "device",
          reason: "",
          maxHeight: 2160,
          hdr: true,
          dolbyVision: false,
          edited: true,
          decisive: false,
        },
        { kind: "rule-limit", reason: "", ruleId: "r_b", maxHeight: 720, decisive: true },
        { kind: "rule-limit", reason: "", bitrateKbps: 8000, stereo: true, decisive: true },
      ],
    };
    expect(answerHeadline(answer)).toBe("720p · HDR on");
    expect(answer.layers.map((l) => layerText(l, "Sam", resolveRule))).toEqual([
      "Rule 2 · At home",
      "No limit",
      "Rule 2 · up to 720p",
      "8 Mbps · Stereo",
    ]);
    expect(
      layerText(
        { kind: "rule-limit", reason: "", ruleId: "r_a", ignored: true, decisive: false },
        "Sam",
        resolveRule,
      ),
    ).toBe("Device resolution is fixed");
    expect(
      layerText(
        { kind: "android-lift", reason: "", ruleId: "r_c", maxHeight: 2160, decisive: false },
        "Sam",
        resolveRule,
      ),
    ).toBe("Rule 3 lifts the device limit");
    expect(
      answerHeadline({
        ...answer,
        maxHeight: 0,
        hdr: false,
        bitrateKbps: 12500,
        stereo: true,
        layers: [],
      }),
    ).toBe("Any resolution · HDR off · Up to 12.5 Mbps · Stereo");
  });
});
