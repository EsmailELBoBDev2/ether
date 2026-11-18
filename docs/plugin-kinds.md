# Plugin content kinds

A content plugin adds a whole **library** to Ether — audiobooks,
comics, music, books, photos, field recordings, anything flat or
hierarchical — with no core changes. The owner meets each kind you
declare as a library beside Movies and TV Shows, in the console and
in every client app you publish it to.

This page is the contract. The worked example is
[`sdk/examples/kind-library`](../sdk/examples/kind-library); the
Studio "A new library" template generates the same skeleton from a
description.

## Declare a kind

In the manifest's `ether` block: `kind: "content"`, at least one of
the `catalog_provider` / `kind_resolver` capabilities (plus optional
`matcher` and `frontend_adapter`), and one entry per kind in
`contributes.mediaKinds`:

```jsonc
{
  "key": "recordings",              // ^[a-z][a-z0-9-]{0,31}$, unique in the plugin
  "label": "Field recordings",
  "library": {
    "name": "Field recordings",     // default name; the owner can rename
    "card": "landscape",            // optional: poster (default) | landscape | square
    "tabs": [                       // optional (<= 6); defaults to Recommended + Library
      { "key": "recommended", "label": "Recommended", "kind": "recommended" },
      { "key": "library", "label": "Library", "kind": "library" }
    ]
  },
  "levels": [                       // 1–5 levels, first is the root
    { "key": "collection", "label": "Collection" },
    { "key": "album", "label": "Album", "container": true },
    { "key": "track", "label": "Track", "playable": true }
  ],
  "fields": [                       // validated per item; indexed ones can facet
    { "key": "genre", "type": "string", "label": "Genre", "indexed": true },
    { "key": "durationSec", "type": "duration", "label": "Duration", "indexed": true }
  ],
  "present": { /* see "Publishing" */ },
  "browse": { "sorts": ["title", "index"], "facets": ["genre", "durationSec"], "search": true, "liveSearch": true },
  "sync": { "mode": "pull", "refresh": "1h" },
  "cache": { "maxBytes": 2147483648 },        // optional; per-library blob cache
  "match": { "thresholdDefault": 0.8 }        // optional
}
```

Rules that will bite if ignored: at least one level must be
`playable`; facet fields must be `indexed`; field keys may not reuse
envelope names (`id`, `title`, …); declarations are **additive across
versions** — removing or retyping a field/level is refused at upgrade
unless the owner agrees to replace the library's contents.

### Library presentation, sub-tabs, sorts and facets in Vela

`library` and `browse` shape how Vela renders the library without per-plugin client logic:

