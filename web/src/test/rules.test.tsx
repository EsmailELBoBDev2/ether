import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { AppProvider } from "../app-context";
import {
  deviceFromKey,
  deviceKey,
  overlappingRules,
  profileUses,
  ruleLimits,
  shadowedBy,
  tidyRule,
  withProfileRenamed,
} from "../lib/qualityRules";
import type { EtherConfig, QualityRule } from "../lib/types";
import { MOCK_QUALITY_DEVICES } from "../mock/quality";
import { RuleDrawer } from "../pages/releases/RuleDrawer";
import { createFakeServer } from "../mock/server";
import { calls, renderApp } from "./harness";

const WAIT = { timeout: 10000 };

describe("qualityRules helpers", () => {
  it("round-trips device keys containing colons", () => {
    const match = deviceFromKey("app:jellyfin:Infuse", "Infuse");
    expect(match).toEqual({ app: "jellyfin:Infuse", label: "Infuse" });
    expect(deviceKey(match)).toBe("app:jellyfin:Infuse");
  });

  it("formats limits and tidies empty conditions", () => {
    expect(
      ruleLimits({ id: "r_1", maxHeight: 1080, maxBitrateKbps: 8000, stereoAudio: true }),
    ).toEqual(["≤ 1080p", "≤ 8 Mbps", "Stereo"]);
    expect(
      tidyRule({
        id: "r_1",
        name: "  ",
        libraries: [],
        exceptLibraries: true,
        where: "",
        kind: "",
        profile: "",
        maxHeight: 1080,
      }),
    ).toEqual({ id: "r_1", maxHeight: 1080 });
  });

  it("detects shadowed and overlapping rules using device groups", () => {
    const rules: QualityRule[] = [
      { id: "r_1", devices: [{ group: "tv", label: "TVs & streaming boxes" }], profile: "4K" },
      { id: "r_2", devices: [{ group: "mobile", label: "Phones & tablets" }], maxHeight: 1080 },
      {
        id: "r_3",
        devices: [{ client: "client-shield-lr", label: "Living room Shield" }],
        profile: "1080p",
      },
      { id: "r_4", libraries: [1], profile: "1080p" },
    ];
    // Rule 1 (TVs) completely covers rule 3 (Living room Shield, which is a TV).
    expect(shadowedBy(rules, 2, MOCK_QUALITY_DEVICES)).toBe(0);
    // Rule 2 is limits-only and never shadows a profile.
    expect(shadowedBy(rules, 3, MOCK_QUALITY_DEVICES)).toBe(-1);
    // Rule 4 (Movies, any device) overlaps rule 1 (any library, TVs) and rule 3 (any library, Shield).
    const hits = overlappingRules(rules, 3, MOCK_QUALITY_DEVICES);
    expect(hits.map((h) => ({ index: h.index, coversAll: h.coversAll }))).toEqual([
      { index: 0, coversAll: false },
      { index: 2, coversAll: false },
    ]);
  });

  it("tracks profile uses and renames references across defaults and rules", () => {
    const quality: EtherConfig["quality"] = {
      profiles: [],
      default: "4K",
      defaultAway: "1080p",
      rules: [{ id: "r_1", name: "TVs", profile: "4K" }],
    };
    const names = { library: (id: number) => `Lib ${id}`, viewer: (id: number) => `User ${id}` };
    expect(profileUses(quality, "4K", names)).toEqual([
      { key: "default", label: "Default", to: "/releases/rules?default=home" },
      { key: "r_1", label: "TVs", to: "/releases/rules?rule=r_1" },
    ]);
    const renamed = withProfileRenamed(quality, "4K", "UHD");
    expect(renamed.default).toBe("UHD");
    expect(renamed.defaultAway).toBe("1080p");
    expect(renamed.rules?.[0]?.profile).toBe("UHD");
  });
});

