/**
 * ESPN public JSON API provider implementing SportsProvider.
 *
 * Public endpoints used:
 * - https://site.api.espn.com/apis/site/v2/sports/{sport}/{league}/scoreboard
 * - https://site.api.espn.com/apis/site/v2/sports/{sport}/{league}/summary?event={id}
 * - https://site.api.espn.com/apis/site/v2/sports/{sport}/{league}/teams?limit=500
 * - https://site.web.api.espn.com/apis/v2/sports/{sport}/{league}/standings
 */

import {
  computeNextPollSec,
  computeScoreboardTtlMs,
  isLiveState,
  TTL_IDLE_MS,
  TTL_LIVE_MS,
  TTL_STANDINGS_MS,
  TTL_TEAMS_MS,
} from "../cache.ts";
import { isCloseGame, parseElapsedSoccerMinutes, parseRemainingClockSec } from "../close.ts";
import type {
  Broadcast,
  Competitor,
  EventLink,
  EventPlay,
  GenericTable,
  HostFetchRequest,
  LeagueRef,
  LiveEntity,
  LiveEvent,
  LiveEventDetail,
  LiveEventState,
  LiveEventStatus,
  Probability,
  ProbabilityPoint,
  RankingRow,
} from "../contract.ts";
import {
  buildMatchHints,
  extractBroadcasts,
  type CompetitorHintInput,
  type RawEspnBroadcast,
  type RawEspnGeoBroadcast,
} from "../hints.ts";
import {
  formatEntityId,
  formatEventId,
  parseEventId,
  type LeagueConfig,
} from "../leagues.ts";
import {
  fetchManyBounded,
  type EntitiesQuery,
  type ProviderRuntime,
  type ScoreboardQuery,
  type ScoreboardResult,
  type SportsProvider,
  type StandingsQuery,
} from "./types.ts";

const ESPN_SITE_API = "https://site.api.espn.com/apis/site/v2/sports";
const ESPN_WEB_API = "https://site.web.api.espn.com/apis/v2/sports";
const FEED_ID = "sports";
const DAY_MS = 86_400_000;
const SINGLE_DAY_COALESCE_MS = 26 * 3_600_000;
const MAX_RANGE_DAYS = 7;
const MAX_PLAYS = 40;
const MAX_PROBABILITY_POINTS = 100;
const MAX_SCOREBOARD_RANKING = 10;

// --- Raw ESPN JSON shapes (defensive / partial) ---

interface RawEspnLogo {
  href?: string;
}

interface RawEspnLink {
  href?: string;
  text?: string;
  shortText?: string;
  rel?: string[];
}

interface RawEspnStatusType {
  name?: string;
  state?: string;
  completed?: boolean;
  description?: string;
  detail?: string;
  shortDetail?: string;
}

interface RawEspnStatus {
  clock?: number;
  displayClock?: string;
  period?: number;
  type?: RawEspnStatusType;
}

interface RawEspnTeam {
  id?: string;
  location?: string;
  name?: string;
  abbreviation?: string;
  displayName?: string;
  shortDisplayName?: string;
  color?: string;
  alternateColor?: string;
  logo?: string;
  logos?: RawEspnLogo[];
}

interface RawEspnAthlete {
  id?: string;
  fullName?: string;
  displayName?: string;
  shortName?: string;
  abbreviation?: string;
  flag?: { href?: string; alt?: string };
}

interface RawEspnLineScore {
  value?: number;
  displayValue?: string;
  period?: number;
}

interface RawEspnRecord {
  name?: string;
  type?: string;
  summary?: string;
}

interface RawEspnCompetitor {
  id?: string;
  type?: string;
  order?: number;
  homeAway?: string;
  winner?: boolean;
  score?: string | number | { displayValue?: string; value?: number };
  linescores?: RawEspnLineScore[];
  records?: RawEspnRecord[];
  curatedRank?: { current?: number };
  team?: RawEspnTeam;
  athlete?: RawEspnAthlete;
}

interface RawEspnSituation {
  downDistanceText?: string;
  shortDownDistanceText?: string;
  possessionText?: string;
  possession?: string;
  isRedZone?: boolean;
  outs?: number;
  balls?: number;
  strikes?: number;
  onFirst?: boolean;
  onSecond?: boolean;
  onThird?: boolean;
  lastPlay?: {
    text?: string;
    probability?: {
      homeWinPercentage?: number;
      awayWinPercentage?: number;
      tiePercentage?: number;
    };
  };
}

interface RawEspnVenue {
  fullName?: string;
  address?: { city?: string; state?: string; country?: string };
}

interface RawEspnCompetition {
  id?: string;
  date?: string;
  startDate?: string;
  type?: { id?: string; abbreviation?: string; text?: string };
  venue?: RawEspnVenue;
  status?: RawEspnStatus;
  broadcasts?: RawEspnBroadcast[];
  geoBroadcasts?: RawEspnGeoBroadcast[];
  competitors?: RawEspnCompetitor[];
  situation?: RawEspnSituation;
}

interface RawEspnEvent {
  id?: string;
  date?: string;
  endDate?: string;
  name?: string;
  shortName?: string;
  status?: RawEspnStatus;
  links?: RawEspnLink[];
  circuit?: RawEspnVenue;
  venues?: RawEspnVenue[];
  competitions?: RawEspnCompetition[];
}

interface RawEspnScoreboard {
  leagues?: Array<{
    id?: string;
    name?: string;
    abbreviation?: string;
    logos?: RawEspnLogo[];
  }>;
  events?: RawEspnEvent[];
}

interface RawEspnPlay {
  id?: string;
  text?: string;
  shortText?: string;
  scoringPlay?: boolean;
  isTurnover?: boolean;
  clock?: { value?: number; displayValue?: string };
  period?: { number?: number; displayValue?: string };
  type?: { text?: string; type?: string; abbreviation?: string };
  team?: { id?: string; abbreviation?: string; displayName?: string };
}

interface RawEspnDrive {
  team?: { id?: string; abbreviation?: string };
  plays?: RawEspnPlay[];
}

interface RawEspnWinProb {
  homeWinPercentage?: number;
  tiePercentage?: number;
  playId?: string;
}

interface RawEspnSummary {
  header?: {
    id?: string;
    links?: RawEspnLink[];
    competitions?: RawEspnCompetition[];
  };
  gameInfo?: {
    venue?: RawEspnVenue;
  };
  broadcasts?: RawEspnBroadcast[];
  boxscore?: {
    teams?: Array<{
      team?: RawEspnTeam;
      homeAway?: string;
      statistics?: Array<{
        name?: string;
        label?: string;
        displayValue?: string;
      }>;
    }>;
    players?: Array<{
      team?: RawEspnTeam;
      statistics?: Array<{
        name?: string;
        text?: string;
        labels?: string[];
        athletes?: Array<{
          athlete?: RawEspnAthlete;
          stats?: string[];
          didNotPlay?: boolean;
        }>;
      }>;
    }>;
  };
  drives?: {
    current?: RawEspnDrive;
    previous?: RawEspnDrive[];
  };
  scoringPlays?: RawEspnPlay[];
  plays?: RawEspnPlay[];
  keyEvents?: RawEspnPlay[];
  winprobability?: RawEspnWinProb[];
}

interface RawEspnTeamsResponse {
  sports?: Array<{
    leagues?: Array<{
      teams?: Array<{ team?: RawEspnTeam }>;
    }>;
  }>;
}

interface RawEspnStandingStat {
  name?: string;
  abbreviation?: string;
  displayValue?: string;
  value?: number;
}

interface RawEspnStandingEntry {
  team?: RawEspnTeam;
  athlete?: RawEspnAthlete;
  stats?: RawEspnStandingStat[];
}

interface RawEspnStandingsNode {
  name?: string;
  abbreviation?: string;
  standings?: {
    name?: string;
    entries?: RawEspnStandingEntry[];
  };
  children?: RawEspnStandingsNode[];
}

// --- Pure helpers ---

function toRfc3339(raw: string | undefined, fallbackMs: number): string {
  if (raw) {
    const ms = Date.parse(raw);
    if (!Number.isNaN(ms)) {
      return new Date(ms).toISOString();
    }
  }
  return new Date(fallbackMs).toISOString();
}

