# Agent prompt: dmhy-mcp + kura

Drop-in guidance for agents driving DMHY searches and Kura library decisions.
Paste relevant sections into your project's CLAUDE.md, AGENTS.md, or
task-specific system prompt. The dmhy-mcp tool descriptions already cover
keyword script preference and the "start short" heuristic; this file covers
workflow, cross-tool orchestration, and DMHY title parsing.

---

## Tool surface

**dmhy-mcp:**
- `search_releases` — keyword + category filter. At least one filter required.
- `get_recent` — latest releases, optional category filter.
- `get_magnets` — resolve a list of `info_hash` values (from prior search
  results) into magnet URIs. Call this only for the releases you intend to
  download.

Search / recent results return: `category`, `title`, `info_hash`,
`pub_date`. **Magnets are intentionally omitted from search output**
(tracker lists make them large and noisy); fetch them via `get_magnets`
when the agent has narrowed down which releases to grab. Returned magnets
are pre-pruned of dead trackers via a background probe cache.

Server-side filtered to anime / anime_season categories only. Group / team
is **not** a separate field — read it from the bracketed prefix in `title`
(`[ANi]`, `[LoliHouse]`, `【喵萌奶茶屋】`). Quality info (resolution, source,
codec) lives in the title too.

`category` enum: `anime` (general anime releases) or `anime_season` (full
season batch / cours-complete packs).

**kura (library):**
- `kura_resolve` — text or `tvdb:<id>` → canonical series ref + titles.
- `kura_show` — ref → per-episode state (codec, resolution, source on disk).
- `kura_list` — series-level rollup. Not enough for codec-level upgrade
  decisions; use `kura_show` for that.

**memory (`@modelcontextprotocol/server-memory`, knowledge graph):**
- `create_entities` / `add_observations` — persist learned facts about
  a series (working DMHY keywords, preferred groups, codec choices).
- `open_nodes` / `search_nodes` — retrieve cached facts before doing
  expensive work.
- `delete_entities` / `delete_observations` — prune stale entries.

The memory server runs alongside dmhy-mcp + kura when configured in
`.mcp.json`. Use it as a write-through cache for anything an agent
discovers that would be expensive to rediscover next run.

### Memory schema (DMHY)

For each series, create one entity keyed by the canonical metadata ref:

- **Entity name:** the metadata ref string, e.g. `tvdb:295068`.
- **Entity type:** `dmhy_series`.

Observations are short strings, prefixed by category. **Memory caches
how to find a show, not what's currently out for it.** Anything that
ages in hours/days does not belong here — write it to the run output
instead. Schema is split into three categories:

**Durable (write freely):**
- `keyword:<fragment>` — verified-working DMHY search fragment.
- `abbreviation:<fragment>` — group-invented short form harvested from
  verified-matching result titles (e.g. `abbreviation:Kuranika`).
- `preferred_team:<group>` — team the user explicitly preferred for
  this show. Only when stated by the user.
- `numbering_convention:<absolute|per_season|both>` — how groups label
  episodes for this series.
- `episode_offset:S<n>=<abs>` — first absolute episode number of a
  season when groups use absolute numbering (`episode_offset:S4=67`
  for Pokemon's "S4 E1 = abs 67").
- `note:<durable text>` — caveats that don't age (`two-script split`,
  `joint groups use romaji`, `kanokari fragment dead after S1`).

**Diagnostic (write rarely, prune liberally):**
- `triage_empty_at:<RFC3339>` — DMHY had zero coverage at the named
  time. Useful so the next run doesn't repeat the same dead-end. The
  next agent should still re-attempt periodically; a single empty
  triage isn't permanent. Keep at most one per entity.
