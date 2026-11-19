import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import Apps from "../pages/apps/Apps";

const apiFetch = vi.fn();
vi.mock("../lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/api")>();
  return {
    ...actual,
    apiFetch: (...args: unknown[]) => apiFetch(...args),
    apiBlob: () => Promise.reject(new Error("QR unavailable")),
  };
});
vi.mock("../app-context", () => ({
  useApp: () => ({
    dashboard: { sections: { remote: {} } },
    reloadBoot: vi.fn(),
    boot: { serverName: "Living Room Ether" },
  }),
}));

type AppRow = {
  descriptor: {
    id: string;
    name: string;
    certifiedApps?: { name: string; steps: string[] }[];
    settingsSchema?: unknown;
  };
  enabled: boolean;
  status: {
    webPlayerReady?: boolean;
    addresses: string[];
    connectUrls?: string[];
    lan: string;
    wan: string;
    clients24h: number;
    clients24hKnown: boolean;
    liveStreams: number;
    liveStreamsKnown: boolean;
    targetVersion: string;
    conformance: string;
    error?: string;
  };
};

/** The settings schema the server serves for these rows (WS1d). */
const servedSchema = {
  signInHelp: true,
  sections: [
    {
      key: "find",
      fields: [
        { key: "serverName", kind: "serverName" },
        { key: "port", kind: "port", min: 1, max: 65535 },
        { key: "discovery", kind: "bool", more: true },
      ],
    },
  ],
};

function row(id: string, name: string, enabled: boolean, extra?: Partial<AppRow>): AppRow {
  return {
    descriptor: { id, name, settingsSchema: servedSchema, ...extra?.descriptor },
    enabled,
    status: {
      addresses: enabled ? ["192.168.1.2:8090"] : [],
      connectUrls: ["http://192.168.1.2:8090"],
      lan: "reachable",
      wan: "unknown",
      clients24h: 1,
      clients24hKnown: true,
      liveStreams: 0,
      liveStreamsKnown: true,
      targetVersion: "1",
      conformance: "passing",
      ...extra?.status,
    },
  };
}

function LocationProbe() {
  const location = useLocation();
  return <div data-testid="loc">{`${location.pathname}${location.search}`}</div>;
}