function normalizeHexColor(raw?: string): string | undefined {
  if (!raw) return undefined;
  const clean = raw.trim().replace(/^#/, "");
  if (/^[0-9a-fA-F]{6}$/.test(clean) || /^[0-9a-fA-F]{3}$/.test(clean)) {
    return `#${clean.toLowerCase()}`;
  }
  return undefined;
}

function formatYyyymmddUtc(ms: number): string {
  const d = new Date(ms);
  const y = d.getUTCFullYear();
  const m = String(d.getUTCMonth() + 1).padStart(2, "0");
  const day = String(d.getUTCDate()).padStart(2, "0");
  return `${y}${m}${day}`;
}

function parseDateParam(raw?: string): number | null {
  if (!raw) return null;
  const trimmed = raw.trim();
  if (!trimmed) return null;
  if (/^\d{4}-\d{2}-\d{2}$/.test(trimmed)) {
    const ms = Date.parse(`${trimmed}T00:00:00Z`);
    return Number.isNaN(ms) ? null : ms;
  }
  if (/^\d{8}$/.test(trimmed)) {
    const iso = `${trimmed.slice(0, 4)}-${trimmed.slice(4, 6)}-${trimmed.slice(6, 8)}T00:00:00Z`;
    const ms = Date.parse(iso);
    return Number.isNaN(ms) ? null : ms;
  }
  const ms = Date.parse(trimmed);
  return Number.isNaN(ms) ? null : ms;
}

function enumerateDateKeys(
  fromMs: number | null,
  toMs: number | null,
  multiDayEvent: boolean,
): string[] {
  if (fromMs === null && toMs === null) {
    return ["current"];
  }
  const start = fromMs ?? (toMs! - DAY_MS);
  let end = toMs ?? (start + DAY_MS - 1);
  if (end < start) {
    end = start;
  }
  const clampedEnd = Math.min(end, start + (MAX_RANGE_DAYS - 1) * DAY_MS);

  if (multiDayEvent) {
    // Multi-day motorsport/golf/UFC events start 1-3 days before Sunday finals.
    const rangeStart = formatYyyymmddUtc(start - 3 * DAY_MS);
    const rangeEnd = formatYyyymmddUtc(clampedEnd + DAY_MS);
    return [`${rangeStart}-${rangeEnd}`];
  }

  // Team sports reject YYYYMMDD-YYYYMMDD ranges with HTTP 400 on ESPN;
  // coalesce <=26h timezone-shifted single-day windows into one ESPN date.
  if (clampedEnd - start <= SINGLE_DAY_COALESCE_MS) {
    const mid = Math.floor((start + clampedEnd) / 2);
    return [formatYyyymmddUtc(mid)];
  }

  const out: string[] = [];
  const startDay = Date.UTC(
    new Date(start).getUTCFullYear(),
    new Date(start).getUTCMonth(),
    new Date(start).getUTCDate(),
  );
  const endDay = Date.UTC(
    new Date(clampedEnd).getUTCFullYear(),
    new Date(clampedEnd).getUTCMonth(),
    new Date(clampedEnd).getUTCDate(),
  );
  for (let d = startDay; d <= endDay && out.length < MAX_RANGE_DAYS; d += DAY_MS) {
    out.push(formatYyyymmddUtc(d));
  }
  return out.length > 0 ? out : ["current"];
}

function mapEventState(status?: RawEspnStatus): LiveEventState {
  const name = (status?.type?.name || "").toUpperCase();
  const state = (status?.type?.state || "").toLowerCase();
  if (name.includes("POSTPONED")) return "postponed";
  if (name.includes("CANCEL")) return "canceled";
  if (
    name.includes("DELAY") ||
    name.includes("SUSPENDED") ||
    name.includes("INTERRUPTED")
  ) {
    return "delayed";
  }
  if (state === "in") return "in";
  if (state === "post") return "post";
  return "pre";
}

function formatPeriodLabel(league: LeagueConfig, period: number): string {
  if (period <= 0) return "";
  switch (league.category) {
    case "football":
      return period <= 4 ? `Q${period}` : period === 5 ? "OT" : `${period - 4}OT`;
    case "basketball":
      if (league.regulationPeriods === 2) {
        return period <= 2 ? `${period}H` : period === 3 ? "OT" : `${period - 2}OT`;
      }
      return period <= 4 ? `Q${period}` : period === 5 ? "OT" : `${period - 4}OT`;
    case "hockey":
      if (period === 1) return "1st";
      if (period === 2) return "2nd";
      if (period === 3) return "3rd";
      if (period === 4) return "OT";
      return period === 5 ? "SO" : `${period - 3}OT`;
    case "baseball":
      return `Inn ${period}`;
    case "soccer":
      return period === 1 ? "1H" : period === 2 ? "2H" : "ET";
    default:
      return `P${period}`;
  }
}

function formatStatusDetail(
  league: LeagueConfig,
  state: LiveEventState,
  rawStatus?: RawEspnStatus,
): string {
  const type = rawStatus?.type;
  const typeName = (type?.name || "").toUpperCase();
  if (state === "in") {
    if (typeName === "STATUS_HALFTIME") return "HT";
    if (typeName === "STATUS_END_PERIOD") {
      return type?.shortDetail || type?.detail || "End of period";
    }
    if (league.category === "soccer") {
      return rawStatus?.displayClock || type?.shortDetail || type?.detail || "Live";
    }
    const period = rawStatus?.period;
    const clock = rawStatus?.displayClock;
    if (
      period &&
      period > 0 &&
      clock &&
      (league.category === "football" ||
        league.category === "basketball" ||
        league.category === "hockey")
    ) {
      return `${formatPeriodLabel(league, period)} · ${clock}`;
    }
    return type?.shortDetail || type?.detail || type?.description || "Live";
  }
  return (
    type?.shortDetail ||
    type?.detail ||
    type?.description ||
    state
  );
}

function extractScore(rawScore: RawEspnCompetitor["score"]): string | undefined {
  if (rawScore === undefined || rawScore === null) return undefined;
  if (typeof rawScore === "string") return rawScore.trim() || undefined;
  if (typeof rawScore === "number" && Number.isFinite(rawScore)) {
    return String(rawScore);
  }
  if (typeof rawScore === "object") {
    if (typeof rawScore.displayValue === "string" && rawScore.displayValue.trim()) {
      return rawScore.displayValue.trim();
    }
    if (typeof rawScore.value === "number" && Number.isFinite(rawScore.value)) {
      return String(rawScore.value);
    }
  }
  return undefined;
}

function extractRecord(records?: RawEspnRecord[]): string | undefined {
  if (!Array.isArray(records) || records.length === 0) return undefined;
  const overall = records.find(
    (r) =>
      r?.type === "total" ||
      r?.name === "overall" ||
      r?.name === "total",
  );
  return (overall?.summary || records[0]?.summary || "").trim() || undefined;
}

function formatVenue(venue?: RawEspnVenue): string | undefined {
  if (!venue?.fullName) return undefined;
  const parts = [venue.fullName.trim()];
  const city = venue.address?.city?.trim();
  const stateOrCountry =
    venue.address?.state?.trim() || venue.address?.country?.trim();
  if (city && stateOrCountry && city.toLowerCase() !== stateOrCountry.toLowerCase()) {
    parts.push(`${city}, ${stateOrCountry}`);
  } else if (city) {
    parts.push(city);
  }
  return parts.join(" · ");
}

function deriveAthleteAbbr(athlete?: RawEspnAthlete): string {
  if (athlete?.abbreviation?.trim()) return athlete.abbreviation.trim().toUpperCase();
  const full = athlete?.displayName || athlete?.fullName || "";
  const parts = full.trim().split(/\s+/);
  const last = parts[parts.length - 1] || full;
  return last
    .replace(/[^a-zA-Z0-9]/g, "")
    .slice(0, 3)
    .toUpperCase();
}

function mapTeamCompetitor(
  league: LeagueConfig,
  raw: RawEspnCompetitor,
  state: LiveEventState,
): { competitor: Competitor; hint: CompetitorHintInput } {
  const team = raw.team || {};
  const rawId = String(raw.id || team.id || "unknown");
  const entityId = formatEntityId(league.id, rawId);
  const name = team.displayName || team.name || team.shortDisplayName || "TBD";
  const short = team.shortDisplayName || team.name || name;
  const abbr = (team.abbreviation || short.slice(0, 3)).toUpperCase();
  const logo = team.logo || team.logos?.[0]?.href || undefined;
  const color = normalizeHexColor(team.color);
  const altColor = normalizeHexColor(team.alternateColor);
  const score = state === "pre" ? undefined : extractScore(raw.score);
  const record = extractRecord(raw.records);
  const rank =
    typeof raw.curatedRank?.current === "number" &&
    raw.curatedRank.current >= 1 &&
    raw.curatedRank.current <= 25
      ? raw.curatedRank.current
      : undefined;

  const competitor: Competitor = {
    id: entityId,
    name,
    short,
    abbr,
    home: raw.homeAway === "home",
    entityId,
  };
  if (logo) competitor.logo = logo;
  if (color) competitor.color = color;
  if (altColor) competitor.altColor = altColor;
  if (score !== undefined) competitor.score = score;
  if (record) competitor.record = record;
  if (rank !== undefined) competitor.rank = rank;
  if (raw.winner === true) competitor.winner = true;

  return {
    competitor,
    hint: {
      displayName: team.displayName,
      shortDisplayName: team.shortDisplayName,
      name: team.name,
      location: team.location,
      abbreviation: team.abbreviation,
    },
  };
}

function mapFighterCompetitor(
  league: LeagueConfig,
  raw: RawEspnCompetitor,
  index: number,
): Competitor {
  const ath = raw.athlete || {};
  const rawId = String(raw.id || ath.id || index);
  const entityId = formatEntityId(league.id, rawId);
  const name = ath.displayName || ath.fullName || "TBD";
  const short = ath.shortName || name;
  const abbr = deriveAthleteAbbr(ath);
  const record = extractRecord(raw.records);
  const comp: Competitor = {
    id: entityId,
    name,
    short,
    abbr,
    home: index === 0,
    entityId,
  };
  if (ath.flag?.href) comp.logo = ath.flag.href;
  if (record) comp.record = record;
  if (raw.winner === true) comp.winner = true;
  return comp;
}

function extractTeamSituation(
  league: LeagueConfig,
  comp: RawEspnCompetition,
  rawCompetitors: RawEspnCompetitor[],
): Record<string, string> | undefined {
  const sit = comp.situation;
  if (!sit) return undefined;
  const out: Record<string, string> = {};

  if (league.category === "football") {
    const down = sit.shortDownDistanceText || sit.downDistanceText;
    if (down) out["Down"] = down;
    if (sit.possessionText) out["Ball on"] = sit.possessionText;
    if (sit.possession) {
      const owner = rawCompetitors.find(
        (c) => String(c.id || c.team?.id) === String(sit.possession),
      );
      if (owner?.team?.abbreviation) {
        out["Possession"] = owner.team.abbreviation;
      }
    }
    if (sit.isRedZone) out["Zone"] = "Red Zone";
  } else if (league.category === "baseball") {
    if (typeof sit.outs === "number") out["Outs"] = String(sit.outs);
    if (typeof sit.balls === "number" && typeof sit.strikes === "number") {
      out["Count"] = `${sit.balls}-${sit.strikes}`;
    }
    const bases: string[] = [];
    if (sit.onFirst) bases.push("1st");
    if (sit.onSecond) bases.push("2nd");
    if (sit.onThird) bases.push("3rd");
    if (bases.length === 3) out["Bases"] = "Loaded";
    else if (bases.length > 0) out["Bases"] = bases.join(", ");
  } else if (sit.lastPlay?.text && sit.lastPlay.text.length <= 64) {
    out["Last play"] = sit.lastPlay.text;
  }

  return Object.keys(out).length > 0 ? out : undefined;
}

function extractScoreboardProbability(
  comp: RawEspnCompetition,
  competitors: Competitor[],
): Probability | undefined {
  const prob = comp.situation?.lastPlay?.probability;
  if (!prob || typeof prob.homeWinPercentage !== "number") return undefined;
  const home = competitors.find((c) => c.home) || competitors[0];
  if (!home) return undefined;
  const value = clamp01(prob.homeWinPercentage);
  return {
    label: `${home.abbr} win`,
    value,
    competitorId: home.id,
  };
}

function clamp01(n: number): number {
  if (!Number.isFinite(n)) return 0;
  if (n < 0) return 0;
  if (n > 1) return 1;
  return Math.round(n * 10000) / 10000;
}

function estimateGameProgress(
  league: LeagueConfig,
  status: LiveEventStatus,
  numericClock?: number,
): number {
  if (status.state === "post") return 1;
  if (status.state !== "in") return 0;
  const period = status.period ?? 1;
  if (league.category === "soccer") {
    const mins = parseElapsedSoccerMinutes(status.clock, numericClock) ?? 0;
    return clamp01(mins / 90);
  }
  const periodSec =
    league.category === "football"
      ? 900
      : league.category === "basketball"
        ? league.regulationPeriods === 2
          ? 1200
          : 720
        : league.category === "hockey"
          ? 1200
          : 0;
  if (periodSec > 0) {
    const totalSec = periodSec * league.regulationPeriods;
    if (period > league.regulationPeriods) return 0.99;
    const rem = parseRemainingClockSec(status.clock, numericClock) ?? periodSec;
    const elapsed = (period - 1) * periodSec + Math.max(0, periodSec - rem);
    return clamp01(elapsed / totalSec);
  }
  return clamp01(period / Math.max(1, league.regulationPeriods));
}

function selectActiveCompetition(
  league: LeagueConfig,
  event: RawEspnEvent,
): RawEspnCompetition | undefined {
  const comps = event.competitions || [];
  if (comps.length === 0) return undefined;
  if (league.id === "f1") {
    const live = comps.find((c) => mapEventState(c.status) === "in");
    if (live) return live;
    const race = comps.find(
      (c) => (c.type?.abbreviation || "").toLowerCase() === "race",
    );
    return race || comps[comps.length - 1];
  }
  if (league.id === "ufc") {
    const live = comps.find((c) => mapEventState(c.status) === "in");
    // ESPN orders UFC bouts prelims first, Main Event last.
    return live || comps[comps.length - 1];
  }
  return comps[0];
}

function buildLeagueRef(
  league: LeagueConfig,
  scoreboard?: RawEspnScoreboard,
): LeagueRef {
  const lgMeta = scoreboard?.leagues?.[0];
  return {
    id: league.id,
    name: league.name,
    abbr: league.abbr,
    logo: lgMeta?.logos?.[0]?.href || league.defaultLogo,
  };
}

function mapScoreboardEvent(
  league: LeagueConfig,
  leagueRef: LeagueRef,
  rawEvent: RawEspnEvent,
  region: string,
  nowMs: number,
): LiveEvent | null {
  if (!rawEvent.id) return null;
  const comp = selectActiveCompetition(league, rawEvent);
  const rawStatus = comp?.status || rawEvent.status;
  const state = mapEventState(rawStatus);
  const period =
    typeof rawStatus?.period === "number" && rawStatus.period > 0
      ? rawStatus.period
      : undefined;
  const clock =
    state === "in" && rawStatus?.displayClock?.trim()
      ? rawStatus.displayClock.trim()
      : undefined;

  const status: LiveEventStatus = {
    state,
    detail: formatStatusDetail(league, state, rawStatus),
  };
  if (period !== undefined) status.period = period;
  if (clock) status.clock = clock;

  const start = toRfc3339(comp?.startDate || comp?.date || rawEvent.date, nowMs);
  const updatedAt = new Date(nowMs).toISOString();
  const broadcasts = extractBroadcasts(
    comp?.geoBroadcasts,
    comp?.broadcasts,
    region,
  );

  const venue =
    formatVenue(comp?.venue) ||
    formatVenue(rawEvent.circuit) ||
    formatVenue(rawEvent.venues?.[0]);

  const rawCompetitors = comp?.competitors || [];
  let competitors: Competitor[] | undefined;
  let ranking: RankingRow[] | undefined;
  let rawHints: CompetitorHintInput[] | undefined;
  let situation: Record<string, string> | undefined;
  let probability: Probability | undefined;
  const extraTerms: string[] = [];

  if (league.mode === "team") {
    const mapped = rawCompetitors.map((c) => mapTeamCompetitor(league, c, state));
    // Consistent order: away first, home second.
    mapped.sort((a, b) => Number(Boolean(a.competitor.home)) - Number(Boolean(b.competitor.home)));
    competitors = mapped.map((m) => m.competitor);
    rawHints = mapped.map((m) => m.hint);

    if (
      isCloseGame({
        category: league.category,
        state,
        period,
        regulationPeriods: league.regulationPeriods,
        clock,
        numericClock: rawStatus?.clock,
        competitors,
      })
    ) {
      status.close = true;
    }

    if (comp) {
      situation = extractTeamSituation(league, comp, rawCompetitors);
      probability = extractScoreboardProbability(comp, competitors);
    }
  } else if (league.mode === "ranking") {
    const sorted = [...rawCompetitors].sort(
      (a, b) => (a.order ?? 999) - (b.order ?? 999),
    );
    ranking = sorted.slice(0, MAX_SCOREBOARD_RANKING).map((c, idx) => {
      const ath = c.athlete || {};
      const name = ath.displayName || ath.fullName || c.team?.displayName || "TBD";
      const abbr = deriveAthleteAbbr(ath);
      const pos = typeof c.order === "number" && c.order > 0 ? c.order : idx + 1;
      const scoreVal = extractScore(c.score);
      let detail: string | undefined;
      if (league.id === "pga") {
        const rounds = (c.linescores || [])
          .map((ls) => ls.displayValue)
          .filter((v): v is string => Boolean(v && v !== "-"));
        detail = scoreVal
          ? rounds.length > 0
            ? `${scoreVal} (${rounds.join("-")})`
            : scoreVal
          : undefined;
      } else if (league.id === "f1") {
        detail = c.winner ? "Winner" : ath.flag?.alt || `P${pos}`;
      } else {
        detail = scoreVal;
      }
      const row: RankingRow = {
        pos,
        name,
        abbr,
        entityId: formatEntityId(league.id, String(c.id || ath.id || pos)),
      };
      if (detail) row.detail = detail;
      return row;
    });

    const sit: Record<string, string> = {};
    if (league.id === "f1") {
      if (comp?.type?.abbreviation) sit["Session"] = comp.type.abbreviation;
      if (rawEvent.circuit?.fullName) sit["Circuit"] = rawEvent.circuit.fullName;
      extraTerms.push("Formula 1", "F1");
    } else if (league.id === "pga") {
      if (ranking[0]) {
        sit["Leader"] = ranking[0].detail
          ? `${ranking[0].name} (${ranking[0].detail.split(" ")[0]})`
          : ranking[0].name;
      }
      if (period) sit["Round"] = `Round ${period}`;
      extraTerms.push("PGA Tour", "PGA", "Golf");
    }
    if (rawEvent.name) extraTerms.push(rawEvent.name);
    if (rawEvent.shortName) extraTerms.push(rawEvent.shortName);
    if (Object.keys(sit).length > 0) situation = sit;
  } else if (league.mode === "card") {
    // UFC: highlight Main Event (or currently active bout) in competitors.
    competitors = rawCompetitors
      .slice(0, 2)
      .map((c, idx) => mapFighterCompetitor(league, c, idx));
    const sit: Record<string, string> = {};
    if (comp?.type?.abbreviation) sit["Main event"] = comp.type.abbreviation;
    if (rawEvent.competitions && rawEvent.competitions.length > 0) {
      sit["Bouts"] = String(rawEvent.competitions.length);
    }
    if (Object.keys(sit).length > 0) situation = sit;
    if (rawEvent.name) extraTerms.push(rawEvent.name);
    if (rawEvent.shortName) extraTerms.push(rawEvent.shortName);
    extraTerms.push("UFC", "MMA");
  }

  const matchHints = buildMatchHints(
    competitors,
    rawHints,
    ranking,
    broadcasts,
    extraTerms,
  );

  const event: LiveEvent = {
    id: formatEventId(league.id, rawEvent.id),
    feed: FEED_ID,
    category: league.category,
    league: leagueRef,
    title: rawEvent.shortName || rawEvent.name || undefined,
    start,
    status,
    updatedAt,
  };
  if (competitors && competitors.length > 0) event.competitors = competitors;
  if (ranking && ranking.length > 0) event.ranking = ranking;
  if (situation) event.situation = situation;
  if (probability) event.probability = probability;
  if (venue) event.venue = venue;
  if (broadcasts.length > 0) event.broadcasts = broadcasts;
  if (matchHints.terms.length > 0 || matchHints.callsigns.length > 0) {
    event.matchHints = matchHints;
  }
  return event;
}

function buildLineScoreTable(
  league: LeagueConfig,
  rawCompetitors: RawEspnCompetitor[],
  competitors: Competitor[],
): GenericTable | null {
  const byId = new Map<string, RawEspnCompetitor>();
  for (const rc of rawCompetitors) {
    byId.set(formatEntityId(league.id, String(rc.id || rc.team?.id || "")), rc);
  }
  let maxPeriods = 0;
  for (const c of competitors) {
    const rc = byId.get(c.id);
    const len = rc?.linescores?.length || 0;
    if (len > maxPeriods) maxPeriods = len;
  }
  if (maxPeriods === 0) return null;

  const columns: string[] = ["Team"];
  for (let p = 1; p <= maxPeriods; p++) {
    columns.push(
      league.category === "baseball"
        ? String(p)
        : formatPeriodLabel(league, p),
    );
  }
  columns.push(league.category === "baseball" ? "R" : "T");

  const rows: string[][] = competitors.map((c) => {
    const rc = byId.get(c.id);
    const ls = rc?.linescores || [];
    const vals: string[] = [c.abbr];
    for (let i = 0; i < maxPeriods; i++) {
      const entry = ls[i];
      vals.push(
        entry?.displayValue ??
          (typeof entry?.value === "number" ? String(entry.value) : "-"),
      );
    }
    vals.push(c.score ?? "-");
    return vals;
  });

  return { title: "Line score", columns, rows };
}

function buildSummaryTables(
  league: LeagueConfig,
  summary: RawEspnSummary,
  rawCompetitors: RawEspnCompetitor[],
  competitors: Competitor[],
): GenericTable[] {
  const tables: GenericTable[] = [];

  const lineScore = buildLineScoreTable(league, rawCompetitors, competitors);
  if (lineScore) tables.push(lineScore);

  // Team comparison stats table
  const bsTeams = summary.boxscore?.teams || [];
  if (bsTeams.length === 2) {
    const away =
      bsTeams.find((t) => t.homeAway === "away") || bsTeams[0];
    const home =
      bsTeams.find((t) => t.homeAway === "home") || bsTeams[1];
    const awayStats = away.statistics || [];
    const homeStats = home.statistics || [];
    const homeByName = new Map<string, string>();
    for (const s of homeStats) {
      if (s.name && s.displayValue !== undefined) {
        homeByName.set(s.name, s.displayValue);
      }
    }
    const rows: string[][] = [];
    for (const s of awayStats.slice(0, 12)) {
      if (!s.name || !s.label) continue;
      const hv = homeByName.get(s.name);
      if (hv === undefined) continue;
      rows.push([s.label, s.displayValue ?? "-", hv]);
    }
    if (rows.length > 0) {
      tables.push({
        title: "Team stats",
        columns: [
          "Stat",
          away.team?.abbreviation || competitors[0]?.abbr || "Away",
          home.team?.abbreviation || competitors[1]?.abbr || "Home",
        ],
        rows,
      });
    }
  }

  // Player box score tables
  const bsPlayers = summary.boxscore?.players || [];
  for (const teamGroup of bsPlayers) {
    const abbr = teamGroup.team?.abbreviation || teamGroup.team?.shortDisplayName || "Team";
    const statGroups = teamGroup.statistics || [];
    const selectedGroups =
      league.category === "football"
        ? statGroups.filter((g) =>
            ["passing", "rushing", "receiving"].includes((g.name || "").toLowerCase()),
          )
        : statGroups.slice(0, 2);

    for (const grp of selectedGroups) {
      const labels = (grp.labels || []).slice(0, 8);
      if (labels.length === 0) continue;
      const athletes = (grp.athletes || []).filter((a) => !a.didNotPlay).slice(0, 8);
      if (athletes.length === 0) continue;
      const rows: string[][] = athletes.map((a) => {
        const pname = a.athlete?.shortName || a.athlete?.displayName || "Player";
        const vals = (a.stats || []).slice(0, labels.length);
        while (vals.length < labels.length) vals.push("-");
        return [pname, ...vals];
      });
      const grpLabel = grp.text || grp.name || "Box score";
      const title =
        selectedGroups.length === 1 && league.category === "basketball"
          ? `${abbr} box score`
          : `${abbr} ${grpLabel.toLowerCase()}`;
      tables.push({
        title,
        columns: ["Player", ...labels],
        rows,
      });
    }
  }

  return tables;
}

const KEY_PLAY_RE =
  /touchdown|interception|fumble|sack|field goal|4th down|safety|red card|yellow card|penalty|var|dunk|three point|3pt|block|steal|power play|home run|triple|strikeout/i;

function classifyPlayEmphasis(play: RawEspnPlay): "score" | "key" | "normal" {
  if (play.scoringPlay === true) return "score";
  const combined = `${play.type?.text || ""} ${play.type?.type || ""} ${play.text || ""}`;
  if (play.isTurnover === true || KEY_PLAY_RE.test(combined)) return "key";
  return "normal";
}

function mapRawPlay(
  league: LeagueConfig,
  play: RawEspnPlay,
  fallbackTeamId?: string,
): EventPlay | null {
  const text = (play.text || play.shortText || "").trim();
  if (!text) return null;
  const out: EventPlay = {
    text,
    emphasis: classifyPlayEmphasis(play),
  };
  if (play.clock?.displayValue?.trim()) {
    out.clock = play.clock.displayValue.trim();
  }
  if (play.period?.number && play.period.number > 0) {
    out.period = formatPeriodLabel(league, play.period.number);
  } else if (play.period?.displayValue?.trim()) {
    out.period = play.period.displayValue.trim();
  }
  const rawTeam = play.team?.id || fallbackTeamId;
  if (rawTeam) {
    out.competitorId = formatEntityId(league.id, String(rawTeam));
  }
  return out;
}

function extractSummaryPlays(
  league: LeagueConfig,
  summary: RawEspnSummary,
): EventPlay[] {
  const out: EventPlay[] = [];

  if (league.category === "soccer" && Array.isArray(summary.keyEvents)) {
    for (const ke of summary.keyEvents) {
      const mapped = mapRawPlay(league, ke);
      if (mapped) out.push(mapped);
    }
  } else if (summary.drives) {
    const allDrives: RawEspnDrive[] = [
      ...(summary.drives.previous || []),
      ...(summary.drives.current ? [summary.drives.current] : []),
    ];
    for (const drive of allDrives) {
      for (const p of drive.plays || []) {
        const mapped = mapRawPlay(league, p, drive.team?.id);
        if (mapped) out.push(mapped);
      }
    }
    if (out.length === 0 && Array.isArray(summary.scoringPlays)) {
      for (const sp of summary.scoringPlays) {
        const mapped = mapRawPlay(league, { ...sp, scoringPlay: true });
        if (mapped) out.push(mapped);
      }
    }
  } else if (Array.isArray(summary.plays)) {
    for (const p of summary.plays) {
      const mapped = mapRawPlay(league, p);
      if (mapped) out.push(mapped);
    }
  }

  return out.length > MAX_PLAYS ? out.slice(out.length - MAX_PLAYS) : out;
}

function extractWinProbability(
  league: LeagueConfig,
  summary: RawEspnSummary,
  competitors: Competitor[],
  status: LiveEventStatus,
  numericClock?: number,
): { probability?: Probability; series?: ProbabilityPoint[] } {
  const raw = summary.winprobability;
  if (!Array.isArray(raw) || raw.length === 0) return {};

  const valid = raw.filter(
    (p): p is { homeWinPercentage: number } =>
      typeof p?.homeWinPercentage === "number" &&
      Number.isFinite(p.homeWinPercentage),
  );
  if (valid.length === 0) return {};

  // Downsample cleanly while preserving start and end points.
  let sampled = valid;
  if (valid.length > MAX_PROBABILITY_POINTS) {
    sampled = [];
    const step = (valid.length - 1) / (MAX_PROBABILITY_POINTS - 1);
    for (let i = 0; i < MAX_PROBABILITY_POINTS; i++) {
      sampled.push(valid[Math.round(i * step)]);
    }
  }

  const maxT =
    status.state === "post"
      ? 1
      : Math.max(0.05, estimateGameProgress(league, status, numericClock));
  const denom = Math.max(1, sampled.length - 1);
  const series: ProbabilityPoint[] = sampled.map((pt, idx) => ({
    t: sampled.length === 1 ? clamp01(maxT) : clamp01((idx / denom) * maxT),
    value: clamp01(pt.homeWinPercentage),
  }));

  const home = competitors.find((c) => c.home) || competitors[0];
  const lastVal = series[series.length - 1].value;
  const probability: Probability | undefined = home
    ? {
        label: `${home.abbr} win`,
        value: lastVal,
        competitorId: home.id,
      }
    : undefined;

  return { probability, series };
}

function extractLinks(rawLinks?: RawEspnLink[]): EventLink[] | undefined {
  if (!Array.isArray(rawLinks)) return undefined;
  const out: EventLink[] = [];
  const seen = new Set<string>();
  for (const ln of rawLinks) {
    const url = ln?.href?.trim();
    const label = (ln?.shortText || ln?.text || "").trim();
    if (!url || !label || !/^https?:\/\//i.test(url) || seen.has(url)) continue;
    seen.add(url);
    out.push({ label, url });
    if (out.length >= 4) break;
  }
  return out.length > 0 ? out : undefined;
}

function buildFallbackEventDetailTables(
  league: LeagueConfig,
  rawEvent: RawEspnEvent,
): GenericTable[] {
  const tables: GenericTable[] = [];
  const comps = rawEvent.competitions || [];

  if (league.id === "f1") {
    const active = selectActiveCompetition(league, rawEvent);
    if (active?.competitors?.length) {
      const sorted = [...active.competitors].sort(
        (a, b) => (a.order ?? 999) - (b.order ?? 999),
      );
      const sessionName = active.type?.abbreviation || "Classification";
      tables.push({
        title: `${sessionName} results`,
        columns: ["Pos", "Driver", "Code", "Country"],
        rows: sorted.map((c, idx) => [
          String(c.order || idx + 1),
          c.athlete?.displayName || c.athlete?.fullName || "TBD",
          deriveAthleteAbbr(c.athlete),
          c.athlete?.flag?.alt || "-",
        ]),
      });
    }
    if (comps.length > 1) {
      tables.push({
        title: "Weekend sessions",
        columns: ["Session", "Status", "Leader"],
        rows: comps.map((c) => {
          const st = mapEventState(c.status);
          const first = [...(c.competitors || [])].sort(
            (a, b) => (a.order ?? 999) - (b.order ?? 999),
          )[0];
          return [
            c.type?.abbreviation || c.type?.text || "Session",
            formatStatusDetail(league, st, c.status),
            first?.athlete?.shortName || first?.athlete?.displayName || "-",
          ];
        }),
      });
    }
  } else if (league.id === "pga") {
    const active = comps[0];
    if (active?.competitors?.length) {
      const sorted = [...active.competitors].sort(
        (a, b) => (a.order ?? 999) - (b.order ?? 999),
      );
      const maxRounds = Math.max(
        1,
        ...sorted.slice(0, 20).map((c) => c.linescores?.length || 0),
      );
      const columns = ["Pos", "Player", "Score"];
      for (let r = 1; r <= maxRounds; r++) columns.push(`R${r}`);
      tables.push({
        title: "Leaderboard",
        columns,
        rows: sorted.slice(0, 25).map((c, idx) => {
          const row = [
            String(c.order || idx + 1),
            c.athlete?.displayName || c.athlete?.fullName || "TBD",
            extractScore(c.score) || "-",
          ];
          for (let r = 0; r < maxRounds; r++) {
            row.push(c.linescores?.[r]?.displayValue || "-");
          }
          return row;
        }),
      });
    }
  } else if (league.id === "ufc") {
    if (comps.length > 0) {
      // Main event first.
      const reversed = [...comps].reverse();
      tables.push({
        title: "Fight card",
        columns: ["Bout", "Division", "Status"],
        rows: reversed.map((c) => {
          const f0 = c.competitors?.[0];
          const f1 = c.competitors?.[1];
          const n0 = f0?.athlete?.displayName || "TBD";
          const n1 = f1?.athlete?.displayName || "TBD";
          const bout = f0?.winner
            ? `${n0} def. ${n1}`
            : f1?.winner
              ? `${n1} def. ${n0}`
              : `${n0} vs ${n1}`;
          const st = mapEventState(c.status);
          return [
            bout,
            c.type?.abbreviation || "Bout",
            formatStatusDetail(league, st, c.status),
          ];
        }),
      });
    }
  }

  return tables;
}

function statLookup(
  stats: RawEspnStandingStat[] | undefined,
  names: string[],
): string {
  if (!Array.isArray(stats)) return "-";
  for (const name of names) {
    const found = stats.find((s) => s.name === name);
    if (found) {
      if (found.displayValue !== undefined && found.displayValue.trim() !== "") {
        return found.displayValue.trim();
      }
      if (typeof found.value === "number" && Number.isFinite(found.value)) {
        return String(found.value);
      }
    }
  }
  return "-";
}

function statNumeric(
  stats: RawEspnStandingStat[] | undefined,
  names: string[],
): number | null {
  if (!Array.isArray(stats)) return null;
  for (const name of names) {
    const found = stats.find((s) => s.name === name);
    if (found) {
      if (typeof found.value === "number" && Number.isFinite(found.value)) {
        return found.value;
      }
      if (found.displayValue) {
        const n = Number(found.displayValue);
        if (Number.isFinite(n)) return n;
      }
    }
  }
  return null;
}

function sortStandingsEntries(
  league: LeagueConfig,
  entries: RawEspnStandingEntry[],
): RawEspnStandingEntry[] {
  return [...entries].sort((a, b) => {
    const ra = statNumeric(a.stats, ["playoffSeed", "rank"]);
    const rb = statNumeric(b.stats, ["playoffSeed", "rank"]);
    if (ra !== null && rb !== null && ra !== rb) return ra - rb;
    if (ra !== null && rb === null) return -1;
    if (ra === null && rb !== null) return 1;

    // Secondary sort: points (soccer/NHL/F1) or winPercent (NFL/NBA/MLB).
    const pa = statNumeric(a.stats, ["points", "championshipPts", "winPercent"]);
    const pb = statNumeric(b.stats, ["points", "championshipPts", "winPercent"]);
    if (pa !== null && pb !== null && pa !== pb) return pb - pa;

    const na = a.team?.displayName || a.athlete?.displayName || "";
    const nb = b.team?.displayName || b.athlete?.displayName || "";
    return na.localeCompare(nb);
  });
}

function mapStandingsTable(
  league: LeagueConfig,
  title: string,
  rawEntries: RawEspnStandingEntry[],
): GenericTable | null {
  if (rawEntries.length === 0) return null;
  const entries = sortStandingsEntries(league, rawEntries);

  if (league.id === "f1") {
    const isDrivers = entries.some((e) => Boolean(e.athlete));
    if (isDrivers) {
      return {
        title,
        columns: ["Pos", "Driver", "PTS"],
        rows: entries.map((e, idx) => [
          statLookup(e.stats, ["rank"]) !== "-"
            ? statLookup(e.stats, ["rank"])
            : String(idx + 1),
          e.athlete?.displayName || e.athlete?.shortName || "TBD",
          statLookup(e.stats, ["championshipPts", "points"]),
        ]),
      };
    }
    return {
      title,
      columns: ["Pos", "Constructor", "PTS"],
      rows: entries.map((e, idx) => [
        statLookup(e.stats, ["rank"]) !== "-"
          ? statLookup(e.stats, ["rank"])
          : String(idx + 1),
        e.team?.displayName || e.team?.name || "TBD",
        statLookup(e.stats, ["points", "championshipPts"]),
      ]),
    };
  }

  switch (league.category) {
    case "football":
      return {
        title,
        columns: ["#", "Team", "W", "L", "T", "PCT", "PF", "PA", "DIFF", "STRK"],
        rows: entries.map((e, idx) => [
          statLookup(e.stats, ["playoffSeed", "rank"]) !== "-"
            ? statLookup(e.stats, ["playoffSeed", "rank"])
            : String(idx + 1),
          e.team?.displayName || e.team?.shortDisplayName || "TBD",
          statLookup(e.stats, ["wins"]),
          statLookup(e.stats, ["losses"]),
          statLookup(e.stats, ["ties"]),
          statLookup(e.stats, ["winPercent"]),
          statLookup(e.stats, ["pointsFor"]),
          statLookup(e.stats, ["pointsAgainst"]),
          statLookup(e.stats, ["pointDifferential", "differential"]),
          statLookup(e.stats, ["streak"]),
        ]),
      };

    case "basketball":
    case "baseball":
      return {
        title,
        columns: ["#", "Team", "W", "L", "PCT", "GB", "DIFF", "STRK"],
        rows: entries.map((e, idx) => [
          statLookup(e.stats, ["playoffSeed", "rank"]) !== "-"
            ? statLookup(e.stats, ["playoffSeed", "rank"])
            : String(idx + 1),
          e.team?.displayName || e.team?.shortDisplayName || "TBD",
          statLookup(e.stats, ["wins"]),
          statLookup(e.stats, ["losses"]),
          statLookup(e.stats, ["winPercent"]),
          statLookup(e.stats, ["gamesBehind"]),
          statLookup(e.stats, ["differential", "pointDifferential"]),
          statLookup(e.stats, ["streak"]),
        ]),
      };

    case "hockey":
      return {
        title,
        columns: ["#", "Team", "GP", "W", "L", "OTL", "PTS", "GF", "GA", "DIFF", "STRK"],
        rows: entries.map((e, idx) => [
          statLookup(e.stats, ["playoffSeed", "rank"]) !== "-"
            ? statLookup(e.stats, ["playoffSeed", "rank"])
            : String(idx + 1),
          e.team?.displayName || e.team?.shortDisplayName || "TBD",
          statLookup(e.stats, ["gamesPlayed"]),
          statLookup(e.stats, ["wins"]),
          statLookup(e.stats, ["losses"]),
          statLookup(e.stats, ["otLosses"]),
          statLookup(e.stats, ["points"]),
          statLookup(e.stats, ["pointsFor"]),
          statLookup(e.stats, ["pointsAgainst"]),
          statLookup(e.stats, ["pointDifferential", "differential"]),
          statLookup(e.stats, ["streak"]),
        ]),
      };

    case "soccer":
    default:
      return {
        title,
        columns: ["#", "Team", "GP", "W", "D", "L", "GF", "GA", "GD", "PTS"],
        rows: entries.map((e, idx) => [
          statLookup(e.stats, ["rank"]) !== "-"
            ? statLookup(e.stats, ["rank"])
            : String(idx + 1),
          e.team?.displayName || e.team?.shortDisplayName || "TBD",
          statLookup(e.stats, ["gamesPlayed"]),
          statLookup(e.stats, ["wins"]),
          statLookup(e.stats, ["ties"]),
          statLookup(e.stats, ["losses"]),
          statLookup(e.stats, ["pointsFor"]),
          statLookup(e.stats, ["pointsAgainst"]),
          statLookup(e.stats, ["pointDifferential"]),
          statLookup(e.stats, ["points"]),
        ]),
      };
  }
}

function collectStandingsTables(
  league: LeagueConfig,
  node: RawEspnStandingsNode,
  prefix: string,
  out: GenericTable[],
): void {
  const label = node.name?.trim() || "";
  const entries = node.standings?.entries;
  if (Array.isArray(entries) && entries.length > 0) {
    const title = label
      ? prefix && !label.toLowerCase().includes(prefix.toLowerCase())
        ? `${prefix} · ${label}`
        : label
      : prefix || league.name;
    const tbl = mapStandingsTable(league, title, entries);
    if (tbl) out.push(tbl);
  }
  if (Array.isArray(node.children)) {
    for (const child of node.children) {
      collectStandingsTables(league, child, label || prefix, out);
    }
  }
}

function sortEvents(events: LiveEvent[]): LiveEvent[] {
  const rank = (ev: LiveEvent): number => {
    if (ev.status.state === "in") return ev.status.close ? 0 : 1;
    if (ev.status.state === "delayed") return 2;
    if (ev.status.state === "pre") return 3;
    return 4;
  };
  return [...events].sort((a, b) => {
    const ra = rank(a);
    const rb = rank(b);
    if (ra !== rb) return ra - rb;
    const ta = Date.parse(a.start) || 0;
    const tb = Date.parse(b.start) || 0;
    if (ra === 4) {
      // Completed games: most recent first.
      if (ta !== tb) return tb - ta;
    } else {
      if (ta !== tb) return ta - tb;
    }
    return a.id.localeCompare(b.id);
  });
}

function scoreboardUrl(league: LeagueConfig, dateKey: string): string {
  const base = `${ESPN_SITE_API}/${league.espnSport}/${league.espnLeague}/scoreboard`;
  return dateKey === "current" ? base : `${base}?dates=${encodeURIComponent(dateKey)}`;
}

function summaryUrl(league: LeagueConfig, rawEventId: string): string {
  return `${ESPN_SITE_API}/${league.espnSport}/${league.espnLeague}/summary?event=${encodeURIComponent(rawEventId)}`;
}

function teamsUrl(league: LeagueConfig): string {
  return `${ESPN_SITE_API}/${league.espnSport}/${league.espnLeague}/teams?limit=500`;
}

function standingsUrl(league: LeagueConfig): string {
  return `${ESPN_WEB_API}/${league.espnSport}/${league.espnLeague}/standings`;
}

// --- Provider implementation ---

export class EspnSportsProvider implements SportsProvider {
  readonly id = "espn";
  readonly name = "ESPN";

  getEvents(query: ScoreboardQuery, rt: ProviderRuntime): ScoreboardResult {
    const nowMs = rt.now();
    const fromMs = parseDateParam(query.from);
    const toMs = parseDateParam(query.to);

    // If specific event IDs are requested, narrow leagues to only those referenced.
    let targetLeagues = query.leagues;
    const wantedIds = query.ids && query.ids.length > 0 ? new Set(query.ids) : null;
    if (wantedIds) {
      const byId = new Map<string, LeagueConfig>();
      for (const id of wantedIds) {
        const parsed = parseEventId(id);
        if (parsed) byId.set(parsed.league.id, parsed.league);
      }
      if (byId.size > 0) {
        targetLeagues = [...byId.values()];
      }
    }

    interface FetchSlot {
      league: LeagueConfig;
      dateKey: string;
      cacheKey: string;
      stale: LiveEvent[] | null;
      req: HostFetchRequest;
    }

    const collected = new Map<string, LiveEvent>();
    const toFetch: FetchSlot[] = [];

    for (const league of targetLeagues) {
      const dateKeys = enumerateDateKeys(fromMs, toMs, league.multiDayEvent);
      for (const dateKey of dateKeys) {
        const cacheKey = `sb:${league.id}:${dateKey}:${query.region}`;
        const cached = rt.cache.read<LiveEvent[]>(cacheKey);
        if (cached?.fresh) {
          for (const ev of cached.value) {
            collected.set(ev.id, ev);
          }
          continue;
        }
        toFetch.push({
          league,
          dateKey,
          cacheKey,
          stale: cached ? cached.value : null,
          req: { method: "GET", url: scoreboardUrl(league, dateKey) },
        });
      }
    }

    if (toFetch.length > 0) {
      const responses = fetchManyBounded(
        rt.http,
        toFetch.map((s) => s.req),
      );
      for (let i = 0; i < toFetch.length; i++) {
        const slot = toFetch[i];
        const res = responses[i];
        if (!res || "error" in res || res.status !== 200) {
          rt.log?.("warn", "ESPN scoreboard fetch failed", {
            league: slot.league.id,
            dateKey: slot.dateKey,
            status: res && !("error" in res) ? res.status : undefined,
            error: res && "error" in res ? res.error.message : undefined,
          });
          if (slot.stale) {
            for (const ev of slot.stale) collected.set(ev.id, ev);
          }
          continue;
        }
        try {
          const parsed = JSON.parse(res.body) as RawEspnScoreboard;
          const leagueRef = buildLeagueRef(slot.league, parsed);
          const mapped: LiveEvent[] = [];
          for (const rawEv of parsed.events || []) {
            const ev = mapScoreboardEvent(
              slot.league,
              leagueRef,
              rawEv,
              query.region,
              nowMs,
            );
            if (ev) mapped.push(ev);
          }
          const ttlMs = computeScoreboardTtlMs(mapped, nowMs);
          rt.cache.write(slot.cacheKey, mapped, ttlMs);
          for (const ev of mapped) {
            collected.set(ev.id, ev);
          }
        } catch (err) {
          rt.log?.("warn", "ESPN scoreboard parse failed", {
            league: slot.league.id,
            error: err instanceof Error ? err.message : String(err),
          });
          if (slot.stale) {
            for (const ev of slot.stale) collected.set(ev.id, ev);
          }
        }
      }
    }

    let events = [...collected.values()];

    // Filter by requested time window (keeping live/delayed games overlapping window).
    if (fromMs !== null || toMs !== null) {
      const winStart = fromMs !== null ? fromMs - 4 * 3_600_000 : -Infinity;
      const winEnd = toMs !== null ? toMs + 6 * 3_600_000 : Infinity;
      events = events.filter((ev) => {
        if (isLiveState(ev.status.state)) return true;
        const startMs = Date.parse(ev.start);
        if (Number.isNaN(startMs)) return true;
        return startMs >= winStart && startMs <= winEnd;
      });
    }

    if (wantedIds) {
      const missing: string[] = [];
      for (const id of wantedIds) {
        if (!collected.has(id) && missing.length < 6) {
          missing.push(id);
        }
      }
      for (const id of missing) {
        const parsed = parseEventId(id);
        if (!parsed) continue;
        const detail = this.getEventDetail(
          parsed.league,
          parsed.rawEventId,
          query.region,
          rt,
        );
        if (detail) {
          const {
            tables: _t,
            plays: _p,
            probabilitySeries: _ps,
            links: _l,
            ...summaryEvent
          } = detail;
          collected.set(summaryEvent.id, summaryEvent);
        }
      }
      events = [...collected.values()].filter((ev) => wantedIds.has(ev.id));
    }

    const sorted = sortEvents(events);
    return {
      events: sorted,
      nextPollSec: computeNextPollSec(sorted, nowMs),
    };
  }

  getEventDetail(
    league: LeagueConfig,
    rawEventId: string,
    region: string,
    rt: ProviderRuntime,
  ): LiveEventDetail | null {
    const nowMs = rt.now();
    const cacheKey = `ev:${league.id}:${rawEventId}:${region}`;
    const cached = rt.cache.read<LiveEventDetail>(cacheKey);
    if (cached?.fresh) {
      return cached.value;
    }

    // F1, PGA, and UFC return HTTP 404 on ESPN /summary; build detail from scoreboard.
    if (!league.supportsSummary) {
      return this.getScoreboardFallbackDetail(
        league,
        rawEventId,
        region,
        cacheKey,
        cached?.value || null,
        rt,
      );
    }

    const [res] = fetchManyBounded(rt.http, [
      { method: "GET", url: summaryUrl(league, rawEventId) },
    ]);
    if (!res || "error" in res || res.status !== 200) {
      if (cached) return cached.value;
      // Fall back to scoreboard if summary endpoint is unavailable.
      return this.getScoreboardFallbackDetail(
        league,
        rawEventId,
        region,
        cacheKey,
        null,
        rt,
      );
    }

    try {
      const summary = JSON.parse(res.body) as RawEspnSummary;
      const comp = summary.header?.competitions?.[0];
      if (!comp) {
        return cached?.value || null;
      }
      const rawStatus = comp.status;
      const state = mapEventState(rawStatus);
      const period =
        typeof rawStatus?.period === "number" && rawStatus.period > 0
          ? rawStatus.period
          : undefined;
      const clock =
        state === "in" && rawStatus?.displayClock?.trim()
          ? rawStatus.displayClock.trim()
          : undefined;

      const status: LiveEventStatus = {
        state,
        detail: formatStatusDetail(league, state, rawStatus),
      };
      if (period !== undefined) status.period = period;
      if (clock) status.clock = clock;

      const rawCompetitors = comp.competitors || [];
      const mapped = rawCompetitors.map((c) => mapTeamCompetitor(league, c, state));
      mapped.sort(
        (a, b) => Number(Boolean(a.competitor.home)) - Number(Boolean(b.competitor.home)),
      );
      const competitors = mapped.map((m) => m.competitor);
      const rawHints = mapped.map((m) => m.hint);

      if (
        isCloseGame({
          category: league.category,
          state,
          period,
          regulationPeriods: league.regulationPeriods,
          clock,
          numericClock: rawStatus?.clock,
          competitors,
        })
      ) {
        status.close = true;
      }

      const broadcasts = extractBroadcasts(
        comp.geoBroadcasts,
        [...(comp.broadcasts || []), ...(summary.broadcasts || [])],
        region,
      );
      const matchHints = buildMatchHints(competitors, rawHints, undefined, broadcasts);
      const venue = formatVenue(summary.gameInfo?.venue || comp.venue);
      const situation = extractTeamSituation(league, comp, rawCompetitors);
      const { probability, series } = extractWinProbability(
        league,
        summary,
        competitors,
        status,
        rawStatus?.clock,
      );
      const tables = buildSummaryTables(league, summary, rawCompetitors, competitors);
      const plays = extractSummaryPlays(league, summary);
      const links = extractLinks(summary.header?.links);

      const away = competitors[0];
      const home = competitors[1];
      const title =
        away && home ? `${away.abbr} @ ${home.abbr}` : undefined;

      const detail: LiveEventDetail = {
        id: formatEventId(league.id, rawEventId),
        feed: FEED_ID,
        category: league.category,
        league: buildLeagueRef(league),
        title,
        start: toRfc3339(comp.date || comp.startDate, nowMs),
        status,
        competitors,
        updatedAt: new Date(nowMs).toISOString(),
      };
      if (situation) detail.situation = situation;
      if (probability) detail.probability = probability;
      if (venue) detail.venue = venue;
      if (broadcasts.length > 0) detail.broadcasts = broadcasts;
      if (matchHints.terms.length > 0 || matchHints.callsigns.length > 0) {
        detail.matchHints = matchHints;
      }
      if (tables.length > 0) detail.tables = tables;
      if (plays.length > 0) detail.plays = plays;
      if (series && series.length > 0) detail.probabilitySeries = series;
      if (links && links.length > 0) detail.links = links;

      const ttlMs = isLiveState(state) ? TTL_LIVE_MS : TTL_IDLE_MS;
      rt.cache.write(cacheKey, detail, ttlMs);
      return detail;
    } catch (err) {
      rt.log?.("warn", "ESPN summary parse failed", {
        league: league.id,
        event: rawEventId,
        error: err instanceof Error ? err.message : String(err),
      });
      return cached?.value || null;
    }
  }

  private getScoreboardFallbackDetail(
    league: LeagueConfig,
    rawEventId: string,
    region: string,
    cacheKey: string,
    stale: LiveEventDetail | null,
    rt: ProviderRuntime,
  ): LiveEventDetail | null {
    const nowMs = rt.now();
    const [res] = fetchManyBounded(rt.http, [
      { method: "GET", url: scoreboardUrl(league, "current") },
    ]);
    if (!res || "error" in res || res.status !== 200) {
      return stale;
    }
    try {
      const parsed = JSON.parse(res.body) as RawEspnScoreboard;
      const rawEv = (parsed.events || []).find((e) => String(e.id) === rawEventId);
      if (!rawEv) return stale;
      const leagueRef = buildLeagueRef(league, parsed);
      const base = mapScoreboardEvent(league, leagueRef, rawEv, region, nowMs);
      if (!base) return stale;

      const tables = buildFallbackEventDetailTables(league, rawEv);
      const links = extractLinks(rawEv.links);
      const detail: LiveEventDetail = { ...base };
      if (tables.length > 0) detail.tables = tables;
      if (links && links.length > 0) detail.links = links;

      const ttlMs = isLiveState(base.status.state) ? TTL_LIVE_MS : TTL_IDLE_MS;
      rt.cache.write(cacheKey, detail, ttlMs);
      return detail;
    } catch {
      return stale;
    }
  }

  getEntities(query: EntitiesQuery, rt: ProviderRuntime): LiveEntity[] {
    if (query.kind !== "team") {
      return [];
    }

    interface Slot {
      league: LeagueConfig;
      cacheKey: string;
      stale: LiveEntity[] | null;
      req: HostFetchRequest;
    }

    const entities: LiveEntity[] = [];
    const toFetch: Slot[] = [];

    for (const league of query.leagues) {
      if (league.teamsSource === "none") continue;
      const cacheKey = `teams:${league.id}`;
      const cached = rt.cache.read<LiveEntity[]>(cacheKey);
      if (cached?.fresh) {
        entities.push(...cached.value);
        continue;
      }
      const url =
        league.teamsSource === "f1_constructors"
          ? standingsUrl(league)
          : teamsUrl(league);
      toFetch.push({
        league,
        cacheKey,
        stale: cached ? cached.value : null,
        req: { method: "GET", url },
      });
    }

    if (toFetch.length > 0) {
      const responses = fetchManyBounded(
        rt.http,
        toFetch.map((s) => s.req),
      );
      for (let i = 0; i < toFetch.length; i++) {
        const slot = toFetch[i];
        const res = responses[i];
        if (!res || "error" in res || res.status !== 200) {
          if (slot.stale) entities.push(...slot.stale);
          continue;
        }
        try {
          const parsedEntities =
            slot.league.teamsSource === "f1_constructors"
              ? parseF1ConstructorEntities(
                  slot.league,
                  JSON.parse(res.body) as RawEspnStandingsNode,
                )
              : parseEspnTeamEntities(
                  slot.league,
                  JSON.parse(res.body) as RawEspnTeamsResponse,
                );
          rt.cache.write(slot.cacheKey, parsedEntities, TTL_TEAMS_MS);
          entities.push(...parsedEntities);
        } catch {
          if (slot.stale) entities.push(...slot.stale);
        }
      }
    }

    let filtered = entities;
    const needle = query.q?.trim().toLowerCase();
    if (needle) {
      filtered = filtered.filter(
        (e) =>
          e.name.toLowerCase().includes(needle) ||
          (e.abbr && e.abbr.toLowerCase().includes(needle)) ||
          (e.league && e.league.toLowerCase().includes(needle)),
      );
    }

    filtered.sort((a, b) => {
      const la = a.league || "";
      const lb = b.league || "";
      if (la !== lb) return la.localeCompare(lb);
      return a.name.localeCompare(b.name);
    });
    return filtered;
  }

  getStandings(query: StandingsQuery, rt: ProviderRuntime): GenericTable[] {
    interface Slot {
      league: LeagueConfig;
      cacheKey: string;
      stale: GenericTable[] | null;
      req: HostFetchRequest;
    }

    const tables: GenericTable[] = [];
    const toFetch: Slot[] = [];

    for (const league of query.leagues) {
      if (!league.supportsStandings) continue;
      const cacheKey = `standings:${league.id}`;
      const cached = rt.cache.read<GenericTable[]>(cacheKey);
      if (cached?.fresh) {
        tables.push(...cached.value);
        continue;
      }
      toFetch.push({
        league,
        cacheKey,
        stale: cached ? cached.value : null,
        req: { method: "GET", url: standingsUrl(league) },
      });
    }

    if (toFetch.length > 0) {
      const responses = fetchManyBounded(
        rt.http,
        toFetch.map((s) => s.req),
      );
      for (let i = 0; i < toFetch.length; i++) {
        const slot = toFetch[i];
        const res = responses[i];
        if (!res || "error" in res || res.status !== 200) {
          if (slot.stale) tables.push(...slot.stale);
          continue;
        }
        try {
          const root = JSON.parse(res.body) as RawEspnStandingsNode;
          const leagueTables: GenericTable[] = [];
          collectStandingsTables(slot.league, root, "", leagueTables);
          rt.cache.write(slot.cacheKey, leagueTables, TTL_STANDINGS_MS);
          tables.push(...leagueTables);
        } catch {
          if (slot.stale) tables.push(...slot.stale);
        }
      }
    }

    return tables;
  }

  healthCheck(
    league: LeagueConfig,
    rt: ProviderRuntime,
  ): { ok: boolean; detail: string } {
    const [res] = fetchManyBounded(rt.http, [
      { method: "GET", url: scoreboardUrl(league, "current") },
    ]);
    if (!res) {
      return { ok: false, detail: "No response from ESPN" };
    }
    if ("error" in res) {
      return { ok: false, detail: res.error.message };
    }
    if (res.status !== 200) {
      return { ok: false, detail: `ESPN returned HTTP ${res.status}` };
    }
    try {
      const data = JSON.parse(res.body) as RawEspnScoreboard;
      const count = Array.isArray(data.events) ? data.events.length : 0;
      return {
        ok: true,
        detail: `Connected to ESPN (${league.abbr}: ${count} events on slate)`,
      };
    } catch {
      return { ok: false, detail: "ESPN returned invalid JSON" };
    }
  }
}

function parseEspnTeamEntities(
  league: LeagueConfig,
  raw: RawEspnTeamsResponse,
): LiveEntity[] {
  const out: LiveEntity[] = [];
  const teams = raw.sports?.[0]?.leagues?.[0]?.teams || [];
  for (const wrapper of teams) {
    const t = wrapper?.team;
    if (!t?.id) continue;
    const entity: LiveEntity = {
      id: formatEntityId(league.id, String(t.id)),
      kind: "team",
      name: t.displayName || t.name || t.shortDisplayName || "Team",
      league: league.id,
    };
    if (t.abbreviation) entity.abbr = t.abbreviation.toUpperCase();
    const logo = t.logo || t.logos?.[0]?.href;
    if (logo) entity.logo = logo;
    const color = normalizeHexColor(t.color);
    if (color) entity.color = color;
    out.push(entity);
  }
  return out;
}

function parseF1ConstructorEntities(
  league: LeagueConfig,
  root: RawEspnStandingsNode,
): LiveEntity[] {
  const out: LiveEntity[] = [];
  const seen = new Set<string>();
  const walk = (node: RawEspnStandingsNode) => {
    for (const entry of node.standings?.entries || []) {
      const t = entry.team;
      if (!t?.id || seen.has(t.id)) continue;
      seen.add(t.id);
      const entity: LiveEntity = {
        id: formatEntityId(league.id, String(t.id)),
        kind: "team",
        name: t.displayName || t.name || "Constructor",
        league: league.id,
      };
      if (t.abbreviation) entity.abbr = t.abbreviation.toUpperCase();
      const color = normalizeHexColor(t.color);
      if (color) entity.color = color;
      out.push(entity);
    }
    for (const child of node.children || []) walk(child);
  };
  walk(root);
  return out;
}
