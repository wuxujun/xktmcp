# Circuit Breaker Configuration Design

## Goal

Make circuit-breaker policy configurable at startup without relying on mutable package globals.

## Design

- Use one validated policy for upstream, student, RAG, staff, and Wiki breakers.
- Read `UPSTREAM_CB_FAILURE_THRESHOLD`, `UPSTREAM_CB_COOLDOWN_SECONDS`, and `UPSTREAM_CB_HALF_OPEN_PROBES` once during startup.
- Keep defaults at 5 failures, 10 seconds, and 1 probe.
- Reject malformed or non-positive values during startup.
- Construct a breaker set and inject the module breaker into each API constructor.

## Validation

- Existing defaults remain unchanged when variables are absent.
- Each API uses its own named breaker while sharing the configured policy.
- Invalid startup values fail closed.
- Existing breaker state-transition tests remain green.
