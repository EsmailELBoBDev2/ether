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

  it("lists, installs, guards private URLs, and removes Vela skins in mock mode", async () => {
    const fake = createFakeServer({ velaInstalled: true });
    const initial = await (await fake.fetch("/ether/api/vela/skins")).json();
    expect(
      initial.skins.some(
        (s: { id: string; builtin: boolean }) => s.id === "cinematic-doc" && s.builtin,
      ),
    ).toBe(true);
    expect(
      initial.skins.some((s: { id: string; builtin: boolean }) => s.id === "harbor" && !s.builtin),
    ).toBe(true);

    const builtinDelete = await fake.fetch("/ether/api/vela/skins/mochi", { method: "DELETE" });
    expect(builtinDelete.status).toBe(400);

    const lanBlocked = await fake.fetch("/ether/api/vela/skins", {
      method: "POST",
      body: JSON.stringify({ url: "http://192.168.1.50/reef.vskin", lan: false }),
    });
    expect(lanBlocked.status).toBe(400);

    const lanAllowed = await fake.fetch("/ether/api/vela/skins", {
      method: "POST",
      body: JSON.stringify({ url: "http://192.168.1.50/reef.vskin", lan: true }),
    });
    expect(lanAllowed.status).toBe(201);
    expect((await lanAllowed.json()).id).toBe("reef");

    const removed = await fake.fetch("/ether/api/vela/skins/reef", { method: "DELETE" });
    expect(removed.status).toBe(200);
  });
});
