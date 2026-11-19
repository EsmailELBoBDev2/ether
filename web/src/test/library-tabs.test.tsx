import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { LibraryTab } from "../pages/libraries/api";
import { calls, renderApp, settleUi } from "./harness";

const WAIT = { timeout: 15000 };

function lastPutTabs(fake: Parameters<typeof calls>[0], id: number): LibraryTab[] {
  const body = calls(fake, "PUT", `/libraries/${id}/tabs`).at(-1)?.body as
    | { tabs: LibraryTab[] }
    | undefined;
  return body?.tabs ?? [];
}

describe("Library tabs editor", () => {
  it("shows default tabs with the Vela app-scope note and links Recommended to Home Designer", async () => {
    renderApp("/libraries/1/settings");

    expect(
      await screen.findByRole("switch", { name: "Display Recommended tab" }, WAIT),
    ).toBeChecked();
    expect(
      screen.getByText("Tabs show in Vela. Plex, Jellyfin and Emby keep their own library tabs."),
    ).toBeInTheDocument();

    expect(screen.getByRole("button", { name: "Reset to default" })).toBeDisabled();
    expect(screen.getByRole("switch", { name: "Display Library tab" })).toBeChecked();

    fireEvent.click(screen.getByRole("button", { name: "Edit Recommended tab" }));
    const dialog = await screen.findByRole("dialog", { name: "Edit Recommended" }, WAIT);
    expect(
      within(dialog).getByText(
        /Rows on this tab come from Movies's Recommended layout in the Home Designer\. Customising that layout updates Vela, Plex and Jellyfin\/Emby\./,
      ),
    ).toBeInTheDocument();
    expect(within(dialog).getByRole("link", { name: "Open in Home Designer" })).toHaveAttribute(
      "href",
      "/home?section=library:1",
    );
  });

  it("renders plugin default tabs on a plugin library and resets to plugin tabs after editing", async () => {
    const { fake } = renderApp("/libraries/3/settings");

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
    const { fake } = renderApp("/libraries/4/settings");

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
    const { fake } = renderApp("/libraries/1/settings");
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
