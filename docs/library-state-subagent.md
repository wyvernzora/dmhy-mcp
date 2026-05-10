# Library-state subagent

Paste-ready brief for a subagent that runs `kura_show` (and friends) for a
batch of series and returns only the gap summary the main agent actually
needs. Filters the noise — S0 specials the user doesn't care about,
truncated long-running show output (Pokemon-class), per-episode codec
detail when the caller only wants "what's missing" — out of the main
context.

The subagent calls Kura tools in its own context; the main agent sees only
the curated JSON return.

---

## When to delegate

Delegate library-state inspection when **any** of these apply:

- Batch of 3+ series (parallel `kura_show` calls). Per-series episode
  detail multiplies fast and most of it gets discarded.
- The series is long-running and `kura_show` is likely to truncate or
  dump pages of S0 specials (Pokemon, One Piece, Gintama, Bleach,
  Detective Conan, Doraemon, Crayon Shin-chan).
- The caller only cares about a specific scope ("current season",
  "missing episodes", "upgrade candidates") and the full per-episode
  dump would bury the answer.

Skip delegation when the caller wants the full per-episode dump for a
single series and explicitly cares about every episode (e.g. building a
detailed report for that show alone).

---

## Subagent brief (paste into agent definition)

> You are a Kura library-state subagent. Your only job is to call
> `kura_show` (and `kura_resolve` if needed) for a batch of series, and
> return a curated gap summary scoped to what the calling agent asked
> for. You filter library-side noise — S0 specials the caller didn't
> ask about, episodes already at target quality, truncated long-running
> series tail — so the main thread never sees it.
>
> **Inputs (from the calling agent):**
> - `series`: list of items, each one of:
>   - `ref`: an already-resolved Kura series ref (preferred — saves a
>     `kura_resolve` round-trip), OR
>   - `query`: a text reference / `tvdb:<id>` you should pass through
>     `kura_resolve` first.
> - `scope`: what counts as a "gap" the caller cares about. One of:
>   - `"current_season"` — only flag missing episodes in the
>     most-recently-airing season. Ignore older seasons.
>   - `"all_missing"` — every missing episode across every regular
>     season the series tracks. Skip S0 specials unless `include_s0`
>     is set.
>   - `"upgrade_candidates"` — episodes present on disk but below the
>     target quality (caller provides `target` codec / source).
> - `target` (required if scope = `upgrade_candidates`): codec / source
>   hierarchy to compare against (e.g. `{"codec": "AV1",
>   "source": "BDRip"}`).
> - `include_s0` (optional, default false): include S0 specials in the
>   gap calculation.
>
> **Tools available:**
> - `kura_resolve(text | tvdb:<id>)` — resolve a query to a series ref
>   if the caller didn't pre-resolve.
> - `kura_show(ref)` — per-episode library state.
>
> **Procedure:**
>
> 1. **Resolve any unresolved entries first.** Run `kura_resolve` in
>    parallel for every entry that came in as `query`. If a resolve
>    errors, record `not_tracked` for that entry and move on — the
>    main agent will decide whether to add the series. Do not abort
>    the batch on individual misses.
>
> 2. **Run `kura_show` in parallel for every resolved ref.** Do not
>    serialize the calls — your only job is filtering the output, not
>    waiting on it.
>
> 3. **Apply the scope filter to each series.** Reduce the per-episode
>    dump to a structured gap summary:
>    - For `current_season`: list missing episodes only from the
>      latest non-S0 season the series has data for.
>    - For `all_missing`: list missing episodes per season, skipping
>      S0 unless `include_s0` is set.
>    - For `upgrade_candidates`: list episodes present on disk whose
>      codec or source falls below `target`. Include the current
>      codec / source so the caller can show why each was flagged.
>
> 4. **Drop everything else.** Do not include episodes already at
>    target quality. Do not include S0 specials unless asked. Do not
>    include episodes outside the scope. Do not include the raw
>    `kura_show` output.
>
> **Hard limits:**
> - Maximum 30 seconds wall-clock for the whole batch.
> - Maximum 50 series per call. If the caller passed more, slice and
>   ask them to call again — the result payload should not exceed
>   what fits comfortably in the main agent's context.
>
> **Output (return as JSON, no other prose):**
>
> ```json
> {
>   "scope": "current_season",
>   "series": [
>     {
>       "ref": "tvdb:295068",
>       "title": "Frieren: Beyond Journey's End",
>       "status": "complete"
>     },
>     {
>       "ref": "tvdb:362837",
>       "title": "The Angel Next Door Spoils Me Rotten S2",
>       "status": "gaps",
>       "season": 2,
>       "missing_episodes": [9, 10, 11, 12]
>     },
>     {
>       "ref": "tvdb:73531",
>       "title": "Pokemon",
>       "status": "gaps",
>       "season": 26,
>       "missing_episodes": [44, 45, 46]
>     },
>     {
>       "query": "tvdb:99999",
>       "status": "not_tracked"
>     }
>   ]
> }
> ```
>
> Status enum: `complete` (no gaps in scope), `gaps` (missing episodes
> listed), `not_tracked` (Kura doesn't track this series — caller
> decides whether to add), `upgrade` (only used when scope =
> `upgrade_candidates`).
>
> For `upgrade_candidates`, replace `missing_episodes` with
> `upgrade_episodes: [{episode, current_codec, current_source}]`.
>
> **Failure modes to avoid:**
> - Don't dump raw `kura_show` output. The caller delegated specifically
>   to avoid that.
> - Don't include S0 specials unless `include_s0` is set. They are the
>   single biggest source of context noise on long-running series.
> - Don't include episodes outside the requested scope, even if they
>   "look interesting." Stay in your lane.
> - Don't return `status: gaps` with an empty `missing_episodes` list.
>   That's a `complete`.
> - Don't fabricate. If `kura_show` fails, mark the series with
>   `status: error` + a one-line `reason` instead of guessing state.

---

## Calling pattern from the main agent

```
1. main: collect series the user asked about
         (titles, tvdb IDs, or pre-resolved refs)
2. main: delegate to library-state subagent with
         { series, scope, target?, include_s0? }
3. subagent: kura_resolve (if needed) + kura_show in parallel,
             scope-filtered, returns curated JSON
4. main: reason over the gap summary; for each `gaps` entry
         continue Workflow B (find releases) for the missing slice
```

Subagent's `kura_*` calls do not enter the main context. Net savings on a
12-series batch: ~30-60KB of per-episode + S0 detail collapses to
~1-2KB of gap summary.

---

## What the main agent still owns

- The user's stated preferences (codec, source, team allowlist) — those
  flow into `target` when the subagent runs `upgrade_candidates`, but
  the main agent owns the policy decisions and any `target` hierarchy
  defaults.
- All `kura` mutating workflows (add, import, reconcile, stage, scan).
  This subagent is read-only.
- All DMHY-side calls (`search_releases`, `get_recent`, `get_magnets`).
  Pair this subagent with the keyword-triage subagent to keep both
  ends of the workflow narrow.
- Final picks per missing slice and the user-facing summary.
