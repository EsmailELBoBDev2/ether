// Mock-only fixtures for the Rows & Pages design screenshots (vite --mode mock).
// Not shipped: the route that uses them exists only in mock builds.
import type { TitleCard } from "../../lib/contract";
import { buildCatalog, card, posterFor } from "../../mock/data";

export type Experience = "watch" | "listen" | "read" | "live";
export interface Lib {
  id: number;
  name: string;
  experience: Experience;
  plugin?: string;
}
export const LIBS: Lib[] = [
  { id: 1, name: "Movies", experience: "watch" },
  { id: 2, name: "TV Shows", experience: "watch" },
  { id: 7, name: "Anime Shows", experience: "watch" },
  { id: 5, name: "Music", experience: "listen", plugin: "Music" },
  { id: 6, name: "Audiobooks", experience: "listen", plugin: "Audiobooks" },
  { id: 8, name: "Sports Replays", experience: "watch", plugin: "Sports" },
];
export const libName = (id: number) => LIBS.find((l) => l.id === id)?.name ?? "";

export interface PageInfo {
  key: string;
  label: string;
  group: string;
  rows: number;
  mode: "curated" | "personalized";
}
export const PAGES: PageInfo[] = [
  { key: "home", label: "Home", group: "Everywhere", rows: 9, mode: "personalized" },
  { key: "library:1/recommended", label: "Movies › Recommended", group: "Watch", rows: 8, mode: "personalized" },
  { key: "library:2/recommended", label: "TV Shows › Recommended", group: "Watch", rows: 8, mode: "personalized" },
  { key: "library:7/recommended", label: "Anime Shows › Recommended", group: "Watch", rows: 6, mode: "personalized" },
  { key: "library:8/recommended", label: "Sports Replays › Recommended", group: "Watch", rows: 4, mode: "curated" },
  { key: "library:5/home", label: "Music › Home", group: "Listen", rows: 6, mode: "curated" },
  { key: "library:5/concerts", label: "Music › Concerts", group: "Listen", rows: 3, mode: "curated" },
  { key: "library:6/home", label: "Audiobooks › Home", group: "Listen", rows: 4, mode: "curated" },
];

export type Placement = "pinned" | "show" | "auto" | "hide";
export type Layout = "standard" | "hero" | "wide" | "tiles" | "tracks" | "surface";
export interface Row {
  id: string;
  source: string; // display name of the source
  title: string; // the row title as served
  group?: string; // fan-out block label
  personal: boolean;
  placement: Placement;
  position?: number;
  libraries: string; // display summary
  when?: string;
  devices?: string;
  audience?: string;
  layout: Layout;
  card?: "poster" | "square" | "round" | "landscape";
  why: string;
  items: TitleCard[];
  note?: string; // skipped / flags line
  flag?: string; // age flag
  plugin?: string;
}

const CAT = buildCatalog(220).map(card);
const movies = CAT.filter((c) => c.type === "movie");
const shows = CAT.filter((c) => c.type === "show");
const pick = (xs: TitleCard[], from: number, n = 12) => xs.slice(from, from + n);

function audio(prefix: string, names: string[], type: "album" | "artist" | "audiobook" = "album"): TitleCard[] {
  return names.map((n, i) => ({
    key: `${prefix}-${i}`,
    type: "movie" as TitleCard["type"],
    title: n,
    year: type === "artist" ? undefined : 2026 - (i % 4),
    poster: posterFor(`${prefix}${i}${n}`, n),
  }));
}
const ALBUMS = audio("alb", [
  "Glass Harbour", "Night Transit", "Low Tide Radio", "Paper Moons", "Saltwater Hymns",
  "Neon Orchard", "The Quiet Hour", "Copper Skies", "Velvet Static", "Northbound", "Lantern", "Afterglow",
]);
const PLAYLISTS = audio("pl", [
  "Friday Night Mix", "Focus Flow", "Sunday Slow", "Road Trip '26", "Kids' Car Songs", "Workout Heat",
  "Rainy Day Jazz", "Dinner Party",
]);
const ARTISTS = audio("ar", ["Mara Vey", "The Lowlands", "Kite Theory", "Junie Blue", "Ossian", "Halcyon Drive", "Rook & Wren", "Teo Marin"], "artist");
const BOOKS = audio("bk", [
  "The Long Orbit", "A Map of Salt", "Winter Engines", "The Ninth House Key", "Small Gods of Rain",
  "Harbour Lights", "The Clockmaker's Daughter", "Under Copper Skies",
], "audiobook");

