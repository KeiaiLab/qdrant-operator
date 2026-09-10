# Changelog

All notable changes to qdrant-operator will be documented in this file.
Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning: [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Generation: `git cliff --tag vX.Y.Z` produces the skeleton at release-tag
time; entries are then written out so each one says what broke and why the
fix looks the way it does. A one-line subject is not a changelog.

## [Unreleased]

### Added

- `make doc-drift` — a release gate and CI job that fails when a Kind with a
  controller is still described as planned in any README, or when the publish
  channel count in the docs disagrees with `hack/release.sh`. Both checks are
  mechanical. Written after 0.9.0 had to correct documentation that had been
  three releases behind the code: the drift was never anyone's task, so it
  never got done. A check does not forget.

## [0.9.0] - 2026-09-10

### Fixed

- A `Dead` replica permanently stalled the rebalancer. The planner held back
  every move while any shard was not `Active` — correct for a transfer in
  flight, wrong for `Dead`, which is a terminal state a replica never leaves
  on its own. The replication-factor repair that exists to recover lost
  durability was therefore disabled by exactly the condition it was written
  to fix, and since a dead entry never disappears, count and size rebalancing
  stayed frozen behind it. Losing one peer permanently was enough to reach
  this state. The gate now separates terminal from transitional: genuinely
  transitioning shards still pause planning, `Dead` ones fall through to
  replicate → reclaim → rebalance. Reclaiming a dead replica is a deliberate
  exception to the no-destructive-action rule and only happens once healthy
  replicas meet the replication factor; auto-dropping surplus *healthy*
  replicas remains forbidden.
- Replication could target a peer that already held a dead copy of the same
  shard. Unreachable before the gate change, fixed alongside it.
- Stale `Degraded=True` (`MoveFailed`) never cleared once the rebalance plan
  became empty. `reconcileRebalance` set the condition when a move failed but
  only ever cleared it from the active-move settle path, so a cluster that had
  since reached balance kept a permanent red light. Observed live 2026-08-22
  through 2026-08-26: `Degraded=True` for four days while all 21 collections
  were green and the shard named in the message was `Active` on both peers.
  A condition that can be raised but not lowered hides the next real fault.
  The balanced branch now clears the condition it owns, and only that one -
  `DrainBlocked` and `ImmutableFieldChanged` belong to other paths.

### Changed

- README (all four languages), the chart description, and the release
  instructions described a Phase A provisioning layer with no collections and
  no rebalancing. Phases A, B and E have been implemented and running for
  weeks. Documentation now states what the operator does, how the rebalance
  loop decides, and which limitations still hold. The publish channel count
  is corrected from five to four (the GitLab mirror and internal registry
  were retired in 0.8.0).

### Security

- Cleared four govulncheck advisories in transitive dependencies —
  `cel-go` GO-2026-6094, `otel` GO-2026-5158, `x/mod` GO-2026-6179 and
  GO-2026-6180. None were reachable from this codebase. The k8s.io group and
  controller-runtime are intentionally held back: k8s.io 0.36.4 requires
  `structured-merge-diff/v7` while controller-runtime v0.24.1 builds against
  v6, so they must move together in a separate change.

## [0.7.0] - 2026-07-22

### Added

- `ownership.*` chart values (`ownership.email`, `ownership.owner`) rendering an
  `email` annotation and `owner` label on the controller-manager Deployment
  (following the valkey-operator pattern). Re-enables the kube-linter
  `required-annotation-email` / `required-label-owner` checks.
- Release pipeline gained a fifth channel: the operator image is now also pushed
  to `ghcr.io/keiailab/qdrant-operator` (the chart's default `image.repository`),
  so `image.tag`-unset consumers can pull the published image. `hack/release.sh`
  and `hack/verify-publish.sh` tag/push/verify this channel.

### Changed

- Kubernetes event recording migrated from the deprecated
  `EventRecorder`/`GetEventRecorderFor` (`k8s.io/client-go/tools/record`) to the
  `events.k8s.io/v1` API (`k8s.io/client-go/tools/events`) via the shared
  `keiailab-commons/pkg/events` adapter, matching the valkey/mongodb/postgres
  operators. Removes the `//nolint:staticcheck` (SA1019) suppressions.

## [0.6.0] - 2026-07-21

### Added

- Phase A operator foundation: `QdrantCluster` CRD, controller, and RBAC
  for declarative provisioning of distributed (Raft) Qdrant clusters.
- Helm chart at `deploy/chart` (CRDs, RBAC, leader-election controller-manager).
- Status reporting (`phase` / `readyReplicas` / `peers` / `conditions`).
- Naive scale-up (new peers join Raft); safe-by-default rejection of
  destructive scale-down and immutable-field mutation via a `Degraded`
  condition.

<!-- generated by git-cliff -->