- `last_seen_at:<RFC3339>` — write-time stamp on any successful triage.
  One per entity, replace on update. Used for prune diagnostics, not
  as a freshness gate (durable observations don't expire).

**Forbidden (never write):**
- "S<n> E<x>-<y> available", "E<x> on DMHY", "released" / "airing" /
  "as of" / "recent" / "latest" — episode availability ages in days.
- Specific batch / season-pack availability ("S2 batch released
  2025-10-01"). True today, misleading next month.
- Date-relative claims of any kind ("currently airing", "this cours").
- Per-run survey results, pick lists, magnet hashes — those belong in
  the run output the user sees, not in cross-session memory.

If the volatile-vs-durable line is unclear: ask "would this still be
true if I read it 6 months from now without rechecking?" If no, do
not write it.

Treat observations as append-only inside a session; reconcile / prune
between sessions if needed.

---

## Before you start: delegation rules

Read this before any DMHY or Kura workflow. These rules override
convenience heuristics that say "I'll just call the tool and see."

The pattern is the same in both cases below: filter periphery noise
inside a subagent so the main thread holds only the curated payload it
actually needs to reason about.

### Required: delegate keyword triage to a subagent

If you do not already know a working DMHY search fragment for the show,
**delegate keyword triage to a subagent.** Do not run the "try romaji →
fall back to CJK → harvest abbreviations" loop in the main thread. See
[`docs/keyword-triage-subagent.md`](keyword-triage-subagent.md) for the
paste-ready subagent brief.

**This is required, not recommended.** Skipping it costs context every
time it fails: 3–5 follow-up queries per show that misses, plus all
their result-page titles, stay in context for the rest of the session.
On a multi-show sweep that compounds fast — the survey output you
actually wanted ends up squeezed against a context wall built from
failed triage attempts.

**Skip delegation only when:**
- The user named exact search terms (`"search for LoliHouse"`,
  `"keyword: kanokari"`). Nothing to triage.
- You already have a verified-working fragment from earlier in the
  current session for this exact show — and you can name where it
  was verified.
- The memory MCP server has cached `keyword:*` observations on the
  series entity (e.g. `tvdb:295068`). Durable observations don't
  expire — keywords for a show stay valid until DMHY group conventions
  shift, which is rare. Use cached keywords directly without
  re-verifying. The triage subagent also consults memory as its
  first step — for batches, pre-load with `open_nodes` against all
  refs upfront and only delegate triage for the misses.

  Exception: if the entity has a `triage_empty_at:<date>` observation
  but no `keyword:*` observations, that's a prior dead-end. Re-attempt
  triage if the date is older than ~30 days; the show may have gained
  coverage since.

**Do not skip on perceived familiarity with the show.** DMHY title
encoding regularly diverges from expected romanizations and from the
canonical English / official-romaji forms. "I know this show, the
keyword should be X" is the failure mode. *Familiarity is not a
substitute for triage.* If you are tempted to skip because the show
seems well-known, that is precisely the case where delegation pays the
most — well-known shows have the most diverse group coverage and the
most opportunities for the title encoding to surprise you.

**For batches of 3+ shows: delegate per show, in parallel.** Inline
triage on a 12-show sweep can burn tens of KB of result titles in main
context. Subagents return ~200B of working keywords each.

### Required: delegate library-state inspection for batches

If you are inspecting Kura library state for **3+ series**, or for any
long-running show that is likely to dump tens of S0 specials or
truncated episode lists into context (Pokemon, One Piece, Gintama,
Bleach, Detective Conan, Doraemon), **delegate to a library-state
subagent.** It runs the `kura_resolve` + `kura_show` calls in its own
context and returns a scope-filtered gap summary. See
[`docs/library-state-subagent.md`](library-state-subagent.md) for the
brief.

**This is required for batches.** Inline `kura_show × 12` typically
dumps 30–60KB of per-episode codec / source detail and S0 special
metadata into the main context — most of which the main agent
discards once it identifies the missing-episode list. The subagent
returns ~1–2KB of just-the-gaps.

**Pass the scope you actually care about** (`current_season`,
`all_missing`, `upgrade_candidates`). The subagent uses scope to drop
out-of-scope episodes before returning. Do not delegate without a
scope and then ask "for everything" — that defeats the filter.

**Skip delegation only when:**
- You are inspecting a single series and need the full per-episode
  dump (e.g. building a detailed report for that one show).
- The series is short (one cours, no S0) and the full dump is small
  enough that filtering wouldn't save meaningful context.

---

## Workflow A: Triage a DMHY release for the library

Given a DMHY release (one or many), decide whether it is useful.

1. **Resolve.** Call `kura_resolve` with a distinctive fragment from the
   release title. Run resolves in parallel when triaging a batch. Series
   identity is not derivable from the title string alone — never skip this
   step.

2. **Inspect library state.** For a single release, call `kura_show`
   directly with the resolved ref to get per-episode codec, resolution,
   and source on disk. **For a batch of 3+ releases, delegate to a
   library-state subagent** (see "Before you start") with
   `scope: upgrade_candidates` plus the user's `target` codec / source —
   it runs the `kura_show` calls in parallel and returns only the
   episodes where an upgrade is justified, dropping the noisy
   already-at-target majority. A series may have mixed codecs across
   episodes (some already upgraded, some not), so episode-level
   inspection matters either way.

3. **Decide.** Compare title attributes (parsed via Title anatomy below)
   against the library state, applying the user's stated preferences:
   - **Series not tracked** (`kura_resolve` errors or `kura_show` reports
     "not indexed"): not an error — distinct decision (add vs. skip).
   - **Episode missing**: download candidate.
   - **Episode present but at lower codec / source / resolution**: upgrade
     candidate.
   - **Episode present at equal or higher quality**: skip silently.

   If the user did not state preferences, ask before proceeding. Example
   hierarchy: AV1 > x265/HEVC > AVC (codec); BDRip > WebRip/WEB-DL > raw
   (source). Do not infer.

4. **Fetch magnets for the keepers.** Once a release is classified as
   download or upgrade, call `get_magnets` with its `info_hash` (batch
   multiple keepers in one call). Search results never carry magnets, so
   this step is required before handing anything off to a torrent client.

**Notes:**
- Groups often publish codec pairs (x265 + AV1) of the same episode.
  Evaluate only the user's preferred variant; skip the other silently.
- If the library already has the target quality for all available episodes,
  say so and move on. Do not burn tool calls verifying confirmed state.

---

## Workflow B: Find the best release for a library show

Given a show the user wants more of, locate releases on DMHY and pick.

1. **Resolve.** Call `kura_resolve` on the user's reference (text or
   `tvdb:<id>`) to get canonical titles in multiple scripts. The resolved
   titles are your search-fragment source — more reliable than guessing
   romanizations or transliterations.

2. **Inspect library state.** For a single show, call `kura_show`
   directly with the resolved ref. **For 3+ shows, or any long-running
   show with heavy S0 specials (Pokemon, One Piece, Gintama, etc.),
   delegate to a library-state subagent** (see "Before you start") with
   `scope: current_season` (or `all_missing` if the user wants the full
   backlog). It runs the `kura_show` calls in parallel and returns only
   the gap summary — entire seasons missing, individual episodes
   missing, or upgrade candidates if scope is `upgrade_candidates`.

3. **Search DMHY.** Order of operations:

   1. **Memory cache lookup.** For batches, call `open_nodes` on the
      memory MCP server with all the metadata refs at once. For each
      hit with fresh `verified_at` (< 90 days) and at least one
      `keyword:*` observation, use the cached keywords directly and
      skip triage for that show.

   2. **Triage the misses.** For shows with no cached keywords (or
      stale ones), **delegate keyword triage to a subagent** — this
      is required (see "Before you start" above and
      [`docs/keyword-triage-subagent.md`](keyword-triage-subagent.md)).
      Pass the show + `kura_resolve` canonical titles; the subagent
      consults memory itself, runs triage if needed, persists the
      result to memory, and returns the working keyword set.

   3. **Search.** Run one or two clean `search_releases` calls in
      the main thread with the resolved keywords (cached + triaged).
      Apply the **Search strategy** below for any refinement on the
      resulting clean result set.

4. **Match to gap shape.**
   - **Entire season missing**: prefer `category: anime_season` first.
     Batch / cours-complete packs cover the gap in one torrent and are
     usually higher quality than per-episode WEB-DL.
   - **Single episodes missing**: prefer `category: anime` and filter
     post-hoc to the specific episode numbers needed.
   - **Mixed (some episodes present, batch wanted for the rest)**: weigh
     batch vs. per-episode — batch may overlap what's already on disk and
     create dedup work. Mention the tradeoff to the user.

5. **Pick the team last.** Unless the user explicitly named a team, do not
   bias the search early; survey the landscape first. When choosing:
   1. Releases the user has previously expressed preference for (carry
      forward from earlier in the conversation or from project-level
      preferences).
   2. Batch / season-complete over one-off episodes when filling a gap.
   3. Higher resolution + native source (BDRip > WebRip/WEB-DL > raw).
   4. More recent `pub_date` over older.

6. **Fetch magnets only for the picks.** Call `get_magnets` with the
   `info_hash` values of the chosen release(s). Do not call it during the
   survey — search results omit magnets specifically because per-release
   magnets are large and the agent doesn't need them to triage. If
   `get_magnets` returns an `info_hash` in `missing`, the cache evicted it
   (server restart or LRU pressure). Re-run the search that surfaced it.

**Notes:**
- If the user said "give me ANi's release of X" or "I want the LoliHouse
  version", filter on the team **upfront** by including a fragment of the
  team name in the keyword (e.g. `keyword: "LoliHouse 葬送"`). Do not
  survey first — they already narrowed the landscape for you.
- If the user asks "what's out for show X" without a download intent, skip
  step 5; just summarize what's available across teams + resolutions and
  let the user pick.
- Joint groups sometimes use a different script for the show title than
  single-group releases. E.g. `[SweetSub&LoliHouse]` used the romaji
  `Honzuki` while a CJK-only query (`本好き`) missed it entirely. Cross-check
  with a romaji fragment when a team-name search comes up empty.

---

## Search strategy

When the user asks for a specific show:

1. **Pick a short, unique keyword fragment. Always start with romanized
   Japanese.** Romaji is the most permissive — many groups embed romaji
   titles even on CJK-primary releases. Only fall back to Traditional
   Chinese or Japanese kana/kanji if you have explicit evidence the show's
   releases skip romaji. Simplified Chinese and English titles almost never
   match verbatim and should not be your first attempt.
   Examples: `tenshi` for *The Angel Next Door*, `Honzuki` for *Ascendance
   of a Bookworm*, `kanokari` for *Rent-a-Girlfriend*, `nonbiri` for
   *Farming Life in Another World*. If you don't know the romaji, run
   `kura_resolve` first (Workflow B step 1) to get the canonical romaji
   title rather than guessing CJK.

2. **Run the broad query first.** Do not pre-filter by team, resolution,
   or codec on the first call. The point of the first call is to learn
   what the release landscape looks like.

3. **Always run multiple queries.** One query is never enough. Different
   groups use different scripts and abbreviations — a CJK query may miss
   groups that use romaji, and vice versa. Run at least 2–3 fragment
   combinations (romaji fragment, then CJK fragment, then any abbreviated
   title visible in earlier results) before summarizing.

4. **Inspect the returned titles.** They concatenate show name, episode,
   group tag, resolution, source, and codec in unpredictable order and
   script. Learn from them:
   - which groups release this show
   - which episodes / seasons are available
   - which resolutions and sources are common
   - what abbreviated title the groups use (e.g. `Kuranika` for
     クラスで２番目に可愛い…)

5. **Refine only after surveying.** Add narrower keyword fragments based
   on what you saw — a team name fragment, a specific episode number, a
   resolution tag. Do not invent fragments that weren't in the survey
   output.

---

## Title anatomy

Understanding title structure helps both parsing and search-fragment
selection.

**Canonical structure** (typical order, not strictly enforced):

```
[Group tag] Show name (CHS / CHT / JP / Romaji) - EP# [quality][sub info]
```

The group tag is always leading and bracketed.

### Group tag

Two bracket styles coexist:
- `【Group】` — fullwidth, used by many fansub groups (喵萌奶茶屋, 今晚月色真美)
- `[Group]` — ASCII, used by encode/raw groups (LoliHouse, ANi, 猎户压制部)

Joint credits use `&`: `[澄空学园&动漫国字幕组&LoliHouse]`,
`[SweetSub&LoliHouse]`. Reposts prepend `[搬運]` before the original group
tag. Season teasers appear inside the group block:
`【喵萌奶茶屋】★04月新番★`.

### Show name scripts

Titles commonly carry the same show name in multiple scripts:

- CHS / CHT only — `和班上第二可爱的女孩子成了朋友 / 我和班上第二可愛的女生成為朋友`
- CHS + romaji — most common for LoliHouse, 猎户压制部
- CHS / CHT / JP / romaji — four-script titles common in LoliHouse:
  `复制品也要谈恋爱。 / 複製品的我也會談戀愛。 / レプリカだって、恋をする。 / Replica datte, Koi wo Suru.`
- CHT + English only — ANi (Baha source): `SNOWBALL EARTH / 凍結地球`

CHS uses `爱`, CHT uses `愛` — same show, different character sets. CHS
and CHT releases are separate torrents from separate groups.

Groups sometimes invent short romaji abbreviations not derived from any
official title: `Kuranika` (クラスで２番目に可愛い…), `Ponsuka`, `LasTame S2`.
Only visible in the feed.

Some groups append an explicit search alias at the end:
`[检索：魔法姐妹露露特莉莉]` — useful as alternate search keys.

### Episode notation

| Format | Example | Notes |
|---|---|---|
| Bare number | `- 05` or `[05]` | Most common |
| Season+absolute | `[05(77)]` | Season ep 5, overall ep 77 |
| Written-out total | `[04 - 总第70]` | "Overall 70th" in Chinese |
| Standard | `S01E06` | Used by non-fansub / Jellyfin-friendly releases |
| Kanji | `第04話` / `第06集` | Less common |
| Batch | `[01-12 合集]` | Season collection |

Season labels: `第四季` (Chinese), `S4`, `4th Season`, `Season 2`.

### Quality tags

**Resolution:** `1080p` / `1080P`, `2160p`, `720p`, `480p`. Some groups
use pixel dimensions: `1920x1080`, `3840x2160`.

**Source:** `WebRip`, `WEB-DL`, `BDRip` — most common. Platform sources
embedded directly: `Baha` (Bahamut), `IQIYI`, `ViuTV`, `CR` (Crunchyroll),
`iCABLE`, `Ani-One`, `TVB`, `HOY`. Casing inconsistent (`WebRip` vs
`WEBrip` vs `WEB-RIP`).

Source ranking for upgrade / selection: BDRip > WebRip / WEB-DL > raw.
Platform WEB-DL (Baha, IQIYI, CR) generally beats unspecified WebRip.

**Codec:** `HEVC` / `HEVC-10bit` / `x265`, `AVC` / `x264`, `AV1`. Audio:
`AAC`, `FLAC`, `DTS`, `OPUS`. Container only when not MKV: `[MP4]`.

### Subtitle info

| Tag | Meaning |
|---|---|
| `简繁内封` / `简繁内封字幕` | CHS + CHT soft-muxed internally |
| `简繁日内封字幕` | CHS + CHT + Japanese trilingual soft mux |
| `简日双语` / `繁日雙語` | Bilingual CHS+JP or CHT+JP |
| `内嵌` / `内嵌字幕` | Hardcoded / burned-in subtitles |
| `外挂` | External subtitle file (separate download) |
| `外挂AI中字` | AI-generated external Chinese sub |
| `CHS`, `CHT`, `CHS&CHT&JP` | Short codes (ANi and similar) |
| `无中字` | No Chinese subtitles (raw) |
| `粵語+內封繁體中文字幕` | Cantonese audio + CHT soft subs |
| `YUE` | ISO tag for Yue/Cantonese audio |

Internal soft-muxed (`内封`) is preferable to hardcoded (`内嵌`) for
flexibility. `外挂` requires separate subtitle handling.

### Anomalies

- `Re꞉` — modified colon (U+A789) used to dodge filesystem restrictions;
  search fragment should use plain `Re` not `Re:`.
- Leading invisible / non-breaking space before some group brackets —
  harmless for search but don't copy-paste literally.
- Non-standard language codes: `GB_CN`, `YUE`.
- Korean Hangul in archival repacks (rare).
- Titles with no episode number — usually specials, OVAs, or movies.

---

## Failure modes to avoid

- **Don't fabricate.** If the search returns nothing, say so and ask for a
  different fragment. Don't invent magnet links, info hashes, or titles.
- **Don't loop on near-identical queries.** If `tenshi` returned nothing,
  trying `Tenshi` or `tenshi-sama` won't help. Switch script (try CJK) or
  switch fragment (try a romaji of a different word in the title).
- **Don't lead with CJK.** Romaji is the broader net. Start there. Falling
  back to CJK after a romaji miss is the correct order, not the reverse.
- **Don't guess CJK without `kura_resolve`.** If you don't know the show's
  romaji, resolve first to get canonical titles instead of guessing CJK
  fragments. Guessing burns tool calls on near-misses.
- **Don't run inline keyword triage.** Delegate to a triage subagent (see
  "Before you start"). The "I'll just try one or two keywords inline"
  shortcut is the most common context-bloat trap. If you find yourself
  running a third `search_releases` call with a different keyword for the
  same show without delegating, you have already lost.
