import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { HomeLayout } from "../lib/contract";
import type { LibraryTab } from "../pages/libraries/api";
import { calls, renderApp, settleUi } from "./harness";

const WAIT = { timeout: 15000 };

function lastPutTabs(fake: Parameters<typeof calls>[0], id: number): LibraryTab[] {
  const body = calls(fake, "PUT", `/libraries/${id}/tabs`).at(-1)?.body as
    | { tabs: LibraryTab[] }
    | undefined;
  return body?.tabs ?? [];
}

function lastPutLayout(fake: Parameters<typeof calls>[0]): HomeLayout | undefined {
  const body = calls(fake, "PUT", "/layout").at(-1)?.body as
    | { layout: HomeLayout }
    | undefined;
  return body?.layout;
}

describe("Library tabs page", () => {
  it("lives on its own top-level Tabs tab (/libraries/<id>/tabs) and keeps Home Designer on Home, Movies and TV only", async () => {
    const first = renderApp("/libraries/1/settings");

    const nav = await screen.findByRole("tablist", { name: "Library sections" }, WAIT);
    expect(within(nav).getAllByRole("tab").map((el) => el.textContent)).toEqual([
      "Browse",
      "Rules",
      "Tabs",
      "Settings",
    ]);
    expect(screen.queryByRole("switch", { name: "Display Recommended tab" })).toBeNull();

    fireEvent.click(within(nav).getByRole("tab", { name: "Tabs" }));
    expect(
      await screen.findByRole("switch", { name: "Display Recommended tab" }, WAIT),
    ).toBeChecked();
    expect(
      screen.getByText("Tabs show in Vela. Plex, Jellyfin and Emby keep their own library tabs."),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reset to default" })).toBeDisabled();
    expect(screen.getByRole("switch", { name: "Display Library tab" })).toBeChecked();

    first.unmount();
    renderApp("/home?section=library:1");
    const pageBtn = await screen.findByRole("button", { name: "Page" }, WAIT);
    expect(pageBtn).toHaveTextContent("Home");
    fireEvent.click(pageBtn);
    const pageMenu = screen.getByRole("menu", { name: "Page" });
    expect(within(pageMenu).getAllByRole("menuitemradio").map((el) => el.textContent)).toEqual([
      "Home",
      "Movies",
      "TV",
    ]);
  });

  it("edits Recommended rows inside the tab sheet (before customising → choose rows → save, and go back to standard rows)", async () => {
    const { fake } = renderApp("/libraries/1/tabs");

    await screen.findByRole("switch", { name: "Display Recommended tab" }, WAIT);
    expect(screen.getByText("Personalized rows")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Edit Recommended tab" }));
    const dialog = await screen.findByRole("dialog", { name: "Edit Recommended" }, WAIT);

    // No Home Designer link or "shared layout" / "everyone's Home" jargon.
    expect(within(dialog).queryByRole("link", { name: /Home Designer/i })).toBeNull();
    expect(within(dialog).queryByText(/shared .* layout/i)).toBeNull();
    expect(within(dialog).queryByText(/everyone's Home/i)).toBeNull();

    const rowsSection = within(dialog).getByRole("region", { name: "Rows on this tab" });
    expect(
      within(rowsSection).getByText(
        "These rows also show on this library's Recommended page in Plex, Jellyfin and Emby.",
      ),
    ).toBeInTheDocument();
    expect(
      within(rowsSection).getByText("Uses the standard rows for Movies libraries."),
    ).toBeInTheDocument();

    // Customise rows for this library.
    fireEvent.click(within(rowsSection).getByRole("button", { name: "Choose rows for this library" }));

    expect(
      within(rowsSection).getByRole("button", { name: "Go back to the standard rows" }),
    ).toBeInTheDocument();
    expect(within(rowsSection).getByRole("region", { name: "Pinned rows" })).toBeInTheDocument();
    expect(within(rowsSection).getByRole("region", { name: "Available rows" })).toBeInTheDocument();

    // Relevant movie collections appear under Collections; TV-only rows do not.
    const collectionsGroup = within(rowsSection).getByRole("group", { name: "Collections" });
    expect(
      within(collectionsGroup).getByRole("listitem", { name: "Row Weeknight wind-down" }),
    ).toBeInTheDocument();
    expect(
      within(collectionsGroup).getByRole("listitem", { name: "Row Family Movie Night" }),
    ).toBeInTheDocument();
    expect(
      within(rowsSection).queryByRole("listitem", { name: "Row New Seasons of Your Shows" }),
    ).toBeNull();

    // Hide a row and save; persists library:1 on /layout without converting default tabs to owner.
    const popularRow = within(rowsSection).getByRole("listitem", {
      name: "Row Popular on This Server",
    });
    fireEvent.click(within(popularRow).getByRole("radio", { name: "Off" }));

    fireEvent.click(within(dialog).getByRole("button", { name: "Save tab" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull(), WAIT);

    await settleUi(() => {
      expect(calls(fake, "PUT", "/layout")).toHaveLength(1);
    });
    expect(calls(fake, "PUT", "/libraries/1/tabs")).toHaveLength(0);
    expect(lastPutLayout(fake)?.global.find((p) => p.section === "library:1")?.rows).toEqual([
      { row: "popular-on-server", status: "off" },
    ]);
    expect(await screen.findByText("Personalized rows · Custom rows")).toBeInTheDocument();
    expect(screen.getByText("Default")).toBeInTheDocument();
  });

  it("opens a TV library with customised Recommended rows and lets the owner go back to the standard rows", async () => {
    const { fake } = renderApp("/libraries/7/tabs?editTab=recommended");

    const dialog = await screen.findByRole("dialog", { name: "Edit Recommended" }, WAIT);
    const rowsSection = within(dialog).getByRole("region", { name: "Rows on this tab" });

    // Seeded pinned TV rows are present; movie-only collections are filtered out.
    const pinned = within(rowsSection).getByRole("region", { name: "Pinned rows" });
    expect(within(pinned).getByText("Finish What You Started")).toBeInTheDocument();
    expect(within(pinned).getByText("New Seasons of Your Shows")).toBeInTheDocument();
    const collectionsGroup = within(rowsSection).getByRole("group", { name: "Collections" });
    expect(
      within(collectionsGroup).queryByRole("listitem", { name: "Row Family Movie Night" }),
    ).toBeNull();
    expect(
      within(collectionsGroup).getByRole("listitem", { name: "Row Recently Added" }),
    ).toBeInTheDocument();

    fireEvent.click(
      within(rowsSection).getByRole("button", { name: "Go back to the standard rows" }),
    );
    expect(
      within(rowsSection).getByText("Uses the standard rows for TV shows libraries."),
    ).toBeInTheDocument();
    expect(
      within(rowsSection).getByRole("button", { name: "Choose rows for this library" }),
    ).toBeInTheDocument();

    fireEvent.click(within(dialog).getByRole("button", { name: "Save tab" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull(), WAIT);

    await settleUi(() => {
      expect(calls(fake, "PUT", "/layout")).toHaveLength(1);
    });
    expect(lastPutLayout(fake)?.global.find((p) => p.section === "library:7")).toBeUndefined();
  });

  it("renders plugin default tabs on a plugin library and resets to plugin tabs after editing", async () => {
    const { fake } = renderApp("/libraries/3/tabs");

    expect(await screen.findByRole("switch", { name: "Display Subjects tab" }, WAIT)).toBeChecked();
    expect(screen.getByText("Plugin default")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reset to plugin tabs" })).toBeDisabled();
    expect(screen.getByRole("switch", { name: "Display Authors tab" })).toBeChecked();

    fireEvent.click(screen.getByRole("button", { name: "Delete Authors tab" }));
    await settleUi(() => {
      expect(lastPutTabs(fake, 3).map((t) => t.key)).toEqual([
        "recommended",
        "library",
        "subjects",
      ]);
    });
    expect(await screen.findByText("Customized")).toBeInTheDocument();

    const resetBtn = screen.getByRole("button", { name: "Reset to plugin tabs" });
    expect(resetBtn).not.toBeDisabled();
    fireEvent.click(resetBtn);
    await settleUi(() => {
      expect(calls(fake, "DELETE", "/libraries/3/tabs")).toHaveLength(1);
    });
    expect(await screen.findByRole("switch", { name: "Display Authors tab" })).toBeChecked();
    expect(screen.getByText("Plugin default")).toBeInTheDocument();
  });

  it("renders the empty state when all tabs are removed and restores standard tabs on reset", async () => {
    const { fake } = renderApp("/libraries/4/tabs");

    expect(await screen.findByText("No tabs configured", {}, WAIT)).toBeInTheDocument();
    const resetButtons = screen.getAllByRole("button", { name: "Reset to default" });
    fireEvent.click(resetButtons[0]!);
    await settleUi(() => {
      expect(calls(fake, "DELETE", "/libraries/4/tabs")).toHaveLength(1);
    });
    expect(await screen.findByRole("switch", { name: "Display Recommended tab" })).toBeChecked();
    expect(screen.getByRole("switch", { name: "Display Library tab" })).toBeChecked();
  });

  it("adds Collections, People, and Saved view tabs with live previews, toggles visibility, and saves", async () => {
    const { fake } = renderApp("/libraries/1/tabs");
    await screen.findByRole("switch", { name: "Display Library tab" }, WAIT);

    // 1. Add a "Collections by…" tab grouped by Decade, ordered Name A–Z
    fireEvent.click(screen.getByRole("button", { name: "Add tab" }));
    const picker1 = await screen.findByRole("dialog", { name: "Add tab" }, WAIT);
    expect(within(picker1).getByText("Already in tabs")).toBeInTheDocument();
    fireEvent.click(within(picker1).getByRole("button", { name: /Collections by…/ }));

    const colDialog = await screen.findByRole(
      "dialog",
      { name: "New Collections by… tab" },
      WAIT,
    );
    fireEvent.click(within(colDialog).getByRole("radio", { name: "Decade" }));
    expect(within(colDialog).getByLabelText("Tab label")).toHaveValue("Decades");
    const bucketPreview = await within(colDialog).findByRole(
      "status",
      { name: "Live preview" },
      WAIT,
    );
    await settleUi(() => {
      expect(bucketPreview).toHaveTextContent(/\d+ collections? in Movies/);
    });
    fireEvent.click(
      within(within(colDialog).getByRole("radiogroup", { name: "Bucket order" })).getByRole(
        "radio",
        { name: "Name A–Z" },
      ),
    );
    fireEvent.click(within(colDialog).getByRole("button", { name: "Add tab" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull(), WAIT);

    await settleUi(() => {
      expect(lastPutTabs(fake, 1)).toContainEqual(
        expect.objectContaining({
          key: "decades",
          label: "Decades",
          kind: "collections",
          facet: "decade",
          order: "name",
        }),
      );
    });

    // 2. Add a "People in…" tab grouped by Directors, ordered by Most titles
    fireEvent.click(screen.getByRole("button", { name: "Add tab" }));
    const picker2 = await screen.findByRole("dialog", { name: "Add tab" }, WAIT);
    fireEvent.click(within(picker2).getByRole("button", { name: /People in…/ }));

    const peopleDialog = await screen.findByRole("dialog", { name: "New People in… tab" }, WAIT);
    fireEvent.click(within(peopleDialog).getByRole("radio", { name: "Directors" }));
    expect(within(peopleDialog).getByLabelText("Tab label")).toHaveValue("Directors");
    const peoplePreview = await within(peopleDialog).findByRole(
      "status",
      { name: "Live preview" },
      WAIT,
    );
    await settleUi(() => {
      expect(peoplePreview).toHaveTextContent(/\d+ (person|people) in Movies/);
    });
    fireEvent.click(
      within(within(peopleDialog).getByRole("radiogroup", { name: "People order" })).getByRole(
        "radio",
        { name: "Most titles" },
      ),
    );
    fireEvent.click(within(peopleDialog).getByRole("button", { name: "Add tab" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull(), WAIT);

    await settleUi(() => {
      expect(lastPutTabs(fake, 1)).toContainEqual(
        expect.objectContaining({
          key: "directors",
          label: "Directors",
          kind: "people",
          facet: "director",
          order: "count",
        }),
      );
    });

    // 3. Add a "Saved view" tab with rules + live matching title count + custom search placeholder
    fireEvent.click(screen.getByRole("button", { name: "Add tab" }));
    const picker3 = await screen.findByRole("dialog", { name: "Add tab" }, WAIT);
    fireEvent.click(within(picker3).getByRole("button", { name: /Saved view/ }));

    const viewDialog = await screen.findByRole("dialog", { name: "New Saved view tab" }, WAIT);
    expect(within(viewDialog).getByLabelText("Tab label")).toHaveValue("Unwatched");
    const rulesPreview = await within(viewDialog).findByRole(
      "status",
      { name: "Matching titles preview" },
      WAIT,
    );
    await settleUi(() => {
      expect(rulesPreview).toHaveTextContent(/\d+ of \d+ titles match/);
    });
    fireEvent.change(within(viewDialog).getByLabelText("Search placeholder"), {
      target: { value: "Find unwatched movies…" },
    });
    fireEvent.click(within(viewDialog).getByRole("button", { name: "Add tab" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull(), WAIT);

    await settleUi(() => {
      expect(lastPutTabs(fake, 1)).toContainEqual(
        expect.objectContaining({
          key: "unwatched",
          label: "Unwatched",
          kind: "view",
          search: true,
          searchPlaceholder: "Find unwatched movies…",
        }),
      );
    });

    // 4. Hide the Decades tab via its inline switch
    fireEvent.click(screen.getByRole("switch", { name: "Display Decades tab" }));
    await settleUi(() => {
      expect(lastPutTabs(fake, 1)).toContainEqual(
        expect.objectContaining({
          key: "decades",
          hidden: true,
        }),
      );
    });
    expect(await screen.findByText("Hidden")).toBeInTheDocument();
  });
});
