# Sports (`org.ether.sports`)

A first-party hosted TypeScript plugin for Ether that brings live scores, standings, schedules, win probability, play-by-play, and team favourites across 17 leagues using ESPN's public JSON endpoints behind a swappable `SportsProvider` interface.

## What it contributes

- **Live feed (`sports`):** `live_events` capability with a 15 s minimum interval and `team` entity kind. The plugin never polls on its own; Ether calls it on demand while a client is subscribed or an alert window is open.
- **Sports screen (`sports`):** `leagues`, `hero`, `Live now`, `Your teams`, `Today`, `Schedule`, `Standings`, and `Teams` sections matching Ether's generic screen contract.
- **Live TV matching:** `broadcasts` (normalized TV / stream / radio with region preference) and `matchHints` (team names, short names, nicknames, locations, abbreviations, and expanded broadcast callsigns) for Ether's "Where to watch" and score-bug overlay matcher.
- **Close-game alerts (`status.close`):**
  - Football (NFL, NCAAF): within 8 points in Q4 under 5 min (or OT within 8).
  - Basketball (NBA, WNBA, NCAAM): within 5 points in the last 3 min of regulation (Q4 for 4-quarter leagues, 2nd half for NCAAM) or OT.
  - Hockey (NHL): tied or within 1 goal in the 3rd period (or OT/SO).
  - Soccer (MLS, EPL, La Liga, Bundesliga, Serie A, Ligue 1, UCL): level at or after 80' (or level in extra time).
  - Baseball (MLB): within 2 runs in the 9th inning or extra innings.

## Default leagues

Enabled by default and selectable in plugin settings (`leagues`):

NFL, NBA, NHL, MLB, MLS, Premier League (`epl`), La Liga (`laliga`), Bundesliga (`bundesliga`), Serie A (`seriea`), Ligue 1 (`ligue1`), Champions League (`ucl`), NCAAF top 25 (`ncaaf`), NCAAM top 25 (`ncaam`), WNBA, Formula 1 (`f1`), UFC, and PGA Tour (`pga`).

## Settings and permissions

- **Settings (`settings.schema.json`):**
  - `leagues` (`multiSelect`): leagues included on the Sports screen and live score feed.
  - `region` (`select`): preferred broadcast region (`us`, `ca`, `gb`, `eu`, `au`, or `all`). When an event has no broadcasts for the chosen region, all listed broadcasts are returned as fallback so Live TV matching keeps working.
- **Network permissions:** `site.api.espn.com` and `site.web.api.espn.com` only.

## Plugin endpoints

- `GET /ether/v1/live/sports/events?from=&to=&league=&ids=`
- `GET /ether/v1/live/sports/events/{id}`
- `GET /ether/v1/live/sports/entities?kind=team&league=&q=`
- `GET /ether/v1/live/sports/standings?league=`
- `GET /ether/health`
- `POST /ether/configure`
- `POST /ether/test`

## Architecture and caching

- **Provider interface (`src/providers/types.ts`):** `EspnSportsProvider` (`src/providers/espn.ts`) implements `SportsProvider`. Another provider can be passed to `createSportsHandler({ provider })` without changing routing, settings validation, close-game heuristics, or caching helpers.
- **Concurrency & caching:** outbound requests use `ether.http.fetchMany` capped at 6 concurrent requests per host (chunked at 64 per call). KV cache TTLs are 15 s for scoreboards/details with live (`in`/`delayed`) events, 5 min for non-live scoreboards/details, 15 min for standings, and 24 h for teams/entities. Transient upstream errors fall back to stale cached values when available.
- **Self-contained hosted sources:** all imports stay relative inside `sdk/examples/sports/src/` so Ether Studio can import and edit `src/plugin.ts` directly without external package imports. `src/contract.ts` mirrors the v0.3.19 `live_events` and `screens` contract types locally.

## Build, check, and test

From the repository root:

```sh
# Offline fixture tests (zero network)
node --test sdk/examples/sports/tests/plugin.test.ts

# Check and pack once live_events & screens manifest validation is enabled
go run ./cmd/ether plugin check sdk/examples/sports
go run ./cmd/ether plugin pack -o ~/.scratch/sports.etherpkg sdk/examples/sports
```

## Ether Studio recipe

See [STUDIO_RECIPE.md](./STUDIO_RECIPE.md) for a step-by-step Ether Studio walkthrough covering importing the plugin, exercising every route in the Inspector, customizing leagues or close-game rules, and swapping in a custom `SportsProvider`.

## Limits and ESPN API notes

- Uses ESPN's public, undocumented JSON endpoints (`site.api.espn.com` and `site.web.api.espn.com`), which may change without notice.
- ESPN's team-sport scoreboard endpoints reject `YYYYMMDD-YYYYMMDD` date ranges with HTTP 400; multi-day windows fan out bounded single-day requests (up to 7 days) through `http.fetchMany`, while multi-day weekend events (`f1`, `pga`, `ufc`) use range queries.
- ESPN returns HTTP 404 for `/summary` on `f1`, `pga`, and `ufc`; event detail falls back to scoreboard data to build race results/weekend sessions, golf leaderboards, and fight cards.
- NBA and MLB standings returned by ESPN are not pre-sorted by rank; the provider sorts entries by `playoffSeed`/`rank` ascending before rendering tables.
- Test fixtures in `tests/fixtures/` are trimmed recordings of real ESPN responses captured on 2026-11-22.
