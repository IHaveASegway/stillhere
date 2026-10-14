# Blame cache

Status: deferred, open decision #3 ([../product.md](../product.md#open-decisions)): build in M1 only if the measured cold scan misses its budget · design carried over from the 2026-10-07 plan, corrected 2026-10-14

## Key

The logical key is repo + HEAD SHA + path blob SHA, plus every input that changes the stored bytes.

`opts-hash` is the SHA-256 of these length-prefixed byte strings:

1. the blame flags;
2. the repo's ignore-revs file bytes, or empty if there is none ([blame-collector.md](blame-collector.md#ignore-revs-per-repository));
3. the org mailmap bytes, or empty if there is none;
4. the repo's own `.mailmap` blob ID at the scanned SHA, or empty if there is none;
5. the header-marker list, one marker per line, UTF-8.

The 2026-10-07 design hashed only the org mailmap. If a repo's own `.mailmap` changed, cached author emails would go stale for every file the range did not touch, so the repo mailmap is now part of the hash.

The policy hash is not part of the key. Bot lists, roles and weights are applied when reducing, so editing them never forces a re-blame. The marker list is source code, not policy, so editing it must cause a miss.

## Stored facts

Under the facts → metrics → views design, these keys become table keys in the facts store. The file layout from the plan shows the same structure:

```
${XDG_CACHE_HOME:-~/.cache}/stillhere/v1/
  repos/<repo-key>/manifest.json
  blobs/<repo-key>/<blob-sha>--<opts-hash>.json
  logs/<repo-key>/<head12>--<since>--<mailmap12>.log
  maat/<repo-key>/<head12>--<since>--<mailmap12>/<analysis>.csv   # only with open decision #1 = jar
```

- **`<repo-key>`** is the first 16 hex characters of the SHA-256 of the repo's real path. The path itself is stored in the manifest, never in a directory name.
- **Blob fact:** `{path, blob, rows: [{commit_sha, email, nonblank}], header_generated}`. It holds per-commit rows, as [blame-collector.md](blame-collector.md#persisted-facts) requires; the 2026-10-07 per-author totals cannot feed durability. It never holds a role or a finished total.
- **Manifest:** the real path, the scrubbed remote, the full head SHA, and `entries` of `{path, blob}`. Never `role`: role is a policy output that would go stale when the policy changes while the blame facts would not.
- **Truncated names.** `<head12>` and `<mailmap12>` are filename conveniences only. Twelve hex characters are not an identity. Each such file has a sibling `.full` holding the full hash. If the truncated name exists and its `.full` does not match, do not overwrite it; recompute instead.
- **Safety.** The directory mode is `0700`, because the cache contains emails and is sensitive local state. `stillhere cache clear` deletes `v1/` and nothing else. Writes go to a temporary file in the same directory and are renamed into place, so a killed process never leaves a truncated fact for the next run to trust.

## Invalidation

Decide for the whole repo first, then per path:

1. **History rewritten.** If `git merge-base --is-ancestor <cached_head> <new_head>` exits non-zero, every blob fact for the repo misses. This is the amend and force-push case. `git cat-file -e <old_head>` is **not** the check: the amended-away commit stays in the object database until `git gc`, so object existence is not reachability.
2. **Fast-forward or same commit.** If the cached HEAD is an ancestor of the new one, run one `git log --name-only --pretty=format: <cached_head>..<new_head>`. Every listed path misses, including a delete and re-add that ends on the same blob, because its authors can change. One log per repo replaces one `cat-file` per cited SHA, which would not fit the warm budget at 50,000 files.
3. **Blob changed.** A path whose blob SHA differs misses, even if the log did not list it.

A path the log did not list, with the same blob SHA and the same `opts-hash`, is reused. Week logs are keyed by full HEAD, window and mailmap hash, and rebuild whenever HEAD moves. Deleting the cache is always safe, and a future `v2/` ignores `v1/`.

## Budget and tests

- **Warm-run target:** under 30 s for 100 repos and 50,000 files with unchanged blobs. That includes reading the cache and rendering; it excludes refreshing hotspots.
- **Second run:** a second scan of the fixture reports `cache_misses == 0`.
- **Amend without gc:** after amending a blamed commit away without `gc`, `git cat-file -e` of the old HEAD still exits 0, yet the next scan misses every blame fact.
- **No wall-clock assertions** in CI.
