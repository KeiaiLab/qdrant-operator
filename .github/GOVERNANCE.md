# Governance

This document defines how decisions are made in
`keiailab/qdrant-operator`.

## Principles

1. **Openness.** All decisions happen on public channels — GitHub
   issues, pull requests, and ADRs.
2. **Lazy consensus.** Day-to-day changes ship when no one objects.
3. **Explicit consensus.** Architecture changes, CRD changes,
   security-model changes, and license changes require an ADR followed
   by a **2/3 supermajority** of maintainers. Ordinary proposals
   (single component, tool adoption, policy reinforcement) require a
   **simple majority** (>50%). Changes to this `GOVERNANCE.md` always
   require a 2/3 supermajority.
4. **Shared responsibility.** Maintainers are jointly responsible for
   code quality, user safety, and community health.

## Decision classification

### Routine (lazy consensus)

- Bug fixes, doc improvements, new tests, minor/patch dependency
  bumps, refactors with no public API change
- Process: PR → at least one maintainer LGTM → CI gates green → merge
- Comment window: none. Once the GitHub Actions checks pass, the PR
  can merge.

### Medium (explicit consensus)

- New CRD fields, new reconcilers, major dependency upgrades,
  changes to the public API
- Process: open an issue proposing the change → 7-day comment window
  → maintainer majority LGTM → merge
- A single objection triggers a maintainer discussion to debate.

### Architectural (ADR required)

- Introducing a new component, changing the security model, changing
  the license, breaking backward compatibility
- Process:
  1. Submit an ADR at `docs/kb/adr/NNNN-title.md`
  2. 14-day comment window
  3. 2/3 maintainer approval
  4. Move the ADR `Status` from `Draft` to `Accepted`, then open the
     implementation PR

## Security decisions

CVE reports and changes to the secrets / auth model are handled first
via the private channels in [SECURITY.md](SECURITY.md). Public
consensus follows once a patch release ships.

## Release decisions

A single maintainer may cut a release or bump a version under lazy
consensus. Creating a new long-term-support line or declaring
End-of-Life on an existing one always requires explicit consensus.

## Change history

| Date | Change |
|---|---|
| 2026-07 | Document created — keiailab operator-family governance alignment |