export const TRACKS = [
  ["Glass Harbour", "Mara Vey", "Glass Harbour", "3:41"],
  ["Night Transit", "The Lowlands", "Night Transit", "4:12"],
  ["Paper Moons", "Junie Blue", "Paper Moons", "3:05"],
  ["Copper Skies", "Kite Theory", "Copper Skies", "3:58"],
  ["Lantern", "Ossian", "Lantern", "5:20"],
];

const kidRated = (xs: TitleCard[], rating: string) => xs.map((x) => ({ ...x, contentRating: rating }));

export function homeRows(kid: boolean): Row[] {
  const mv = kid ? kidRated(pick(movies, 30, 12), "PG") : pick(movies, 0);
  return [
    { id: "r1", source: "Continue", title: "Continue Watching", personal: true, placement: "pinned", position: 1, libraries: "Any library", layout: "standard", why: "Pinned to #1 for everyone", items: kid ? kidRated(pick(shows, 40, 5), "TV-Y7") : pick(shows, 3, 6) },
    { id: "r2", source: "Plugin row · Sports", title: "Tonight", personal: false, placement: "pinned", position: 2, libraries: "—", layout: "surface", why: "Pinned to #2 for everyone", items: [], plugin: "Sports", flag: kid ? "Sports Tonight · TV-PG (declared by the plugin)" : undefined },
    { id: "r3", source: "Trending · This week", title: "Trending Movies", group: "Trending · one row per library", personal: false, placement: "pinned", position: 3, libraries: "Movies, TV Shows · one row per library", layout: kid ? "standard" : "hero", why: "Pinned to #3 for everyone", items: mv, note: kid ? undefined : "Shared · 2 skipped (shown above)" },
    { id: "r3b", source: "Trending · This week", title: "Trending TV Shows", group: "Trending · one row per library", personal: false, placement: "pinned", position: 4, libraries: "Movies, TV Shows · one row per library", layout: "standard", why: "Same row, next library", items: kid ? kidRated(pick(shows, 20), "TV-PG") : pick(shows, 10) },
    { id: "r4", source: "New · Released recently", title: "New Movies", group: "New · one row per library", personal: false, placement: "show", libraries: "Movies, TV Shows · one row per library", layout: "standard", why: "You chose Show; Ether placed it here", items: kid ? kidRated(pick(movies, 60), "G") : pick(movies, 40) },
    { id: "r4b", source: "New · Released recently", title: "New TV Shows", group: "New · one row per library", personal: false, placement: "show", libraries: "Movies, TV Shows · one row per library", layout: "standard", why: "Same row, next library", items: kid ? kidRated(pick(shows, 60), "TV-Y7") : pick(shows, 30) },
    { id: "r5", source: "Because you watched", title: kid ? "Because Luca watched Paper Boats" : "Because you watched The Long Shore", personal: true, placement: "auto", libraries: "Any library", layout: "standard", why: "Ether chose it: Luca plays these most on weekday evenings", items: kid ? kidRated(pick(movies, 80), "PG") : pick(movies, 70) },
    { id: "r6", source: "Collection", title: "Family Movie Night", personal: false, placement: "show", libraries: "Movies", when: "Fri–Sat evenings", audience: "Only Luca, Mia", layout: "wide", card: "landscape", why: "Shown Fri–Sat evenings", items: kidRated(pick(movies, 100), "PG"), flag: kid ? "1 unrated title (hidden for Luca)" : undefined },
    { id: "r7", source: "Jump back in", title: "Jump back in · Music", personal: true, placement: "auto", libraries: "Music", layout: "standard", card: "square", why: "Ether chose it", items: ALBUMS.slice(0, 8) },
  ];
}