- **Don't run `kura_show` × N inline for batches.** Delegate to a
  library-state subagent (see "Before you start"). Long-running shows
  (Pokemon, One Piece, Gintama, Detective Conan, Doraemon) dump
  truncated S0 specials and per-episode codec detail that bury the
  gap summary you actually wanted. Same trap, library side instead of
  search side.
- **Don't write volatile facts to memory.** Episode availability ("S4
  E01–04 on DMHY", "S2 batch released 2025-10-01"), "as of" snapshots,
  airing-season notes, and per-run survey results are forbidden in
  memory observations. They age in days and mislead future runs. Cache
  *how to find a show* (keywords, abbreviations, numbering, durable
  caveats), not *what's currently out for it*. The 6-month test: if
  the observation wouldn't still be true unverified six months from
  now, do not write it.
- **Don't stop at one query.** Even when the first query returns results,
  run additional fragments in other scripts to catch groups you'd miss.
- **Don't stack filters on the first call.** `keyword + category +
  specific resolution` on call one is almost always too narrow.
- **Don't skip `kura_resolve`.** Series identity is not derivable from a
  title string. Triage and search workflows both depend on the canonical
  ref.
- **Don't confuse "not indexed" with errors.** `kura_show` reporting "not
  indexed" means the series is not tracked — distinct from a tool failure.
- **Paginate via `offset`, not by raising `limit`.** Per-page `limit` is
  capped at 100 to fit MCP output budgets. When `has_more` is true, call
  again with `offset += limit`. Dedup across pages by `info_hash` — order
  may shift slightly between calls if upstream churns mid-paginate.
- **Don't call `get_magnets` during survey.** Magnets are large; fetching
  every result wastes the MCP output budget. Survey first with the lean
  search results, narrow to the keepers, then batch a single `get_magnets`
  call for those `info_hash` values.
- **`get_magnets` cache misses are not errors.** The server keeps an
  in-memory LRU of magnets; on restart or LRU eviction, `info_hash` values
  return in the `missing` list. Re-run the search that surfaced the hash
  to repopulate, then retry `get_magnets`.
