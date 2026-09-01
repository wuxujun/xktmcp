# Wiki Backlinks Index Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Precompute local Wiki reverse links during refresh and serve backlinks from the published index.

**Architecture:** Extend the local index snapshot with a backlinks map built from existing link parsers. Publish snapshots atomically after successful scans; local routers keep independent snapshots.

**Tech Stack:** Go 1.25, existing local Wiki index and `testing` package.

**Spec:** `docs/superpowers/specs/2026-09-01-wiki-backlinks-index-design.md`

## Global Constraints

- Preserve Markdown and `[[wiki link]]` parsing semantics.
- Keep per-user indexes isolated.
- Retain the previous snapshot when refresh fails.

### Task 1: Extend the local index snapshot

**Files:**
- Modify: `internal/wiki/local_search.go`
- Test: `internal/wiki/local_search_test.go`

- [ ] Add a failing test that indexes a page linking to another page and asserts backlinks are returned.
- [ ] Run `go test ./internal/wiki -run Backlink -count=1` and confirm failure because the index does not provide the map.
- [ ] Add a backlinks map to the snapshot and populate it during the existing document scan using the current link parser.
- [ ] Change backlinks lookup to read the map while preserving current response fields.
- [ ] Run the focused test and then `go test ./internal/wiki`.
- [ ] Commit with `fix: index local wiki backlinks`.

### Task 2: Verify refresh replacement and router isolation

**Files:**
- Modify: `internal/wiki/local_router_test.go`
- Test: `internal/wiki/local_search_test.go`

- [ ] Add tests proving changed/deleted links disappear after refresh and one user router cannot see another user's backlinks.
- [ ] Run the new tests first and confirm they fail against the old scan behavior or missing snapshot updates.
- [ ] Ensure refresh publishes the complete snapshot only after a successful scan and rerun the tests.
- [ ] Run `go test ./...`, `go test -race ./internal/wiki`, `go vet ./...`, and `git diff --check`.
- [ ] Commit with `test: cover wiki backlinks index refresh`.

### Task 3: Documentation and integration verification

**Files:**
- Modify: `README.md`
- Modify: `r260829.md`

- [ ] Document that local backlinks are indexed during refresh and become visible after successful writes.
- [ ] Run the full test and static checks again.
- [ ] Commit with `docs: document indexed local wiki backlinks`.