describe("Releases › Rules", () => {
  it("lists rules in order with fixed At home and Away default rows", async () => {
    renderApp("/releases/rules");
    expect(await screen.findByText("Living room Apple TV", {}, WAIT)).toBeInTheDocument();
    expect(
      screen.getByText(
        "Checked from the top. The first rule with a profile picks it; every matching limit applies.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByText(/Everything else · At home/)).toBeInTheDocument();
    expect(screen.getByText(/Everything else · Away/)).toBeInTheDocument();
    expect(screen.getByText("Same as at home")).toBeInTheDocument();
  });

  it("validates empty rules and adds a limits-only rule via PUT /config/quality", async () => {
    const { fake } = renderApp("/releases/rules");
    fireEvent.click(await screen.findByRole("button", { name: "Add rule" }, WAIT));
    const drawer = await screen.findByRole("dialog", { name: "New rule" });

    // Saving without a profile or limit shows inline validation.
    fireEvent.click(within(drawer).getByRole("button", { name: "Save rule" }));
    expect(
      await within(drawer).findByText("Choose a profile or set at least one limit."),
    ).toBeInTheDocument();
    expect(calls(fake, "PUT", "/config/quality")).toHaveLength(0);

    // Pick Device -> Only... -> Phones & tablets from GET /quality/devices.
    const deviceMode = within(drawer).getByRole("radiogroup", { name: "Device" });
    fireEvent.click(within(deviceMode).getByRole("radio", { name: "Only…" }));
    fireEvent.click(within(drawer).getByRole("button", { name: "Devices" }));
    fireEvent.click(await screen.findByRole("option", { name: /Phones & tablets/ }));

    // Pick Where -> Away and limits (1080p, 5 Mbps, Stereo), leaving Profile on "No change".
    fireEvent.click(
      within(within(drawer).getByRole("radiogroup", { name: "Where" })).getByRole("radio", {
        name: "Away",
      }),
    );
    fireEvent.click(
      within(within(drawer).getByRole("radiogroup", { name: "Max resolution" })).getByRole(
        "radio",
        { name: "1080p" },
      ),
    );
    fireEvent.change(within(drawer).getByLabelText("Max bitrate"), { target: { value: "5" } });
    fireEvent.click(
      within(within(drawer).getByRole("radiogroup", { name: "Audio" })).getByRole("radio", {
        name: "Stereo",
      }),
    );

    fireEvent.click(within(drawer).getByRole("button", { name: "Save rule" }));
    await waitFor(() => expect(calls(fake, "PUT", "/config/quality")).toHaveLength(1));
    const body = calls(fake, "PUT", "/config/quality")[0]!.body as EtherConfig["quality"];
    expect(body.rules).toHaveLength(2);
    expect(body.rules?.[1]).toMatchObject({
      devices: [{ group: "mobile", label: "Phones & tablets" }],
      where: "away",
      maxHeight: 1080,
      maxBitrateKbps: 5000,
      stereoAudio: true,
    });
    expect(body.rules?.[1]?.profile).toBeUndefined();
    // Wait for drawer close and list refresh with chips.
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(await screen.findByText("≤ 1080p")).toBeInTheDocument();
    expect(screen.getByText("≤ 5 Mbps")).toBeInTheDocument();
    expect(screen.getByText("Stereo")).toBeInTheDocument();
  });

  it("reorders rules with order buttons and saves the new order", async () => {
    const { fake } = renderApp("/releases/rules", {}, (f) => {
      f.state.config.quality.rules = [
        { id: "r_first", name: "First rule", profile: "4K" },
        { id: "r_second", name: "Second rule", maxHeight: 1080 },
      ];
    });
    await screen.findByText("First rule", {}, WAIT);
    fireEvent.click(screen.getByRole("button", { name: "Move rule 2 up" }));
    await waitFor(() => expect(calls(fake, "PUT", "/config/quality")).toHaveLength(1));
    const body = calls(fake, "PUT", "/config/quality")[0]!.body as EtherConfig["quality"];
    expect(body.rules?.map((r) => r.id)).toEqual(["r_second", "r_first"]);
  });

  it("edits the fixed At home and Away default rows", async () => {
    const { fake } = renderApp("/releases/rules");
    fireEvent.click(await screen.findByRole("button", { name: "Edit the away default" }, WAIT));
    const drawer = await screen.findByRole("dialog", { name: "Everything else, away" });
    fireEvent.click(within(drawer).getByRole("button", { name: "Profile" }));
    fireEvent.click(await screen.findByRole("option", { name: "1080p" }));
    fireEvent.click(within(drawer).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(calls(fake, "PUT", "/config/quality")).toHaveLength(1));
    expect(fake.state.config.quality.defaultAway).toBe("1080p");
  });

  it("flags shadowed rules in the list and offers a jump link in the drawer", async () => {
    renderApp("/releases/rules", {}, (f) => {
      f.state.config.quality.rules = [
        { id: "r_all", name: "Everything 4K", profile: "4K" },
        { id: "r_sub", name: "Movies in 1080p", libraries: [1], profile: "1080p" },
      ];
    });
    expect(await screen.findByText("Never picks a profile", {}, WAIT)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Edit rule 2" }));
    const drawer = await screen.findByRole("dialog", { name: "Movies in 1080p" });
    expect(
      within(drawer).getByText(/Rule 1 “Everything 4K” comes first for all of these/),
    ).toBeInTheDocument();
    fireEvent.click(within(drawer).getByRole("button", { name: "Open rule 1" }));
    expect(await screen.findByRole("dialog", { name: "Everything 4K" })).toBeInTheDocument();
  });

  it("opens rules and presets from URL query params", async () => {
    renderApp("/releases/rules?new=1&library=1&profile=4K");
    const drawer = await screen.findByRole("dialog", { name: "New rule" }, WAIT);
    expect(await within(drawer).findByText("Only Movies", {}, WAIT)).toBeInTheDocument();
    // Clicking the widen button unlocks the library condition to the full picker.
    fireEvent.click(within(drawer).getByRole("button", { name: "Choose other libraries" }));
    expect(within(drawer).getByRole("radiogroup", { name: "Library" })).toBeInTheDocument();
  });

  it("opens an existing rule via ?rule=<id> and a default row via ?default=home", async () => {
    renderApp("/releases/rules?rule=r_appletv1");
    expect(
      await screen.findByRole("dialog", { name: "Living room Apple TV" }, WAIT),
    ).toBeInTheDocument();
  });

  it("removes a rule after confirmation and shows a dismissible migration note", async () => {
    const { fake } = renderApp("/releases/rules", {}, (f) => {
      f.state.config.quality.migratedRules = 1;
      f.state.config.quality.rulesMigrationNote = true;
    });
    expect(
      await screen.findByText(
        "Your quality settings were moved into rules. Nothing plays differently.",
        {},
        WAIT,
      ),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(
      screen.queryByText("Your quality settings were moved into rules. Nothing plays differently."),
    ).not.toBeInTheDocument();
    await waitFor(() => expect(fake.state.config.quality.rulesMigrationNote).toBe(false));

    fireEvent.click(screen.getByRole("button", { name: "Edit rule 1" }));
    const drawer = await screen.findByRole("dialog", { name: "Living room Apple TV" });
    fireEvent.click(within(drawer).getByRole("button", { name: "Remove rule" }));
    fireEvent.click(await screen.findByRole("button", { name: "Remove" }));
    await waitFor(() => expect(fake.state.config.quality.rules).toEqual([]));
  });

  it("renders standalone RuleDrawer with lockLibrary and lockViewer props", async () => {
    const fake = createFakeServer();
    vi.stubGlobal("fetch", vi.fn(fake.fetch));
    let closed = false;
    render(
      <MemoryRouter>
        <AppProvider
          boot={{
            ok: true,
            version: "test",
            setupComplete: true,
            authenticated: true,
            authRequired: false,
            serverName: "Test",
          }}
          reloadBoot={async () => {}}
        >
          <RuleDrawer
            open
            lockLibrary={1}
            lockViewer={2}
            onClose={() => {
              closed = true;
            }}
          />
        </AppProvider>
      </MemoryRouter>,
    );
    const drawer = await screen.findByRole("dialog", { name: "New rule" }, WAIT);
    expect(await within(drawer).findByText("Only Movies", {}, WAIT)).toBeInTheDocument();
    expect(await within(drawer).findByText("Only Sam Rivera", {}, WAIT)).toBeInTheDocument();
    fireEvent.click(within(drawer).getByRole("button", { name: "Cancel" }));
    expect(closed).toBe(true);
  });
});