export function musicRows(): Row[] {
  return [
    { id: "m1", source: "Jump back in", title: "Jump back in", personal: true, placement: "show", libraries: "This library", layout: "tiles", card: "square", why: "1 · in your order", items: PLAYLISTS.slice(0, 6) },
    { id: "m2", source: "Music · New releases", title: "New releases", personal: false, placement: "show", libraries: "This library", layout: "standard", card: "square", why: "2 · in your order", items: ALBUMS, plugin: "Music" },
    { id: "m3", source: "Playlists", title: "Your playlists", personal: true, placement: "show", libraries: "This library", layout: "standard", card: "square", why: "3 · in your order", items: PLAYLISTS },
    { id: "m4", source: "Music · Top songs", title: "Top songs this week", personal: false, placement: "show", libraries: "This library", layout: "tracks", why: "4 · in your order", items: [], plugin: "Music" },
    { id: "m5", source: "Music · Artists you play", title: "Artists you play", personal: true, placement: "show", libraries: "This library", layout: "standard", card: "round", why: "5 · in your order", items: ARTISTS, plugin: "Music" },
    { id: "m6", source: "Recently added", title: "Recently added", personal: false, placement: "show", when: "Weekends", libraries: "This library", layout: "standard", card: "square", why: "6 · in your order", items: ALBUMS.slice(4) },
  ];
}
export const BOOK_ROWS_HINT = BOOKS;

export interface SourceEntry {
  title: string;
  desc: string;
  personal: boolean;
  plugin?: string;
  only?: string;
  used?: string;
}
export const SOURCE_GROUPS: { name: string; items: SourceEntry[] }[] = [
  { name: "Keep watching", items: [
    { title: "Continue", desc: "What each viewer is part-way through, in any library", personal: true, used: "On this page" },
    { title: "Up Next", desc: "The next episode of shows they're watching", personal: true },
    { title: "Watchlist", desc: "Coming soon and now available from their watchlist", personal: true },
  ] },
  { name: "Trending & popular", items: [
    { title: "Trending", desc: "What's trending on TMDB today or this week", personal: false, used: "On this page" },
    { title: "Popular", desc: "TMDB's most popular right now", personal: false },
    { title: "Top 10 today", desc: "Trending, popularity and what viewers here watch, numbered", personal: false },
    { title: "Popular on this server", desc: "What viewers here played most in 30 days", personal: false },
  ] },
  { name: "New", items: [
    { title: "New", desc: "Newest releases, or newly playable here", personal: false, used: "On this page" },
    { title: "Recently added", desc: "Newest additions to a library", personal: false },
    { title: "Airing today", desc: "Shows with an episode out today", personal: false },
    { title: "Upcoming", desc: "Coming soon, weeks or months ahead", personal: false },
  ] },
  { name: "For you", items: [
    { title: "Because you watched", desc: "Like something they just finished · up to 3 rows", personal: true, used: "On this page" },
    { title: "Top 10 for you", desc: "The ten titles the taste model rates highest", personal: true },
    { title: "Hidden gems", desc: "Little-known titles that fit their taste", personal: true },
  ] },
  { name: "Top rated", items: [
    { title: "Top rated", desc: "TMDB's top rated, optionally in one genre", personal: false },
    { title: "Acclaimed & unseen", desc: "Trusted high ratings they haven't seen", personal: true },
  ] },
  { name: "Browse", items: [
    { title: "By genre", desc: "Ether picks a genre per viewer, or pin one (Anime, Horror…)", personal: false },
    { title: "By decade", desc: "Ether picks, or pin one (1990s…)", personal: false },
    { title: "More from a studio or network", desc: "A24, Studio Ghibli, HBO…", personal: true },
  ] },
  { name: "Your collections", items: [
    { title: "Family Movie Night", desc: "Collection · 48 titles · PG and under", personal: false, used: "On this page" },
    { title: "Ghibli & Friends", desc: "Collection · 31 titles", personal: false },
  ] },
  { name: "From plugins", items: [
    { title: "Anime with an English dub", desc: "English Dubs", personal: false, plugin: "English Dubs" },
    { title: "Tonight", desc: "Sports · live and upcoming games", personal: false, plugin: "Sports", only: "Only in Vela", used: "On this page" },
    { title: "Popular", desc: "Cinemeta catalogue", personal: false, plugin: "Cinemeta" },
  ] },
  { name: "Live", items: [{ title: "Live now", desc: "What's on your channels right now", personal: false }] },
];

export const VIEWERS = [
  { id: "everyone", label: "Everyone" },
  { id: "luca", label: "Luca", hint: "Age limit PG · 2 changes" },
  { id: "mia", label: "Mia", hint: "Age limit PG-13" },
  { id: "sam", label: "Sam", hint: "No changes" },
];

export const TRENDING_MOVIES = pick(movies, 0, 8);
export const TRENDING_SHOWS = pick(shows, 10, 8);
