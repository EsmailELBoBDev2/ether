import { describe, expect, it } from "vitest";
import { createFlowsMock, describeFlow, flowTemplates, validateFlowGraph, _simulateForTests } from "./flows";
import type { Flow, Graph, TestRunResult } from "../pages/flows/types";

const q = new URLSearchParams();

describe("flows mock", () => {
  it("serves the catalogue and templates", async () => {
    const mock = createFlowsMock();
    const events = mock.handle("GET", ["flows", "catalogue", "events"], q, null);
    expect(events?.status).toBe(200);
    expect((events?.body as { events: unknown[] }).events.length).toBeGreaterThan(20);
    expect(flowTemplates().length).toBeGreaterThanOrEqual(7);
    for (const t of flowTemplates()) expect(validateFlowGraph(t.graph).valid).toBe(true);
  });

  it("rejects a graph with no trigger and accepts the 4K recipe", () => {
    const bad: Graph = { schemaVersion: 1, nodes: [{ id: "a", kind: "action", position: { x: 0, y: 0 }, config: { actionType: "notify.send", params: {} } }], edges: [] };
    const result = validateFlowGraph(bad);
    expect(result.valid).toBe(false);
    expect(result.errors?.some((e) => e.code === "no_trigger")).toBe(true);
  });

  it("test run walks the 4K recipe to the stop action for the sample payload", () => {
    const mock = createFlowsMock();
    const list = mock.handle("GET", ["flows"], q, null)?.body as { flows: Flow[] };
    const flow = list.flows.find((f) => f.templateId === "stop-remote-4k-transcodes")!;
    const res = mock.handle("POST", ["flows", flow.id, "test"], q, { dryRun: true, event: { type: "playback.session.started" } });
    const { steps } = res?.body as TestRunResult;
    const kinds = steps.map((s) => s.nodeKind);
    expect(kinds[0]).toBe("trigger.event");
    expect(kinds).toContain("condition");
    expect(kinds).toContain("action");
    const cond = steps.find((s) => s.nodeKind === "condition")!;
    expect(cond.branch).toBe("true");
    const trace = (cond.output as { trace: { rules: { matched: boolean }[] } }).trace;
    expect(trace.rules.every((r) => r.matched)).toBe(true);
  });

  it("describe turns the owner's example into a stop-transcode draft", () => {
    const draft = describeFlow("stop video transcodes for viewers away from home");
    expect(draft.graph.nodes.some((n) => n.config?.actionType === "playback.stop_session")).toBe(true);
    expect(draft.explanation.length).toBeGreaterThan(20);
  });

  it("simulator respects a false branch", () => {
    const t = flowTemplates().find((x) => x.id === "stop-remote-4k-transcodes")!;
    const steps = _simulateForTests(t.graph, { stream: { videoDecision: "copy" }, source: { videoHeight: 1080 }, network: { location: "lan" }, viewer: { tags: [] } }, "playback.session.started", "run_x");
    expect(steps.some((s) => s.nodeKind === "action")).toBe(false);
  });
});
