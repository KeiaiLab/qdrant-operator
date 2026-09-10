<p align="center">
  <a href="https://keiailab.com">
    <img src="docs/branding/symbol.png" alt="qdrant-operator" width="96"/>
  </a>
</p>

# qdrant-operator

> **A Kubernetes operator that provisions and operates distributed [Qdrant](https://qdrant.tech) vector-database clusters from a single `QdrantCluster` resource. MIT licensed.**

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-blue.svg" alt="License: MIT"/></a>
  <a href="go.mod"><img src="https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white" alt="Go"/></a>
  <a href="https://qdrant.tech/"><img src="https://img.shields.io/badge/Qdrant-1.18%2B-DC244C?logo=qdrant&logoColor=white" alt="Qdrant"/></a>
  <a href="https://kubernetes.io/"><img src="https://img.shields.io/badge/Kubernetes-1.29%2B-326CE5?logo=kubernetes&logoColor=white" alt="Kubernetes"/></a>
</p>

<p align="center">
  <b>English</b> ·
  <a href="README.ko.md">한국어</a> ·
  <a href="README.ja.md">日本語</a> ·
  <a href="README.zh.md">中文</a>
</p>

## Design assets

| Asset | Path | Usage |
|---|---|---|
| Centered service symbol | [`docs/branding/symbol.png`](docs/branding/symbol.png) | GitHub README, Artifact Hub icon |
| Keiailab base symbol | [`docs/branding/base-symbol.png`](docs/branding/base-symbol.png) | Source reference for the outer rotating-arrow mark |
| Branding guide | [`docs/BRANDING.md`](docs/BRANDING.md) | Public visual usage rules |

## Why

Running self-hosted Qdrant on Kubernetes means assembling a StatefulSet, Services, a ConfigMap, and PVCs by hand — and manually moving shards every time you add or remove a node. Qdrant exposes all the primitives you need (Raft peer join, `move_shard`, `replicate_shard`, collection aliases) as public APIs, but the control loop that ties them together and converges to a desired state is left to the operator.

This operator is that control loop. Provisioning and shard orchestration are implemented; backup/restore and Raft-aware rolling upgrades are not yet (see the roadmap below).

## Custom resources

| Kind | Status | What it does |
|---|---|---|
| `QdrantCluster` | Implemented | A standalone instance or a distributed (Raft) Qdrant cluster, with shard rebalancing and safe scale-in |
| `QdrantCollection` | Implemented | Declarative collections — create, adopt, and alias-based re-sharding |
| `QdrantBackup` | Implemented | Scheduled snapshot backups across every peer, with retention |
| `QdrantRestore` | Implemented | Restore a collection from a backup generation |

All resources use the API group `qdrant.keiailab.com/v1alpha1`.

## What it does

Phases A and B are implemented and running in production.

**Provisioning**

- Declarative create/update/delete of a distributed Qdrant cluster through the `QdrantCluster` CRD
- Parity with the equivalent hand-written manifests — ServiceAccount, ConfigMap, headless Service, client Service, StatefulSet, and a PodDisruptionBudget at `replicas >= 2`
- `apiKey` / `readOnlyApiKey` wired into the Qdrant auth env from a Secret, with an `AuthWithoutTLS` warning when a key is set without TLS
- Status reporting (`phase` / `readyReplicas` / `peers` / `shardDistribution` / `plannedMoves` / `conditions`)

**Collections**

- Declarative collections through the `QdrantCollection` CRD — created if absent, **adopted** if already present
- A spec that disagrees with the live collection surfaces as `Degraded(ParamsMismatch)`; the operator never recreates a collection to force a match
- `onDelete: Retain` by default — deleting the CR does not delete your data
- Changing `shardNumber` runs an alias-based re-shard: shadow collection → copy → atomic alias swap, with the swap as the single commit point

**Shard rebalancing**

- Continuous observe → plan → execute convergence loop, one shard operation at a time
- Balances shard **count** across peers first, then total **points** per peer as a secondary criterion once counts are level
- Repairs under-replicated shards back to the collection's `replication_factor`, and reclaims dead replicas once healthy copies are back
- **scale-up**: new peers join Raft and receive shards automatically
- **scale-in**: the departing peer is drained (move shards → remove from consensus → shrink the StatefulSet), never truncated
- Pods stranded on a permanently failed node are force-deleted so the StatefulSet can replace them
- **Raft-aware rolling upgrades**: the operator drives the StatefulSet's rollout one ordinal at a time and only advances once the restarted peer has rejoined consensus and every shard is `Active` again. Rebalancing pauses for the duration
- `spec.rebalance.enabled: false` turns the loop into a dry run — plans are still published to `status.plannedMoves`, nothing is issued

**Autoscaling**

- `QdrantCluster` exposes a `/scale` subresource, so a KEDA `ScaledObject` (or an HPA) can drive `spec.replicas` directly and let the operator handle the shard movement

**Not implemented**

- Backup / restore → Phase C
- Raft-aware rolling-upgrade orchestration → Phase D

## How rebalancing works

Every automatic action is published before it is taken. There is no hidden work to reconstruct after the fact.

```
observe            plan                          execute
─────────────────  ────────────────────────────  ─────────────────────────
GET /cluster       1. restore replication factor  one operation at a time,
GET .../cluster    2. reclaim dead replicas       wait for it to land,
per-peer sizes     3. balance count, then size    then re-plan from scratch
                   → status.plannedMoves
```

The order is the safety invariant: durability is restored before anything is removed, and balancing never counts unusable copies as placement. Plans are recomputed from the current observation on every pass and are fully deterministic, so an interrupted operator resumes at the same conclusion without persisting a queue.

Only one shard operation is in flight at a time — moves are expensive in network, disk, and re-indexing, and running them in parallel hurts the cluster you are trying to help. A balanced cluster issues no writes at all; it only reads.

Failures surface as a `Degraded` condition and an Event rather than a silent retry loop, and a condition is cleared by the same path that raised it.

## Backup and restore

A snapshot in Qdrant is node-local: it captures only the shards on the peer that produced it. One backup of a collection is therefore the set of every peer's snapshot, and `QdrantBackup` fans out accordingly.

```yaml
apiVersion: qdrant.keiailab.com/v1alpha1
kind: QdrantBackup
metadata: { name: nightly, namespace: data }
spec:
  clusterRef: my-qdrant
  collections: []          # empty = every collection in the cluster
  schedule: "0 3 * * *"    # omit for a one-shot backup
  retention:
    keepLast: 7            # omit and nothing is ever deleted
```

Creation is issued asynchronously and completion is decided by observing the snapshot list — a large collection takes far longer than any HTTP timeout. One snapshot is in flight at a time. A schedule that cannot be parsed surfaces as `Degraded` rather than a backup that silently never runs.

Point snapshot storage at an S3-compatible bucket and Qdrant writes there itself; the operator never handles the bytes.

```yaml
spec:
  snapshots:
    storage: S3
    s3:
      bucket: qdrant-backups
      endpointURL: http://rook-ceph-rgw-my-store.rook-ceph.svc:80
      credentials:
        name: qdrant-backup-s3   # an ObjectBucketClaim secret works as-is
```

If a NetworkPolicy stands between the cluster and that endpoint, allow the **DNATed pod port**, not the service port — getting it wrong presents as a hang rather than a 403.

Restore is a separate one-shot resource:

```yaml
apiVersion: qdrant.keiailab.com/v1alpha1
kind: QdrantRestore
spec:
  clusterRef: my-qdrant
  collection: my-vectors
  fromBackup: nightly      # or list spec.sources with explicit per-peer locations
  priority: snapshot       # conflicts resolve in favour of the snapshot
```

Nothing is deleted to make room for a restore. Qdrant's own guidance is to drop and recreate the collection first, but that deletion is irreversible; `priority: snapshot` gets the same result without it. A completed `QdrantRestore` never runs again — re-running it would silently roll back everything written since.

## Honest limitations (please read)

The operator weighs "structurally preventing destructive mistakes" over "new features".

1. **immutable-field changes are unsupported.** Spec changes that touch a StatefulSet immutable field (`serviceName` / `volumeClaimTemplates` / `selector`, e.g. `persistence.size`) are surfaced via a `Degraded` condition + Event instead of a crash-looping patch; the StatefulSet is preserved. Controlled recreate is a Phase D task.
2. **Collections are never recreated.** If a `QdrantCollection` spec disagrees with the live collection on vector size, distance, or replication factor, the operator reports `ParamsMismatch` and stops. Only `shardNumber` has a non-destructive migration path (alias re-shard).
3. **Surplus healthy replicas are not removed.** Replicas above `replication_factor` are observed and reported, never auto-dropped. Dead replicas are the one exception — they serve nothing, and they are only reclaimed after healthy copies meet the replication factor.
4. **Restoring from S3 needs explicit locations.** Qdrant recovers from a URL or a local file; it does not accept an `s3://` location, and a peer does not serve its own snapshots over HTTP once they live in a bucket. `fromBackup` therefore only works for peer-local snapshots — with S3 storage, give `spec.sources` a reachable URL per peer. The operator refuses to guess an address rather than issuing a restore against a URL that does not exist.
5. **Size-aware placement needs every peer reachable.** The per-peer point counts that drive the secondary balancing criterion are collected peer by peer; if any peer fails to answer, the size stage is skipped entirely rather than acting on a partial map.

For data safety, PVCs (`volumeClaimTemplates`) are **intentionally not owned** by the operator — deleting a `QdrantCluster` leaves the PVCs behind (they are reclaimed only when you set `persistence.retentionPolicy: Delete`).

## Installation

The operator is deployed with its own Helm chart.

```sh
helm install qdrant-operator ./deploy/chart \
  --namespace qdrant-operator-system --create-namespace
```

This installs the CRDs, RBAC, and a leader-election-enabled controller-manager Deployment. The container image is published at `ghcr.io/keiailab/qdrant-operator`.

### From source

```sh
make install                                   # install the CRDs
make deploy IMG=ghcr.io/keiailab/qdrant-operator:latest
```

## Usage

```yaml
apiVersion: qdrant.keiailab.com/v1alpha1
kind: QdrantCluster
metadata:
  name: my-qdrant
  namespace: data
spec:
  image:
    repository: qdrant/qdrant   # default
    tag: v1.18.2                # default
  replicas: 3
  resources:
    requests: { cpu: 250m, memory: 512Mi }
    limits:   { cpu: "2",  memory: 4Gi }
  persistence:
    size: 10Gi                   # default
    storageClassName: ceph-rbd   # default
    accessModes: [ReadWriteOnce] # default
    retentionPolicy: Retain      # default — Retain | Delete
  config:
    clusterEnabled: true   # default — distributed (Raft) mode
    tlsEnabled: false
    # rawOverride: {}      # production.yaml escape hatch (rare upstream options)
  serviceType: ClusterIP   # default
  apiKey:
    name: my-qdrant-api-key  # Secret name (required)
    key: api-key             # default
  runAsUser: 1000  # default
  fsGroup: 3000    # default
```

Apply it and check the status:

```sh
kubectl apply -f qdrantcluster.yaml
kubectl get qdrantcluster my-qdrant -n data -o jsonpath='{.status.phase}'
```

## Roadmap

| Phase | Subsystem | Key CRD | What | Depends on | Status |
|---|---|---|---|---|---|
| **A** | Operator foundation + provisioning | `QdrantCluster` | scaffold · controller · RBAC + declarative distributed cluster bring-up | — | **Done** |
| **B** | Collection / shard orchestration | `QdrantCollection` | declarative collections + auto-rebalance (observe → plan → `move_shard`) + replication-factor repair + alias re-shard + safe scale-in drain | A | **Done** |
| **C** | Data protection | `QdrantBackup` / `QdrantRestore` | scheduled snapshot-API backups across every peer · S3 object storage · retention · restore | A | **Done** |
| **D** | Day-2 / upgrades | (status) | Raft-aware rolling upgrades · health gate · observability · TLS | A | **Upgrades and health gate done**; observability and TLS remain |
| **E** | Autoscaling integration | (`/scale` subresource) | scale triggers → wired into the Phase B rebalance machine | B | **Done** — `QdrantCluster` is directly scalable by KEDA or an HPA; no dedicated CRD was needed |

Dependency graph: `A → {B, C, D}` can proceed in parallel; `E` requires `B`. Phase B is the core value of this project (automated shard rebalancing).

Phase E landed as a `/scale` subresource rather than a `QdrantAutoscaler` CRD: KEDA can already scale any custom resource that exposes `/scale`, so a second autoscaler would have been a reimplementation. KEDA decides, the operator executes.

## API

- Group / Version: `qdrant.keiailab.com/v1alpha1`
- Kinds: `QdrantCluster`, `QdrantCollection`
- Domain: `keiailab.com` (`kubebuilder init --domain keiailab.com --group qdrant`)

The API is `v1alpha1`; expect changes before a stable release.

## Documentation

- Development guide (scaffold structure, regeneration commands, controller conventions): [`AGENTS.md`](AGENTS.md)
- Design documents (Phase B and beyond): [`docs/design/`](docs/design/)

## Releasing

Maintainers publish to four channels at once — GitHub tag, ghcr container image, ghcr OCI chart, and the central catalog — from a single command so no channel is missed:

```bash
make release VERSION=0.9.0     # gate → tag → image → chart → catalog → verify
DRY_RUN=1 hack/release.sh 0.9.0  # print every step without publishing
make verify-publish            # check the 4-channel consistency of the current state
```

The release gate runs `test`, `lint`, and `publish-scan` first and re-checks the
result with `verify-publish` — if any of them fails, the release aborts.

## Contributing

Contributions are welcome. For anything non-trivial, please open an issue first so we can agree on the API surface. See [CONTRIBUTING.md](.github/CONTRIBUTING.md), and run `make help` for the full list of build targets.

To report a security issue, follow [SECURITY.md](.github/SECURITY.md) rather than opening a public issue.

## License

[MIT](LICENSE) © keiailab

---

<p align="center">© 2026 keiailab · MIT · <a href="https://keiailab.com">keiailab.com</a></p>