- `library.card`: `"poster"` (2:3, default), `"landscape"` (16:9; Ether serves the item's `thumb`, else `backdrop`, as `artwork[]` kind `"thumb"`, falling back to `poster`), or `"square"` (1:1).
- `library.tabs`: up to 6 entries of `{key, label, kind, facet?, sort?, direction?}` where `kind` is `"recommended"`, `"library"`, or `"collections"`. Keys match `^[a-z][a-z0-9-]{0,31}$` and are unique within the library; labels support `$t:` keys and follow copy rules (≤40 characters). Omitting `tabs` defaults to `Recommended` (`recommended`) and `Library` (`library`). A `"collections"` tab may set `facet` (one of `browse.facets`), `sort` (one of `browse.sorts`), and `direction` (`"asc"` or `"desc"`, defaulting to `released` `desc` when `released` is declared); `GET /vela/v0/libraries/{id}/collections?tab=<tabKey>` pages those facet buckets most-items-first with the newest item's artwork, and opening one navigates to `/vela/v0/libraries/{id}/items?facet.<field>=<valueId>&sort=&direction=`.
- `browse.sorts` and `browse.facets`: surfaced directly on `GET /vela/v0/libraries` under `browse.sorts` and `browse.filters.facets`. Facet values are paged at `GET /vela/v0/libraries/{id}/facets/{field}` (`int`, `date`, and `duration` fields are served as discrete buckets — years for `date`, duration ranges for `duration`, and distinct integers up to 50 or ranges above 50) and filtered with repeatable `facet.<field>=<bucketId>` on `GET /vela/v0/libraries/{id}/items` alongside in-library `query=`.
- Recommended rows (`GET /vela/v0/libraries/{id}/rows`): Ether builds up to 8 shelves (20 items each) from your stored items — Continue watching, Recently added, Newest, Top rated (when `rating` is a declared sort), and top values of your first declared facet.

## The item envelope

Your provider returns `KindItem`s: your own `id` (letters, digits,
`. _ ~ -`, ≤128 chars — Ether namespaces it, you never see the stored
identity), `level`, `parentId`, `index` within the parent, `title`,
optional `summary/year/released/rating`, `playable`, counts, `art`
(`poster/backdrop/thumb/logo` plus declared roles), `fields` (checked
against your declaration; unknown keys are dropped, wrong types
reject the item), `hints` (an opaque JSON object ≤16 KiB, stored and
handed back to your resolver verbatim — put ids and locators there,
never secrets), and an `etag` for cheap re-syncs.

Caps: title 500 characters, summary 20k, each field 4k, 16 art roles.

## Routes Ether calls

Kind and setup routes are `GET`/`POST` with JSON in and out, through the
normal plugin transport. Frontend-adapter traffic keeps whatever method and
query the client sent:

| Route | When | Notes |
|---|---|---|
| `/ether/v1/kinds/{kind}/browse` | Pull sync walks your catalogue | One level per call (`parentId`, `level`, `cursor`, `limit`). Return `nextCursor` or the full set up to `limit`; never more than `limit` items. |
| `/ether/v1/kinds/{kind}/changes` | Pull sync refresh | Optional delta feed `{upserts, deletes, nextCursor, complete}`. Answer 404/501 and Ether re-walks and diffs by `etag` instead. |
| `/ether/v1/kinds/{kind}/items/{id}` | Conformance and detail refresh | One full item. |
| `/ether/v1/kinds/{kind}/search` | Client searches, only when `browse.liveSearch` is on | Same page shape as browse with `query` set. Short budget; see below. |
| `/ether/v1/kinds/{kind}/resolve` | A viewer presses play | `{libraryKey, item, partIndex?, client?}` → ordered `sources`. Never called during browse. |
| `/ether/v1/kinds/{kind}/match` | Resolve-time matching, console re-match | `{item, candidates}` → `{matches:[{candidateRef, score 0..1, reasons}]}` best first. |
| `/ether/v1/setup/{step}/test` | The setup wizard | `{step, fields}` → `{ok, detail, fields?}`. Secrets arrive via `SecretGet`, never in this body. |
| `/ether/v1/frontend/{key}/…` | Your frontend adapter's traffic | Any method, and the original query string (repeated keys flatten to the first value). Ether authenticated the viewer first; you get `X-Ether-Viewer` headers, never a credential. |

Clients are **always served from Ether's store** — browse, search
and detail never call you live, so nothing scales with your catalogue
size. You are called at sync time and at play time.

Sync modes: **pull** (Ether walks `browse`, then polls `changes` on
your `refresh`, minimum 15m) or **push** (you call the `KindUpsert`
host service; refused for pull kinds — one writer per kind). A pull
walk that would delete more than half the library aborts and raises
owner attention instead of wiping it.

### Live search

With `browse.liveSearch: true`, a client search on your library also
calls your `/search` route with a short budget (about two seconds).
Results are validated, **upserted into the store like synced items**
(stubs are fine — playable leaves resolve at play time), and merged
with local results. On timeout or error the viewer gets local
results in time and your late answer still lands in the store for
the next search. Declare it only if your source can answer a search
quickly; the Studio template has it on with the explanation beside
the toggle. Pair it with `browse.search: true` so earlier live
results stay findable locally.

## Resolving bytes

`resolve` returns ordered `PlayableSource`s; Ether tries them in
order. Patterns:

- **file** — a hosted resolver path inside an owner-picked folder; requires
  `folders: read` and live containment checks when resolving and opening.
- **range** — a URL Ether reads with HTTP Range (demoted to download
  when the host cannot range and the size fits the cache).
- **download** — fetched whole into the per-library blob cache
  (SHA-256 checked when you provide one), then served locally.
- **direct** — the client surface is handed the URL (only where the
  surface supports it).
- **container** — one member of a zip/RAR/7z, possibly multi-volume:
  `{volumes:[{url,size}], member}` or `memberIndex`. Passwords come
  from a named settings secret (`passwordSecret`), never from your
  response. Store-mode members stream by byte range; compressed ZIP
  (deflate), non-solid 7z (LZMA/LZMA2) and RAR4/RAR5 members decode
  from ranged reads into the library cache; solid 7z caches the volume
  first under the request context and library cache quota. Encrypted
  non-stored members and unsupported archive formats or compression
  methods fail with `archive_unstreamable`.
- **release** — hand Ether a magnet/infohash/NZB and its existing
  debrid/Usenet/torrent pipeline opens it. For torrents, set
  `release.fileIndex` (zero-based index in the provider's complete file list),
  `release.candidate.fileIndex`, or `release.file` (exact path or unique
  basename) to open audio, books, comics, ROMs or other non-video files.
  A top-level `fileIndex` overrides the candidate's index. If both index and
  `file` are supplied they must identify the same file. Empty files,
  invalid indices and missing or ambiguous names fail without fallback.
  Without either selector the existing video picker is used. In Go, indices
  are `*int`: a pointer to zero selects the first file; nil leaves selection
  automatic. Indices refer to the provider's ordering, not its file IDs;
  prefer a path when the provider's ordering is unknown. Explicit selectors
  on an NZB release are rejected; use a container source for archive members.
  `fileHint` retains its existing meaning as an archive member hint.

`ResolveSources({kind: "indexer_search", query?, category?, profile?})` accepts a
comma-separated list of Newznab category IDs (`"3000"`, `"7030"`) or standard
names: `console`, `movies`/`movie`, `audio`/`music`, `pc`, `tv`, `adult`,
`books`, `comics`, `other`. It sends a generic search with those categories
and the query unchanged, rather than assuming a movie or TV request.
When `query` is omitted or empty and `category` is set, Ether issues a
recent-releases / RSS request (`t=search&cat=…` with no `q`) and returns up to
the newest 100 candidates; an empty `query` with no `category` is refused.
Each `indexer_search` candidate carries `indexer` and `indexerId`, and video
release names also receive `score`, `resolution`, `source` and `codec` scored
against `profile` (or the owner's default quality profile when `profile` is
empty); non-video candidates leave those four fields empty, and unknown profile
names are rejected. Invalid categories are rejected. Stremio/addon sources do
not support category filters and reject scoped searches. Omit `category` for
the default media search. Results and acquired files are cached separately by
category and file selection. The wire shapes are documented in
[`plugin-sources.v1.json`](../schemas/plugin-sources.v1.json).

URLs may carry `{itemIndex}`, `{pageNumber}` (= itemIndex + 1),
`{byteStart}`, `{byteEnd}` and `{byteRange}` placeholders; Ether
substitutes integers only. Resolver `headers` may use
`{secret:name}` placeholders, substituted host-side — never put a
raw secret in a response (responses are logged). Returning zero
sources means "not available right now" and is cached briefly.

With `permissions.sources: "search"` (or `"acquire"` to open debrid
content) your resolver can ask Ether to find bytes through the
owner's indexers and debrid via the `ResolveSources` host service,
rank the candidates yourself (or in `/match`), and return a
`release` source.

### Multi-file items (`hints.parts`)

When one playable item consists of several ordered files (multi-disc sets,
CUE + BIN tracks, multi-part audiobooks, split archives), list its parts on the
item in `hints.parts` (an array of `{name, mime?, size?, durationSec?}` objects
or filenames; Ether also reads a declared `fields.parts` array when
`hints.parts` is absent):

```json
{
  "id": "ff7",
  "level": "game",
  "title": "Final Fantasy VII",
  "playable": true,
  "hints": {
    "parts": [
      { "name": "Disc 1.chd", "mime": "application/octet-stream", "size": 524288000 },
      { "name": "Disc 2.chd", "mime": "application/octet-stream", "size": 512000000 },
      { "name": "Disc 3.chd", "mime": "application/octet-stream", "size": 498000000 }
    ]
  }
}
```

When `parts` is non-empty, Ether calls `/ether/v1/kinds/{kind}/resolve` per
part with zero-based `partIndex` (`0 .. len(parts)-1`), caches and invalidates
resolved sources per `(item, partIndex)`, and isolates pending prepare jobs per
part:

- **Whole-item opens** (standard protocol playback or `/ether/v1/kind-media/{ticket}`
  without a part selector) resolve `partIndex: 0` for single-part items and
  deliver multi-part items as an ordered, byte-rangeable `Sequence` across all
  parts (`0 .. N-1`).
- **Per-part opens** let frontend adapters and multi-file clients fetch each
  file individually:
  - Mint a part-scoped ticket URL with `ether.frontend.streamURL(itemRef, partIndex).url`
    (hosted JS), `streamUrl(ctx, itemRef, partIndex)` (TypeScript SDK),
    `ctx.StreamPartURL(itemRef, partIndex)` (Go SDK), or
    `ctx.stream_url(item_ref, part_index)` (Python SDK) — backed by
    `POST /ether/plugin-host/frontend/stream-url` with `{"viewerId", "itemRef", "partIndex"}`.
  - Or pass `?part=N` on an unpinned item ticket (`/ether/v1/kind-media/{ticket}?part=0`).
    A malformed or negative `part` answers `400 invalid_part`, an out-of-range
    part answers `404 item_not_found`, and a `?part=N` that disagrees with a
    part-scoped ticket answers `404 ticket_invalid`.

Watch state and progress are keyed per playable leaf. For paged
media, store progress against the leaf the viewer is on (its index
is the natural unit); containers themselves carry no progress.

## Publishing (`present`)

Each library publishes per protocol; the owner can override your
defaults per library afterwards.

- `vela: {type: "movies"|"shows"}` — Ether's own app (`shows` needs at
  least two levels).
- `plex: {base: "movie"|"show"|"artist"|"photo"}` — root→show/artist,
  middle→season/album, leaf→episode/track. More than three levels
  are flattened with indexes kept (the Studio preview shows this).
- `mediaBrowser: {collectionType, itemTypes}` — Jellyfin/Emby.
  Item types: `Movie, Series, Season, Episode, MusicAlbum, Audio,
  Book, PhotoAlbum, Photo`, mapped per level; unmapped levels use
  the collection's natural container.
- `stremio: {type}`, `xtream: {category}`, `silo: {type}`.
- `adapterOnly: true` — no generic mapping fits; ship a
  `frontend_adapter` and serve the protocol yourself. Embed
  `/ether/v1/kind-media/{itemId}` stream URLs for bytes (the SDKs'
  `StreamURL`/`stream_url`/`streamUrl` helper) instead of proxying
  media through the adapter. A refused stream URL answers
  `{"error": {"code", "message"}}` with the code also in the
  `X-Ether-Media-Error` header, and the host log records the cause
  (`kind media refused`):

  | Status | Code | Meaning |
  |---|---|---|
  | 400 | `invalid_part` | `?part=` is not a non-negative integer |
  | 404 | `ticket_invalid` | Unknown, expired (2 min), out-of-scope, or part-mismatched ticket |
  | 403 | `viewer_invalid` | The ticket's viewer can no longer play |
  | 404 | `item_not_found` | The item (or requested part index) is not in a library this viewer can open |
  | 409 | `no_source` | The item has no playable source (your resolve returned none) |
  | 422 | `archive_unstreamable` | This archive format or compression method cannot be streamed |
  | 503 | `preparing` | Resolve is still `pending` after the 25 s wait budget (`Retry-After` gives seconds to wait) |
  | 503 | `source_unavailable` | The source is temporarily unavailable |
  | 502 | `open_failed` | The source could not be opened |

A library the owner cannot see is invisible to that viewer on every
surface; you never check viewer permissions yourself.

## Matching

Matching is yours; persistence and the console are Ether's. A manual
pin from the item drawer always wins, then your best score at or
above the kind's threshold (manifest default, owner-editable), then
an inline match during resolve. Give plain-language `reasons` — the
owner reads them.

## Host services

Beyond the usual services, kind plugins get (capability-checked on
every call): `KindUpsert` / `KindDelete` (push kinds), `KindItems`
(read your own library back), `ResolveSources`, and
`CachePut` / `CacheOpen` for the per-library blob cache
(content-addressed `blob:<sha256>` refs; you never name paths).
Default cache quota is 2 GiB per library under a shared cap of
min(10 GiB, 5% of the cache disk); your `cache.maxBytes` and the
owner can override it.

## Setup

Declare `contributes.setup.steps` (any plugin kind): each step lists
settings fields and an optional test route. The console walks the
steps in a drawer; a content plugin's libraries stay hidden and sync
waits until required steps pass (the owner may skip, and sees a
setup banner instead).

## SDKs

Same contract in all three, pinned by the shared fixtures in
`sdk/conformance/kinds` (each SDK's tests replay them):

- **Go** (`sdk/go`): `Plugin.OnCatalog / OnResolve / OnMatch /
  OnFrontend / OnSetupTest`, the `CatalogProvider`, `KindResolver`,
  `KindMatcher`, `FrontendAdapter` interfaces, host kind methods on
  `Host`, and `sdk/go/kit` helpers — `scrape` (polite host-HTTP
  fetching), `archivex` (zip/cbz/epub members), `metaparse` (EPUB
  OPF, ComicInfo, ID3, FLAC), `feedparse` (RSS/Atom/JSON Feed →
  items), `hints` (typed hints/fields).
- **Python** (`sdk/python`): `CatalogProvider`, `KindResolver`,
  `KindMatcher`, `FrontendAdapter`, `SetupTester` base classes with
  `on_catalog` etc., `ctx.ether.kinds` / `.cache`, `parse_feed` and
  `archive_list` helpers.
- **TypeScript** (`sdk/typescript`): `onCatalog / onResolve /
  onMatch / onFrontend / onSetupTest`, `ctx.ether.kinds` / `.cache`,
  `parseFeed`, `archiveList`, `scrapeText/Links`.

Content plugins are usually best as `remote` (scraping wants a real
process); `hosted` works for pure-API providers — the example and
the Studio template are hosted. In hosted JS, read settings with
`ether.settings.get()` and kind services via `ether.kinds`,
`ether.sources`, `ether.cache`.

## Checking your work

- The conformance kit (`kit.CheckKindProvider`, run by Studio and
  `ether plugin check`) walks your provider offline: pagination must
  terminate, pages must fit the limit, detail must round-trip, a
  declared live search must answer, resolver sources must use known
  patterns and placeholders.
- Studio's kind preview browses your draft, shows the hierarchy and
  the per-protocol projection, and dry-runs the resolver on a real
  leaf (template expansion included) before anything is installed.
- Start from the Studio "A new library" template or copy
  `sdk/examples/kind-library`; its test is the executable version of
  this page.


### Granted local files (hosted runtime)

A resolver can return a source such as
`{"id":"local","pattern":"file","path":"/picked/library/Game.nds","mime":"application/octet-stream"}`.
This requires `folders: read` and an owner-picked, non-revoked folder grant.
Ether uses the same `validateLocalFile` and `openGrantedLocalFile` path as
Stremio `ether.file`: it rejects `..`, resolves symlinks, checks containment at
resolve time and again at open time, and opens through `os.OpenRoot`.
Remote/sidecar resolvers cannot serve host files with this pattern.

### File identification without file contents

Hosted plugins can call `ether.folders.hash(root, path)` (TypeScript SDK:
`host.folders.hash`; Go SDK: `FoldersHash`). `path` is relative to the picked
root. The service requires `folders: read` and the same live folder grant as
`folders.stat`. It refuses traversal, symlink escapes and non-regular files.
It returns `{size, modTime, crc32, md5, sha1}`: Unix seconds for `modTime`,
lowercase hexadecimal digests (CRC32 IEEE, 8 digits; MD5, 32; SHA1, 40).
No file contents cross the plugin boundary. All three hashes are computed in
one streaming pass with fixed memory. `hash` stays synchronous for files up to
256 MiB; larger files must use `hashStart(root, path)` (Go: `FoldersHashStart`).
This explicitly starts a background job and returns `{id, state, bytes}`.
Poll `hashPoll(id)` (`FoldersHashPoll`) for bytes processed and state: `running`,
`done`, `canceled`, or `error`. A done job includes `result` with the hash object;
an error includes `error`. `hashCancel(id)` (`FoldersHashCancel`) requests cancellation;
poll until terminal to observe completion. Start and poll do not return file bytes.

There is no file-size ceiling for jobs. At most one runs per plugin and two
across the process; a busy start returns an error to retry later. Starting the
same active root/path returns its existing job ID. Readers yield for 2 ms after
each 1 MiB chunk. Jobs survive the initiating call, have a 24-hour deadline,
and recheck grants and cancellation every chunk. Files changing during hashing
abort without caching. Revocation or plugin disable stops ongoing reads.

Successful digests persist in a host-owned database cache indexed by plugin,
resolved path, size and nanosecond modification time. Unchanged files reuse the
cached digest after permission and containment checks. Cache rows are removed
when the plugin is uninstalled. Only explicitly requested files are hashed.
Job status is process-local, and each plugin's terminal status is retained until
its next start; after a restart, start again to reuse any completed cached result.
SDK sidecars cannot use these hosted folder services.

### Item availability

An upsert can carry
`"unavailable":{"code":"no_verified_source","detail":"No source matched the DAT record"}`.
The code must be nonempty and at most 128 bytes; detail is optional and at most
2000 bytes. The item remains visible in browse/counts so owners can inspect the
coverage gap. Ether exposes the reason in the item drawer, marks the item
nonplayable, and never calls its resolver or opens a cached resolution while
this declaration is present. The declaration also travels in frontend item
details. The next upsert without `unavailable` clears it, even with the same
etag. This is independent of the level's `playable` capability; clearing the
reason does not grant playback to a nonplayable level.

### Large downloads and archive members (v0.3.12)

Hosted and remote clients can keep large data in the host's existing library
blob cache, reading only the metadata or descriptor bytes the plugin itself must parse.
Big media (video/audio streams, ROMs, disc images, firmware downloads) never goes through
`cache.read`, `archive.open`, or `archive.read` — Ether streams cached blobs and archive
members directly to clients itself (for example via frontend `{ blob: { libraryKey, ref, memberIndex } }`
descriptors in `internal/plugins/frontend_blob.go`, `frontend.streamURL`, and playable container sources):

```ts
const limits = await ctx.ether.runtime!.limits();
const readBudget = limits.readBytes ?? 8 * 1024 * 1024; // clamp(memoryBytes / 4, 8 MiB, 512 MiB)
const cached = await ctx.ether.http.fetchToCache!({
  url: "https://datasets.example/platform.zip",
  libraryKey: "pk-community-example-library-games", // this plugin's own library
  name: "platform.zip",
});
const members = await ctx.ether.archive!.list(cached.blob);

// Batch up to 4096 small/medium members per call within readBytes:
const xmlMembers = members.filter(m => !m.unsafe && m.name.endsWith(".xml") && m.size > 0);
let batch: Array<{ member: number; offset: number; length: number }> = [];
let batchBytes = 0;
for (const m of xmlMembers) {
  const length = Math.min(m.size, readBudget);
  if (batch.length === 4096 || batchBytes + length > readBudget) {
    const results = await ctx.ether.archive!.read(cached.blob, batch);
    // results: [{ index, member, size, body: Uint8Array }] in request order
    batch = [];
    batchBytes = 0;
  }
  batch.push({ member: m.index, offset: 0, length });
  batchBytes += length;
}
if (batch.length > 0) {
  await ctx.ether.archive!.read(cached.blob, batch);
}

const extracted = await ctx.ether.archive!.extract(cached.blob, xmlMembers[0]!.index);
// {ref: "blob:<sha256>", size, sha1, md5, crc32} streamed directly into the library cache
```

`http.fetchToCache({url, headers?, libraryKey, name})` performs a GET, streams
up to the owner download ceiling (8 GiB by default), further bounded by the library cache quota, into the plugin's own library cache, and returns
`{blob, size, crc32, md5, sha1, status}`. SHA-256 is the blob reference.
Digests cover the stored response bytes in one pass. Compare the digest with
trusted catalogue metadata before serving a verified download. Non-2xx responses,
network failures, size overflow and quota failures return errors; they do not
publish a truncated blob. Redirects recheck the live network grant and the
existing SSRF policy. The existing content capability, network host declaration
and library cache quota apply; no new permission is needed. The operation is
synchronous and bounded by the current call's deadline; there is no background
prefetch. Cache eviction still applies to returned refs.

The per-call byte ceiling `readBytes` for `cache.read`, `archive.open`, and `archive.read`
scales with the plugin's granted memory: `readBytes = clamp(memoryBytes / 4, 8 MiB, 512 MiB)`
(default 32 MiB plugin memory → 8 MiB per call; declaring `runtime.limits.memoryBytes` of 1 GiB
grants 256 MiB per call). Plugins read their live grant via `runtime.limits`
(`ctx.ether.runtime!.limits()` in TypeScript, `BlobHost.RuntimeLimits(ctx)` in Go,
`host.runtime.limits()` in Python, or `GET /ether/plugin-host/limits`) so batch sizes never rely
on hardcoded guesses.

`cache.read(blob, {offset, length})` reads `1..readBytes` from a cached blob.
`archive.list(blob)` returns `[{index, name, size, unsafe?, unsafeReason?}]`, reading ZIP, 7z or RAR (4.x/5.x) headers
without loading all compressed bytes. `archive.open(blob, memberIndex, {offset, length})`
returns `1..readBytes` from one **uncompressed** member, while `archive.read(blob, reads)`
reads `1..4096` `{member, offset, length}` ranges in one call whose requested `length`s sum
to at most `readBytes` (returning an error rather than truncating when exceeded) and returns
`[{index, member, size, body}]` in request order (`bodyBase64` on raw JSON wire calls).
Use `archive.open` when reading a single member or stepping sequentially through one large
file across range calls. Use `archive.read` when parsing many small or medium members from the
same cached archive (such as thousands of XML, DAT, or CUE descriptors), amortizing cache
ownership checks, directory lookup, and host crossings into one call.
Offsets are nonnegative integers; a read at EOF returns empty bytes and reads crossing EOF
return the available suffix. Every call opens only the calling plugin's cache namespace and
closes every member stream and blob handle before returning.
`archive.extract(blob, memberIndex[, {libraryKey?, name?}])` streams the
uncompressed member directly into the plugin's own library blob cache (defaulting to
the source archive's library namespace) in one pass with no full in-memory buffer,
computing `{ref, size, sha1, md5, crc32}` (`ref` is `"blob:<sha256>"`). It enforces
`archiveMemberBytes` and the target library's cache quota, refuses unsafe members by
index with `UnsafeArchiveMemberError` and directories, observes cancellation and
deadlines, and charges governance units per started MiB (`ArchiveExtractUnitsPerMiB`).
The Go `RemoteHostClient` provides `RuntimeLimits`, `FetchToCache`, `CacheRead`, `ArchiveList`,
`ArchiveOpen`, `ArchiveRead`, and `ArchiveExtract` through `BlobHost`; the Python
`RemoteHostClient` and `MockHostClient` expose `runtime.limits()` and `archive.list`,
`archive.open`, `archive.read`, and `archive.extract`.

Archive sizes and member counts use owner global/per-plugin ceilings: defaults
are 8 GiB compressed archive/member, 32 GiB total expansion and 65,535 members.
Unsafe member names remain in listings with `unsafe: true` and `unsafeReason`.
Their stored names are identifiers only: opening one returns a typed
`UnsafeArchiveMemberError` (a descriptive error through hosted/remote calls),
while other members remain readable. Empty names, names over 1024 bytes, NUL,
backslashes, absolute paths (including drive-rooted paths), and traversal or
noncanonical path segments are unsafe. Colons in ordinary member names are
allowed: reads use member indexes and never write stored names to disk.
These flags are retained in the cached directory. ZIP supports Store/Deflate and ZIP64;
7z supports Copy, LZMA and LZMA2 (including solid blocks); RAR supports stored
and compressed 4.x/5.x members. Other 7z coder chains are refused, including
BCJ filters and PPMd. Split/multi-volume input, encrypted headers or members,
links and other special files are refused. Unsafe paths are listed but cannot
be opened. Directories may appear
in listings but cannot be opened. A 1000:1 expansion ceiling applies per ZIP
member and per 7z/RAR compression block. Nothing is extracted to a filesystem.

Metadata and decoder allocations are bounded by `memoryBytes` (32 MiB by
default). ZIP directories also retain the 64 MiB host ceiling. A large dictionary
or directory can be refused even when the member fits the byte grant. 7z uses
at most half the memory grant for its dictionary; RAR reserves one eighth per
decoder buffer, including PPMd and filters. Compressed input bytes read, including
metadata and decoder read-ahead, are cumulatively bounded by `fetchBytes` per
call. Expanded solid blocks and the whole declared expansion are bounded by
`archiveExpandedBytes`. Calls observe the runtime deadline and cancellation.

A range is a bound on **returned bytes**, not decompression work. Deflate reads
start at the member beginning. Solid 7z/RAR ranges decode dependencies from the
block beginning, then discard bytes through the requested offset. Calls are
stateless: sequential calls also restart decoding. RAR can additionally consume
earlier solid blocks in archive order. Prefer large sequential
chunks and avoid repeated random reads; persist verification digests in KV.
Range reads do not independently verify the full member CRC; use trusted digests
when integrity matters. Local owner-granted paths are not archive inputs: first
use a plugin-owned cached blob. Gzip files are unsupported here.
An HTTP `Content-Encoding: gzip` response may be transparently decoded by the
HTTP client; its digests then describe the decoded bytes. A `.gz` file is not
automatically decompressed by this archive service.

Direct `http.fetch` still limits the raw response to 10 MiB, with its JSON result
allowance including encoding expansion. Hosted WASM host-result envelopes also scale
automatically with the plugin's `readBytes` grant (including base64 and JSON framing
overhead, with a 4 MiB baseline floor), while hosted QuickJS binary reads (`cache.read`,
`archive.open`, `archive.read`) transfer decoded bytes directly into guest `ArrayBuffer`s.
Remember that big media never goes through these read calls — Ether streams blobs and
archive members directly to clients itself (`internal/plugins/frontend_blob.go`); reserve
`cache.read`, `archive.open`, and `archive.read` for bytes the plugin must parse.

### Owner-started multi-call work and resource requests

A page action returning `{job: "build-catalogue"}` starts a background job.
The job returns `continueInMs`, an opaque JSON `continuation`, and a `progress`
string to continue; Ether forwards the token in the next POST body. `done: true`
finishes, `failed: "reason"` fails, and an absent `continueInMs` finishes too.
Jobs use the owner's time ceiling (12h by default), a 1,000,000-call sanity bound,
a minimum 100ms pause, OS background CPU/IO priority, and cooperative pauses
between calls and host operations during playback, Live TV or high host load.
They are cancellable with visible progress. Persist checkpoints in KV for
recovery on a later owner action after restart; Ether does not resume eagerly.

`ether.runtime.limits` requests memory, time, compute, download, archive and
catalogue-item resources within owner-controlled global/per-plugin ceilings.
Defaults: 2 GiB memory ceiling (up to 4 GiB; omitted heap request stays 32 MiB),
12h, effectively unlimited background work units, 8 GiB download/member, 32 GiB
total expansion, 65,535 archive members, and 2,000,000 stored catalogue items
(`maxItems`, with no upper cap when raised by the owner). Remote plugins can
also request data and `maxItems` limits. The library cache quota further bounds
downloads. The owner
sees requested versus granted values before install and in the drawer, and can
raise or lower ceilings under Plugins → Settings or for one plugin. Saving
stops affected jobs so the new ceilings apply immediately.

Viewer search and playback calls derive their unit budget directly from each
route's deadline (`10,000 units/s`): `/stream/*` and `/ether/open*` get 3 s
(30,000 units), `/subtitles/*`, `/ether/subtitles/*` and kind `/resolve` and
`/match` get 5 s (50,000 units), and `/catalog/*`, `/meta/*`, `/ether/metadata/*`,
`/ether/markers*` and kind `/search` and `/items/*` get 10 s (100,000 units),
reported live by `ether.runtime.limits()`. Ranking budgets stay unchanged.
Background kind catalogue sync (`/browse` and `/changes`), administrative setup,
actions, tools, events and jobs use owner grants. Health/configuration/view/options/test
keep short protocol deadlines. See `sdk/AGENTS.md` and the SDK README for exact
per-route limits and how owners raise ceilings.

### Pending resolve and match replies

Both routes can return `{pending:true,retryInMs:1000,job:"/ether/job/prepare"}`.
`job` is optional. Ether clamps retry delays to 250–5000 ms, retries for up to
25 seconds total per Play, and keeps each invocation within five seconds.
Pending is neutral for health. When the 25-second budget expires while the reply is still `pending` (or when
background job slots are full), Ether returns `503` with `code: "preparing"` and
`Retry-After` set from the last `retryInMs` (defaulting to `5` seconds) across
`kind-media`, Vela, Jellyfin/MediaBrowser, and Plex API routes, and never saves
a negative resolution — whereas a real empty `sources: []` answer stays `409`
`no_source`. A job receives the original request body (including `partIndex` when
resolving a multi-part item), uses promoted deadlines and continuation replies,
and is deduplicated per plugin/kind/item (and `partIndex`). Job completion wakes
the next resolve.

For remote verification call `ether.http.hash({url,algo:"sha256",expectSize})`.
A miss returns pending while a bounded host worker streams digests without
storing bytes; the next resolve reads the cached result. See the worked example
and exact timeout, cache, cancellation and byte limits in [the authoring recipe](../sdk/AGENTS.md#slow-resolution-and-remote-verification).
