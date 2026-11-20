import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { ClientIdentityConfig } from "../lib/types";
import { calls, renderApp } from "./harness";

const WAIT = { timeout: 4000 };
const DEFAULT_UA =
  "Ether/dev (+https://github.com/braedonsaunders/ether/blob/main/docs/indexer-operators.md)";

describe("Sources › Indexers identity rules", () => {
  it("shows the compact honest-default card and opens the rules drawer", async () => {
    renderApp("/sources/indexers");
    await screen.findByRole("heading", { name: "How Ether identifies itself" }, WAIT);
    expect(screen.getByText("Ether (honest, recommended)")).toBeInTheDocument();
    expect(screen.getByText("No rules")).toBeInTheDocument();
    expect(screen.getByText(DEFAULT_UA)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Manage rules" }));
    const drawer = await screen.findByRole("dialog", { name: "How Ether identifies itself" }, WAIT);
    expect(within(drawer).getByText("Default")).toBeInTheDocument();
    expect(within(drawer).getByRole("button", { name: "Add rule" })).toBeInTheDocument();
  });

  it("adds a rule with Advanced header/parameter edits and renders masked preview", async () => {
    const { fake } = renderApp("/sources/indexers");
    await screen.findByRole("heading", { name: "How Ether identifies itself" }, WAIT);
    fireEvent.click(screen.getByRole("button", { name: "Manage rules" }));
    const drawer = await screen.findByRole("dialog", { name: "How Ether identifies itself" }, WAIT);
    fireEvent.click(within(drawer).getByRole("button", { name: "Add rule" }));

    const editor = await screen.findByRole("dialog", { name: "Add identity rule" }, WAIT);
    fireEvent.click(within(editor).getByRole("button", { name: /Advanced/ }));
    fireEvent.click(within(editor).getByRole("button", { name: "Add header edit" }));
    fireEvent.change(within(editor).getByLabelText("Header name"), {
      target: { value: "X-Owner" },
    });
    fireEvent.change(within(editor).getByLabelText("Header value"), {
      target: { value: "yes" },
    });

    await waitFor(() => expect(within(editor).getAllByText(/apikey=••••/)).toHaveLength(2), WAIT);

    fireEvent.click(within(editor).getByRole("button", { name: "Save rule" }));
    await waitFor(() => expect(calls(fake, "PUT", "/config/clientIdentity")).toHaveLength(1), WAIT);
    const body = calls(fake, "PUT", "/config/clientIdentity")[0]!.body as ClientIdentityConfig;
    expect(body.rules).toHaveLength(1);
    expect(body.rules![0]).toMatchObject({
      target: "indexer",
      indexerId: "i1",
      profile: "sonarr",
      headers: [{ name: "X-Owner", value: "yes" }],
    });
  });

  it("shows read-only Identifies as summary inside an indexer drawer and opens rules drawer", async () => {
    renderApp("/sources/indexers", {}, (fake) => {
      fake.state.config.clientIdentity = {
        rules: [
          { id: "r1", target: "indexer", indexerId: "i1", profile: "sonarr" },
          { id: "r2", target: "nzb_downloads", profile: "sabnzbd" },
        ],
      };
    });
    await screen.findByRole("heading", { name: "How Ether identifies itself" }, WAIT);
    expect(screen.getByText("2 rules")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Edit Indexer One" }));
    const drawer = await screen.findByRole("dialog", { name: "Indexer One" }, WAIT);
    expect(
      within(drawer).getByText("Identifies as: Compatible with Sonarr (from rule: Indexer One)"),
    ).toBeInTheDocument();

    fireEvent.click(within(drawer).getByRole("button", { name: "Manage rules" }));
    await screen.findByRole("dialog", { name: "How Ether identifies itself" }, WAIT);
  });

  it("runs a capabilities test for a chosen indexer from the rules drawer", async () => {
    const { fake } = renderApp("/sources/indexers", {}, (f) => {
      f.state.config.clientIdentity = {
        rules: [{ id: "r1", target: "indexer", indexerId: "i1", profile: "sonarr" }],
      };
    });
    await screen.findByRole("heading", { name: "How Ether identifies itself" }, WAIT);
    fireEvent.click(screen.getByRole("button", { name: "Manage rules" }));
    const drawer = await screen.findByRole("dialog", { name: "How Ether identifies itself" }, WAIT);

    fireEvent.click(within(drawer).getByRole("button", { name: "Test" }));
    await waitFor(
      () => expect(calls(fake, "POST", "/test/indexer-identity")).toHaveLength(1),
      WAIT,
    );
    await waitFor(
      () =>
        expect(
          within(drawer).getByText(/Capabilities answered using Compatible with Sonarr\./),
        ).toBeInTheDocument(),
      WAIT,
    );
  });
});
