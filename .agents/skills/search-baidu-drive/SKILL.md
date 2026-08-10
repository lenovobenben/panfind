---
name: search-baidu-drive
description: "Explore, analyze, and locate anything in the user's own 百度网盘/Baidu Netdisk from an unrestricted natural-language search goal. Use PanFind as a fast read-only POSIX metadata namespace, but do not limit the investigation to filenames or metadata: iterate freely and combine public web research, calculations, temporary scripts, and other available user-authorized download or content-analysis tools when useful. Return conclusions, evidence, uncertainty, and exact cloud locations. PanFind itself must remain read-only and must not share, upload, move, rename, or delete files."
---

# Search Baidu Drive

Treat the user's request as an investigation goal, not as a command to translate into one filename query. Use PanFind as one reliable source of evidence about the user's file world. Freely choose other available tools when metadata alone cannot answer the question.

## Operating principles

- Accept any natural-language search target, including descriptions based on meaning, scenes, images, structure, external facts, approximate memories, or file contents.
- Use as many bounded PanFind queries as needed. Start broad when names or locations are uncertain, inspect the result structure, and change strategy as evidence appears.
- Do not stop merely because PanFind lacks a field or cannot read content. Consider public web research, local calculations, temporary scripts, download tools, document parsers, OCR, media inspection, archive tools, or format-specific analysis when available and in scope.
- Use PanFind to reduce the candidate space before expensive downloads or content inspection. For large candidates, prefer hashes, metadata, directory structure, public databases, Range reads, or fixed-format headers when possible.
- Distinguish physical files from logical resources. Group episodes, multipart archives, alternate versions, and related directory contents when that better matches the user's question.
- Make uncertainty explicit. Return ranked candidates and missing evidence when a unique identification is not possible.
- Treat `baidu:/` paths as authoritative locations and keep private paths inside the current conversation.

## Use PanFind

Use the helper for the current platform. Release executable names are part of the Skill contract:

- Windows amd64: `panfind-windows-amd64.exe`;
- macOS Intel: `panfind-macos-amd64`;
- macOS Apple Silicon: `panfind-macos-arm64`.

The helpers search the repository root and `dist/` for the matching name, then fall back to a locally built `panfind` or `panfind.exe` and finally `PATH`. Do not guess a different release name.

On Windows, prefer `search.ps1` for ordinary metadata queries because it handles argument quoting, JSONL parsing, aggregate size, bounded display, and location links:

```powershell
& '.\.agents\skills\search-baidu-drive\scripts\search.ps1' `
  -Root 'baidu:/' `
  -Extensions @('mkv', 'mp4') `
  -LargerThan '800M' `
  -SmallerThan '2G' `
  -Limit 20
```

On macOS, invoke PanFind through the architecture-aware launcher and consume its JSON Lines output. The launcher does not aggregate or truncate results, so use bounded queries and calculate summaries from the complete JSONL stream when needed:

```sh
./.agents/skills/search-baidu-drive/scripts/panfind.sh \
  query 'baidu:/' -type f \( -iname '*.mkv' -o -iname '*.mp4' \) \
  -size +800M -size -2G --json
```

Set `PANFIND_PATH` for the macOS launcher, or `-PanFindPath` for the Windows helper, only when normal executable discovery fails.

The Windows helper emits one JSON object with `total`, `matched_size_bytes`, `matched_size_human`, `returned`, `truncated`, `query`, and `results`. `matched_size_bytes` covers the entire match set even when `results` is truncated. Calculate from byte values, not formatted strings. The macOS launcher preserves PanFind's native JSON Lines output instead.

Each returned result preserves the provider hash as `hash` when available. Treat a missing `hash` as unavailable metadata, not as an empty or matching hash.

Use direct PanFind commands when the helper cannot express a necessary supported query. Inspect `capabilities --json`, `schema --json`, and `explain ... --json` instead of guessing field availability or query syntax.

## Windows helper parameters

- Set the search root with `-Root 'baidu:/path'`.
- Select `-Kind file|directory|any`.
- Require substrings with `-NameContains` or `-PathContains`.
- Match alternatives with `-NameAny`, `-NamePattern`, or `-Extensions`.
- Bound size with `-LargerThan` and `-SmallerThan` using integer units `c`, `w`, `b`, `k`, `M`, or `G`.
- Bound modification time with `-ModifiedAfter` and `-ModifiedBefore` using `YYYY-MM-DD`.
- Restrict traversal with `-MinDepth` and `-MaxDepth`.
- Select an account with `-Account` only when multiple discovered accounts require it.
- Keep `-Limit` between 1 and 200. Narrow or partition a query when the returned candidates are insufficient.
- Use `-CaseSensitive` only when explicitly required.
- Set `-PanFindPath` only when normal executable discovery fails.

Different parameter groups combine with AND. `NameAny`, `NamePattern`, and `Extensions` combine their values with OR. `NameContains` and `PathContains` require every value.

Directory nodes have size zero. Query descendant files and sum their bytes when the user asks for the capacity of a directory or logical collection. State grouping and deduplication assumptions.

## Continue beyond metadata

PanFind currently queries metadata and does not itself download or open cloud files. This is a boundary of the PanFind executable, not a restriction on the Agent.

When another available tool can safely retrieve user-owned content:

- download or read small, relevant candidates when doing so is a reasonable search step;
- estimate cost and avoid blind full downloads of large collections;
- use the least expensive evidence that can resolve the question;
- inspect PDFs, text, images, subtitles, media metadata, keyframes, archives, ROMs, or ISOs with appropriate tools;
- follow the separate tool's permission and safety requirements.

If no retrieval mechanism is available, return the best metadata-based shortlist and explain what content check would distinguish the candidates.

## Report results

Lead with the answer at the user's semantic level. Include:

- the interpreted scope;
- logical results rather than an unprocessed file dump;
- counts and exact aggregate sizes when requested;
- evidence used for semantic identification;
- uncertainty or competing candidates;
- full `baidu:/` paths.

Render non-null `web_url` values as `[打开所在目录](https://pan.baidu.com/...)`. The URL is an undocumented convenience route. For files it opens the containing directory and does not select the file; name the exact file to choose. If routing fails, fall back to `parent_path` plus `name` and do not bypass browser security restrictions.

## Safety

- Keep PanFind operations read-only: discovery, status, capabilities, schema, explain, query, or watch.
- Do not use PanFind to share, upload, move, rename, delete, or call private mutation APIs.
- Treat content retrieval as a separate read operation governed by the available tool and the user's scope. Do not turn a search request into destructive cleanup.
- Do not send private full paths or unrelated filenames to public web services. Search with derived titles, hashes, or minimal clues when external research is needed.
- Do not launch a browser or desktop client merely to open a result unless the user asks.