function renderApps(entry: string) {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <LocationProbe />
      <Routes>
        <Route path="/apps/:tab?/:section?" element={<Apps />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("Apps", () => {
  beforeEach(() => {
    apiFetch.mockReset();
  });

  it("shows live status and toggles a registered protocol", async () => {
    let enabled = true;
    apiFetch.mockImplementation(async (path: string, options?: RequestInit) => {
      if (path === "/frontends" && !options?.method) {
        return [
          row("silo", "Silo", enabled, {
            descriptor: {
              id: "silo",
              name: "Silo",
              certifiedApps: [
                { name: "Certified player", steps: ["openApp", "addServer", "unknown"] },
              ],
            },
          }),
        ];
      }
      if (path === "/frontends/silo/enabled") {
        enabled = false;
        return { ok: true };
      }
      if (path === "/frontends/silo/settings") return { port: 8090 };
      throw new Error(`Unexpected request: ${path}`);
    });
    renderApps("/apps");
    expect(await screen.findByRole("switch", { name: "Silo enabled" })).toBeChecked();
    expect(screen.getByText("On")).toBeInTheDocument();
    expect(screen.queryByText("192.168.1.2:8090")).not.toBeInTheDocument();
    expect(screen.queryByText(/LAN:/)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Connect a device" })).not.toBeInTheDocument();
    expect(screen.queryByRole("spinbutton", { name: "Port" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: "Silo" }));
    expect(await screen.findByText(/· Reachable/)).toBeInTheDocument();
    expect(screen.getByText(/192\.168\.1\.2:8090/)).toBeInTheDocument();
    expect(screen.queryByText(/LAN:/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Outside access is not checked/)).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "How people sign in" })).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Copy address" })).toHaveLength(2);
    expect(
      screen.getByText("Create an app password under Connect apps in your account."),
    ).toBeInTheDocument();
    expect(screen.queryByText(/The Emby for TV/)).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Open my account" })).not.toBeInTheDocument();
    expect(screen.getByText("People sign in with their Ether viewer account.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Viewers" })).toHaveAttribute("href", "/viewers");
    fireEvent.click(screen.getByRole("button", { name: "Connect a device" }));
    expect(screen.getByText("Certified player")).toBeInTheDocument();
    expect(screen.getByText("Open the app on your device.")).toBeInTheDocument();
    expect(screen.queryByText("unknown")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: "Overview" }));
    fireEvent.click(screen.getByRole("switch", { name: "Silo enabled" }));
    await waitFor(() =>
      expect(screen.getByRole("switch", { name: "Silo enabled" })).not.toBeChecked(),
    );
    expect(apiFetch).toHaveBeenCalledWith("/frontends/silo/enabled", {
      method: "PUT",
      body: JSON.stringify({ enabled: false }),
    });
    expect(screen.queryByRole("tab", { name: "Silo" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Settings" })).not.toBeInTheDocument();
  });

  it("reveals a config tab when an app is enabled and keeps forms off the overview", async () => {
    let enabled = false;
    apiFetch.mockImplementation(async (path: string, options?: RequestInit) => {
      if (path === "/frontends" && !options?.method) return [row("jellyfin", "Jellyfin", enabled)];
      if (path === "/frontends/jellyfin/enabled") {
        enabled = true;
        return { ok: true };
      }
      if (path === "/frontends/jellyfin/settings") return { serverName: "House", port: 8096 };
      throw new Error(`Unexpected request: ${path}`);
    });
    renderApps("/apps");
    expect(await screen.findByRole("switch", { name: "Jellyfin enabled" })).not.toBeChecked();
    expect(screen.queryByRole("tab", { name: "Jellyfin" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Settings" })).not.toBeInTheDocument();
    expect(screen.queryByRole("spinbutton", { name: "Port" })).not.toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: "Server name" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("switch", { name: "Jellyfin enabled" }));
    expect(await screen.findByRole("tab", { name: "Jellyfin" })).toBeInTheDocument();
    expect(screen.queryByRole("spinbutton", { name: "Port" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: "Jellyfin" }));
    expect(await screen.findByText("Quick Connect")).toBeInTheDocument();
    expect(screen.queryByRole("spinbutton", { name: "Port" })).not.toBeInTheDocument();
    expect(screen.queryByText("Server name")).not.toBeInTheDocument();
    expect(screen.queryByText("Server identity")).not.toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: "Server name" })).not.toBeInTheDocument();
  });

  it("shows the chosen Jellyfin address with a styled picker and a single Other addresses disclosure", async () => {
    const urls = [
      "http://192.168.68.65:32400/jellyfin",
      "http://192.168.68.65:8098",
      "http://[fd79:f184::1]:32400/jellyfin",
      "http://100.65.221.123:8098",
      "http://10.255.0.2:32400/jellyfin",
    ];
    apiFetch.mockImplementation(async (path: string) => {
      if (path === "/frontends") {
        return [
          row("jellyfin", "Jellyfin", true, {
            status: {
              addresses: urls,
              connectUrls: urls,
              lan: "unknown",
              wan: "unknown",
              clients24h: 0,
              clients24hKnown: false,
              liveStreams: 0,
              liveStreamsKnown: false,
              targetVersion: "1",
              conformance: "passing",
            },
          }),
        ];
      }
      if (path === "/frontends/jellyfin/settings") return { serverName: "House", port: 8096 };
      throw new Error(`Unexpected request: ${path}`);
    });
    const { container } = renderApps("/apps/jellyfin");
    const line = await screen.findByRole("link", { name: urls[0] });
    expect(line.textContent).toContain("http://192.168.68.65:32400/jellyfin");
    expect(line.textContent).not.toMatch(/fd79|100\.65|10\.255|:8098/);
    expect(screen.queryByText("http://[fd79:f184::1]:32400/jellyfin")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Other addresses" }));
    expect(screen.getByText("http://[fd79:f184::1]:32400/jellyfin")).toBeInTheDocument();
    expect(line.textContent).not.toMatch(/fd79/);
    fireEvent.click(screen.getByRole("button", { name: "Connect a device" }));
    expect(screen.queryByRole("button", { name: "Connect a device" })).not.toBeInTheDocument();
    expect(container.querySelector("select")).toBeNull();
    expect(screen.getAllByRole("button", { name: "Other addresses" })).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Server address" }));
    fireEvent.click(await screen.findByRole("option", { name: "http://192.168.68.65:8098" }));
    expect(
      screen.getAllByRole("link", { name: "http://192.168.68.65:8098" })[0],
    ).toBeInTheDocument();
    expect(line).not.toHaveTextContent("32400/jellyfin");
  });

  it("places Watch inside Vela Status as a bordered button and shows Vela-specific setup copy", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ documents: [] }), { status: 200 }),
    );
    apiFetch.mockImplementation(async (path: string) => {
      if (path === "/frontends") {
        return [
          row("vela", "Vela", true, {
            status: {
              webPlayerReady: true,
              addresses: ["http://192.168.68.65:32550/vela"],
              connectUrls: ["http://192.168.68.65:32550/vela", "http://100.65.221.123:32550/vela"],
              lan: "reachable",
              wan: "unknown",
              clients24h: 0,
              clients24hKnown: true,
              liveStreams: 0,
              liveStreamsKnown: true,
              targetVersion: "1",
              conformance: "passing",
            },
          }),
        ];
      }
      if (path === "/frontends/vela/settings") return { defaultSkinId: "cinematic" };
      if (path === "/vela/skins") return { skins: [] };
      if (path === "/frontends/vela/distribution")
        return { installed: true, version: "0.4.0", channel: "bundled" };
      if (path === "/web-player")
        return {
          available: true,
          players: [{ id: "vela", url: "http://192.168.68.65:32550/vela/" }],
        };
      throw new Error(`Unexpected request: ${path}`);
    });
    renderApps("/apps/vela");
    const statusHeading = await screen.findByRole("heading", { name: "Status" });
    const statusCard = statusHeading.closest("section")!;
    const watchBtn = await within(statusCard).findByRole("button", {
      name: "Open the web player",
    });
    expect(watchBtn).toHaveClass("border");
    expect(screen.getAllByRole("button", { name: "Open the web player" })).toHaveLength(1);
    fireEvent.click(within(statusCard).getByRole("button", { name: "Connect a device" }));
    expect(
      within(statusCard).getByText(
        "Open Watch to play in your browser, or install the Vela app on web, Windows, macOS, or Apple TV and connect with the server address above.",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByText(/Swiftfin/)).not.toBeInTheDocument();
    vi.restoreAllMocks();
  });

  it("sorts Overview cards with enabled apps first in tab-bar order, then disabled apps", async () => {
    apiFetch.mockImplementation(async (path: string) => {
      if (path === "/frontends") {
        return [
          row("stremio", "Stremio", false),
          row("plex", "Plex", true),
          row("dlna", "DLNA", false),
          row("emby", "Emby", true),
          row("silo", "Silo", false),
          row("vela", "Vela", true, {
            status: {
              webPlayerReady: true,
              addresses: ["http://192.168.1.2:32550/vela"],
              connectUrls: ["http://192.168.1.2:32550/vela"],
              lan: "reachable",
              wan: "unknown",
              clients24h: 0,
              clients24hKnown: true,
              liveStreams: 0,
              liveStreamsKnown: true,
              targetVersion: "1",
              conformance: "passing",
            },
          }),
          row("jellyfin", "Jellyfin", true),
        ];
      }
      if (path === "/plex/remote") return { addresses: [] };
      throw new Error(`Unexpected request: ${path}`);
    });
    renderApps("/apps");
    const switches = await screen.findAllByRole("switch");
    expect(switches.map((el) => el.getAttribute("aria-label"))).toEqual([
      "Vela enabled",
      "Plex enabled",
      "Jellyfin enabled",
      "Emby enabled",
      "Stremio enabled",
      "Silo enabled",
      "DLNA enabled",
    ]);
  });

  it("hides Connect a device when the panel is already open", async () => {
    apiFetch.mockImplementation(async (path: string) => {
      if (path === "/frontends") return [row("jellyfin", "Jellyfin", true)];
      if (path === "/frontends/jellyfin/settings") return { serverName: "House", port: 8096 };
      throw new Error(`Unexpected request: ${path}`);
    });
    renderApps("/apps/jellyfin?connect=1");
    expect(await screen.findByText(/Enter this server address/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Connect a device" })).not.toBeInTheDocument();
  });

  it.each(["jellyfin"])("sends an unavailable %s deep link back to the overview", async (id) => {
    apiFetch.mockImplementation(async (path: string) => {
      if (path === "/frontends")
        return [row(id, id === "vela" ? "Vela" : "Jellyfin", id === "vela")];
      throw new Error(`Unexpected request: ${path}`);
    });
    renderApps(`/apps/${id}`);
    await waitFor(() => expect(screen.getByTestId("loc")).toHaveTextContent("/apps/overview"));
    expect(
      screen.queryByRole("tab", { name: id === "vela" ? "Vela" : "Jellyfin" }),
    ).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Settings" })).not.toBeInTheDocument();
    expect(screen.queryByRole("spinbutton", { name: "Port" })).not.toBeInTheDocument();
  });

  it("sends a disabled Plex route back to the overview", async () => {
    apiFetch.mockImplementation(async (path: string) => {
      if (path === "/frontends") return [row("plex", "Plex", false)];
      throw new Error(`Unexpected request: ${path}`);
    });
    const { unmount } = renderApps("/apps/plex");
    await waitFor(() => expect(screen.getByTestId("loc")).toHaveTextContent("/apps/overview"));
    expect(screen.queryByRole("tab", { name: "Plex" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Settings" })).not.toBeInTheDocument();
    expect(screen.getByText("Off")).toBeInTheDocument();
    unmount();
    renderApps("/apps/plex/remote");
    await waitFor(() => expect(screen.getByTestId("loc")).toHaveTextContent("/apps/overview"));
    expect(screen.queryByLabelText("Secure connections")).not.toBeInTheDocument();
  });

  it("keeps the previous switch state when enabling fails", async () => {
    apiFetch.mockImplementation(async (path: string, options?: RequestInit) => {
      if (path === "/frontends" && !options?.method) return [row("silo", "Silo", true)];
      if (path === "/frontends/silo/enabled") throw new Error("enable failed");
      throw new Error(`Unexpected request: ${path}`);
    });
    renderApps("/apps");
    const toggle = await screen.findByRole("switch", { name: "Silo enabled" });
    expect(toggle).toBeChecked();
    fireEvent.click(toggle);
    expect(await screen.findByText("enable failed")).toBeInTheDocument();
    expect(screen.getByRole("switch", { name: "Silo enabled" })).toBeChecked();
    expect(screen.getByRole("tab", { name: "Silo" })).toBeInTheDocument();
  });

  it("edits Vela app name, uploads light and dark wordmarks, updates live preview, and saves trimmed branding", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ documents: [] }), { status: 200 }),
    );
    let savedSettings: Record<string, unknown> = { defaultSkinId: "cinematic" };
    let uploadCall = 0;
    apiFetch.mockImplementation(async (path: string, options?: RequestInit) => {
      if (path === "/frontends" && !options?.method) {
        return [
          row("vela", "Vela", true, {
            status: {
              webPlayerReady: true,
              addresses: ["http://192.168.68.65:32550/vela"],
              connectUrls: ["http://192.168.68.65:32550/vela"],
              lan: "reachable",
              wan: "unknown",
              clients24h: 0,
              clients24hKnown: true,
              liveStreams: 0,
              liveStreamsKnown: true,
              targetVersion: "1",
              conformance: "passing",
            },
          }),
        ];
      }
      if (path === "/vela/skins" && !options?.method) return { skins: [] };
      if (path === "/frontends/vela/settings" && !options?.method) return savedSettings;
      if (path === "/frontends/vela/settings" && options?.method === "PUT") {
        savedSettings = JSON.parse(String(options.body));
        return { ok: true };
      }
      if (path === "/frontends/vela/wordmark" && options?.method === "POST") {
        uploadCall += 1;
        return uploadCall === 1
          ? { url: "/vela/v0/branding/aaaa.svg", width: 240, height: 48 }
          : { url: "/vela/v0/branding/bbbb.svg", width: 240, height: 48 };
      }
      if (path === "/vela/status") {
        return {
          installed: true,
          installedVersion: "0.4.0",
          availableVersion: "0.4.0",
          rollbackAvailable: false,
          busy: false,
          phase: "",
          downloaded: 0,
          total: 0,
        };
      }
      if (path === "/vela/check") {
        return {
          installed: true,
          installedVersion: "0.4.0",
          availableVersion: "0.4.0",
          rollbackAvailable: false,
          busy: false,
          phase: "",
          downloaded: 0,
          total: 0,
        };
      }
      if (path === "/web-player") {
        return {
          available: true,
          players: [{ id: "vela", url: "http://192.168.68.65:32550/vela/" }],
        };
      }
      throw new Error(`Unexpected request: ${path}`);
    });

    renderApps("/apps/vela");
    const nameInput = await screen.findByLabelText("App name");
    expect(screen.getByRole("heading", { name: "Branding" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Skins" })).toBeInTheDocument();
    fireEvent.change(nameInput, { target: { value: "  Rialto House  " } });

    const lightPreview = screen.getByTestId("vela-preview-light");
    const darkPreview = screen.getByTestId("vela-preview-dark");
    expect(within(lightPreview).getAllByText("Rialto House")).toHaveLength(2);
    expect(within(darkPreview).getAllByText("Rialto House")).toHaveLength(2);

    const tooLarge = new File([new Uint8Array(513 * 1024)], "huge.png", { type: "image/png" });
    fireEvent.change(screen.getByLabelText("Upload light wordmark"), {
      target: { files: [tooLarge] },
    });
    expect(await screen.findByText("Wordmark must be 512 KB or smaller.")).toBeInTheDocument();

    const lightSvg = new File(["<svg/>"], "light.svg", { type: "image/svg+xml" });
    fireEvent.change(screen.getByLabelText("Upload light wordmark"), {
      target: { files: [lightSvg] },
    });
    expect(await screen.findByText("240×48 px")).toBeInTheDocument();
    expect(within(lightPreview).getByRole("img", { name: "Rialto House" })).toHaveAttribute(
      "src",
      "/vela/v0/branding/aaaa.svg",
    );
    expect(within(darkPreview).getByRole("img", { name: "Rialto House" })).toHaveAttribute(
      "src",
      "/vela/v0/branding/aaaa.svg",
    );

    const darkSvg = new File(["<svg/>"], "dark.svg", { type: "image/svg+xml" });
    fireEvent.change(screen.getByLabelText("Upload dark wordmark"), {
      target: { files: [darkSvg] },
    });
    await waitFor(() =>
      expect(within(darkPreview).getByRole("img", { name: "Rialto House" })).toHaveAttribute(
        "src",
        "/vela/v0/branding/bbbb.svg",
      ),
    );

    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() =>
      expect(savedSettings).toEqual({
        defaultSkinId: "cinematic",
        appName: "Rialto House",
        wordmark: {
          url: "/vela/v0/branding/aaaa.svg",
          width: 240,
          height: 48,
          dark: "/vela/v0/branding/bbbb.svg",
        },
      }),
    );

    fireEvent.click(screen.getByLabelText("Remove dark wordmark"));
    expect(within(darkPreview).getByRole("img", { name: "Rialto House" })).toHaveAttribute(
      "src",
      "/vela/v0/branding/aaaa.svg",
    );
    vi.restoreAllMocks();
  });
});
