import { describe, expect, it } from "vitest";
import { createFakeServer } from "../mock/server";

describe("Apps mock API", () => {
  it.each([false, true])(
    "preserves setup choices and installed-client status (%s) while saving listener settings",
    async (velaInstalled) => {
      const fake = createFakeServer({ velaInstalled });
      const fetch = fake.fetch;
      const initial = await (await fetch("/ether/api/frontends")).json();
      expect(
        initial.find((app: { descriptor: { id: string } }) => app.descriptor.id === "plex").enabled,
      ).toBe(true);
      expect(
        initial.find((app: { descriptor: { id: string } }) => app.descriptor.id === "vela").status
          .webPlayerReady,
      ).toBe(velaInstalled);
      const players = await (await fetch("/ether/api/web-player")).json();
      expect(players.players.some((player: { id: string }) => player.id === "vela")).toBe(
        velaInstalled,
      );
      await fetch("/ether/api/frontends/silo/settings", {
        method: "PUT",
        body: JSON.stringify({ port: 8092, discovery: true }),
      });
      await fetch("/ether/api/frontends/silo/enabled", {
        method: "PUT",
        body: JSON.stringify({ enabled: true }),
      });
      const settings = await (await fetch("/ether/api/frontends/silo/settings")).json();
      expect(settings).toEqual({ port: 8092, discovery: true });
      expect(fake.state.config.frontends?.plex?.enabled).toBe(true);
      expect(fake.state.config.frontendPorts?.silo).toBe(8092);
    },
  );

  it("lists enabled apps and returns a viewer connection", async () => {
    const fake = createFakeServer();
    await fake.fetch("/ether/api/frontends/stremio/enabled", {
      method: "PUT",
      body: JSON.stringify({ enabled: true }),
    });
    // Plex is on by default in the mock and is listed like any other enabled app.
    expect(await (await fake.fetch("/ether/api/me/apps")).json()).toEqual({
      apps: ["plex", "stremio"],
    });
    const created = await (
      await fake.fetch("/ether/api/me/apps/stremio", { method: "POST" })
    ).json();
    expect(created.url).toContain("/stremio/mock-grant/manifest.json");
    expect(created.qr).toMatch(/^data:image\/png;base64,/);
  });

  it("uploads Vela wordmarks and stores normalized branding settings", async () => {
    const fake = createFakeServer({ velaInstalled: true });
    const svg =
      '<svg xmlns="http://www.w3.org/2000/svg" width="220" height="48" viewBox="0 0 220 48"><rect width="220" height="48" rx="8" fill="#111"/></svg>';
    const form = new FormData();
    form.append("file", new File([svg], "wordmark.svg", { type: "image/svg+xml" }));
    const uploadRes = await fake.fetch("/ether/api/frontends/vela/wordmark", {
      method: "POST",
      body: form,
    });
    expect(uploadRes.status).toBe(200);
    const uploaded = (await uploadRes.json()) as { url: string; width: number; height: number };
    expect(uploaded.url).toMatch(/^\/vela\/v0\/branding\/[0-9a-f]{64}\.svg$/);
    expect(uploaded.width).toBe(220);
    expect(uploaded.height).toBe(48);

    const served = fake.velaBranding.serveAsset(uploaded.url.replace("/vela/v0/branding/", ""));
    expect(served.status).toBe(200);
    expect(served.headers.get("Content-Type")).toBe("image/svg+xml");

    await fake.fetch("/ether/api/frontends/vela/settings", {
      method: "PUT",
      body: JSON.stringify({
        defaultSkinId: "cinematic",
        appName: "  North Star Cinema  ",
        wordmark: uploaded,
      }),
    });
    const saved = await (await fake.fetch("/ether/api/frontends/vela/settings")).json();
    expect(saved).toEqual({
      defaultSkinId: "cinematic",
      appName: "North Star Cinema",
      wordmark: uploaded,
    });
  });
});
