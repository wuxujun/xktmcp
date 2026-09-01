# Wiki Backlinks Index Design

## Goal

Precompute local Wiki reverse links during index refresh so backlinks queries do not scan every document.

## Design

- Extend the local index snapshot with a `target page ID/title -> []WikiBacklink` map.
- Build the map from the existing Markdown and `[[wiki link]]` parsers while scanning documents.
- Publish the complete snapshot atomically after a successful refresh; retain the previous snapshot on refresh errors.
- Keep per-user LocalRouter indexes isolated and preserve existing link parsing semantics.
- After successful local writes, reuse the normal refresh path so backlinks become visible immediately.

## Validation

- Markdown links resolve to the same page identifiers as today.
- Wiki-link syntax is indexed identically to on-demand scanning.
- Refreshes remove deleted/changed links from the new snapshot.
- User-specific routers cannot read another user's backlinks.
- Existing HTTP Wiki behavior remains unchanged.
