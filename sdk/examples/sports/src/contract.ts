/**
 * Local mirror of the Ether v0.3.19 Live Events & Screens contract
 * (spec sections A1, A2, A3, and binding Vela wire shapes) and the
 * hosted QuickJS runtime host surface.
 *
 * NOTE: At the time this plugin was authored, `live_events`,
 * `contributes.liveFeeds`, and `contributes.screens` were being implemented
 * in parallel (W255/W256) and were not yet exported from `@ether/plugin-sdk`.
 * All runtime types are mirrored here with zero external imports so the
 * plugin compiles cleanly via `wasm.Compile` (Studio) and `kit.Pack`.
 */

export type LiveEventState =
  | "pre"
  | "in"
  | "post"
  | "delayed"
  | "postponed"
  | "canceled";

export interface LeagueRef {
  id: string;
  name: string;
  abbr: string;
  logo?: string;
}

export interface LiveEventStatus {
  state: LiveEventState;
  detail: string;
  period?: number;
  clock?: string;
  close?: boolean;
}

export interface Competitor {
  id: string;
  name: string;
  short?: string;
  abbr: string;
  logo?: string;
  color?: string;
  altColor?: string;
  score?: string;
  record?: string;
  rank?: number;
  home?: boolean;
  winner?: boolean;
  entityId?: string;
}

export interface RankingRow {
  pos: number;
  name: string;
  abbr?: string;
  detail?: string;
  entityId?: string;
}

export interface Probability {
  label: string;
  value: number;
  competitorId?: string;
}

/**
 * t is either a 0..1 game fraction (preferred when known) or an RFC3339
 * timestamp, matching the binding Vela wire contract.
 */
export interface ProbabilityPoint {
  t: number | string;
  value: number;
}

export interface Broadcast {
  name: string;
  kind: "tv" | "stream" | "radio";
  region?: string;
}

export interface MatchHints {
  terms: string[];
  callsigns: string[];
}

export interface LiveEvent {
  id: string;
  feed: string;
  category: string;
  league: LeagueRef;
  title?: string;
  start: string;
  status: LiveEventStatus;
  competitors?: Competitor[];
  ranking?: RankingRow[];
  situation?: Record<string, string>;
  probability?: Probability;
  venue?: string;
  broadcasts?: Broadcast[];
  matchHints?: MatchHints;
  updatedAt: string;
}

export interface GenericTable {
  title: string;
  columns: string[];
  rows: string[][];
}

export interface EventPlay {
  clock?: string;
  period?: string;
  text: string;
  emphasis: "score" | "key" | "normal";
  competitorId?: string;
}

export interface EventLink {
  label: string;
  url: string;
}

export interface LiveEventDetail extends LiveEvent {
  tables?: GenericTable[];
  plays?: EventPlay[];
  probabilitySeries?: ProbabilityPoint[];
  links?: EventLink[];
}

export interface LiveEntity {
  id: string;
  kind: string;
  name: string;
  abbr?: string;
  logo?: string;
  league?: string;
  color?: string;
  // Populated by Ether (W256) per viewer; omitted by the plugin.
  key?: string;
  favorite?: boolean;
}

export interface EventsResponse {
  events: LiveEvent[];
  nextPollSec: number;
}

export interface EntitiesResponse {
  entities: LiveEntity[];
}

export interface StandingsResponse {
  tables: GenericTable[];
}

// Mirrored manifest contribution types (spec A1, A3 & binding Vela shapes).
export interface LiveFeedContribution {
  id: string;
  title: string;
  minIntervalSec: number;
  entityKinds?: string[];
}

export interface ScreenSectionQuery {
  state?: LiveEventState[];
  favorites?: boolean;
  window?: "today" | "week" | "recent";
  league?: string;
}

export interface ScreenSection {
  id: string;
  type: "leagues" | "hero" | "events" | "schedule" | "table" | "entities";
  title?: string;
  feed: string;
  query?: ScreenSectionQuery;
  source?: string;
  entityKind?: string;
}

export interface ScreenContribution {
  id: string;
  title: string;
  icon: string;
  sections: ScreenSection[];
}

// Hosted runtime request / response / globalThis.ether host bindings.
export interface HostedRequest {
  method?: string;
  path: string;
  query?: Record<string, string | string[]>;
  headers?: Record<string, string>;
  body?: unknown;
}

export interface HostedResponse {
  status: number;
  headers?: Record<string, string>;
  body: unknown;
}

export interface HostFetchRequest {
  method?: string;
  url: string;
  headers?: Record<string, string>;
  body?: string;
  timeoutMs?: number;
}

export interface HostFetchResponse {
  status: number;
  headers?: Record<string, string>;
  body: string;
}

export interface HostFetchError {
  code?: string;
  message: string;
}

export type HostFetchBatchResult =
  | HostFetchResponse
  | { error: HostFetchError };

export interface HostedEtherGlobal {
  settings: {
    get(): Record<string, unknown>;
  };
  kv: {
    get(key: string): string | null;
    put(key: string, value: string): void;
    delete(key: string): void;
    list?(prefix?: string): string[];
  };
  http: {
    fetch(req: HostFetchRequest): HostFetchResponse;
    fetchMany?(
      reqs: HostFetchRequest[],
      opts?: { concurrency?: number },
    ): HostFetchBatchResult[];
  };
  log?(level: string, message: string, fields?: Record<string, unknown>): void;
}
