import { ConfirmRoot } from "@braedonsaunders/appkit-ui";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import livetvDescriptors from "../../mock/extend/descriptors/livetv.json";
import { IPTVProviders } from "./IPTV";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function mockBackend(sources: unknown[] = []) {
  const requests: { method: string; url: string; body?: string }[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      const method = (init?.method ?? "GET").toUpperCase();
      const body = typeof init?.body === "string" ? init.body : undefined;
      requests.push({ method, url, body });

      if (url.includes("/api/extend/descriptors") && method === "GET") {
        return jsonResponse({ descriptors: livetvDescriptors, version: 1 });
      }
      if (url.endsWith("/api/config") && method === "GET") {
        return jsonResponse({ catalog: { region: "US", language: "en-US" } });
      }
      if (url.endsWith("/api/livetv/sources") && method === "GET") {
        return jsonResponse(sources);
      }
      if (url.includes("/api/livetv/sources") && (method === "POST" || method === "PUT")) {
        const parsed = body ? (JSON.parse(body) as Record<string, unknown>) : {};
        return jsonResponse({
          id: (parsed.id as string) || "created-1",
          name: parsed.name ?? "Created",
          auth: parsed.auth,
          hasPlaylist: true,
          hasGuide: true,
        });
      }
      return jsonResponse({});
    }),
  );
  return requests;
}

function renderIPTV(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <ConfirmRoot />
      <Routes>
        <Route path="/sources/iptv" element={<IPTVProviders />} />
      </Routes>
    </MemoryRouter>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("IPTV source onboarding", () => {
  it("shows 'Guide found in the playlist' until an owner guide overrides it", async () => {
    mockBackend([
      {
        id: "m3u-auto",
        name: "Auto Guide M3U",
        playlistUrl: "https://iptv.example/playlist.m3u",
        playlistGuide: true,
        hasPlaylist: true,
        hasGuide: true,
        channelCount: 12,
        guideChannelCount: 12,
        programmeCount: 240,
        updatedAt: "2026-11-16T00:00:00Z",
      },
    ]);

    renderIPTV("/sources/iptv?source=m3u-auto");

    expect(await screen.findByDisplayValue("Auto Guide M3U")).toBeInTheDocument();
    expect(screen.getByText("Guide found in the playlist")).toBeInTheDocument();

    const guideInput = screen.getByLabelText("XMLTV guide (optional)");
    fireEvent.change(guideInput, { target: { value: "https://owner.example/custom.xml" } });
    await waitFor(() => {
      expect(screen.queryByText("Guide found in the playlist")).not.toBeInTheDocument();
    });

    fireEvent.change(guideInput, { target: { value: "" } });
    expect(await screen.findByText("Guide found in the playlist")).toBeInTheDocument();
  });
});
