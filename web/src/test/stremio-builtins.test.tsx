import { fireEvent, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { renderApp } from "./harness";

describe("Stremio built-ins", () => {
  it("adds a subtitle plugin from the subtitle providers page", async () => {
    renderApp("/playback/subtitles");
    fireEvent.click(await screen.findByRole("button", { name: "Add a plugin from Stremio" }));
    expect(
      await screen.findByRole("dialog", { name: "Add a plugin from Stremio" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "Add a subtitle provider in Plugins" }),
    ).toHaveAttribute("href", expect.stringContaining("/plugins"));
    expect(screen.queryByLabelText("Plugin URL")).not.toBeInTheDocument();
  });

  it("adds a Live TV plugin through the shared plugin drawer", async () => {
    renderApp("/livetv");
    fireEvent.click(await screen.findByRole("button", { name: "Add" }));
    fireEvent.click(await screen.findByRole("menuitem", { name: /Stremio plugin/ }));
    expect(await screen.findByText("Install link")).toBeInTheDocument();
    expect(screen.getByPlaceholderText("https://… /manifest.json")).toBeInTheDocument();
    expect(screen.queryByLabelText("Plugin URL")).not.toBeInTheDocument();
  });
});
