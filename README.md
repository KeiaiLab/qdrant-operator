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

This operator automates that assembly and rebalancing as a Kubernetes controller. The scope is large, so it is built in five sequential phases (see the roadmap below).

## Custom resources

| Kind | Status | What it does |
|---|---|---|
| `QdrantCluster` | Phase A (implemented) | A standalone instance or a distributed (Raft) Qdrant cluster |
| `QdrantCollection` | Phase B (planned) | Declarative collections with shard rebalancing |

All resources use the API group `qdrant.keiailab.com/v1alpha1`.

## Phase A scope (current)

This repository currently implements **Phase A: operator foundation + provisioning** only.

**In scope**

- Declarative create/update/delete of a distributed Qdrant cluster through the `QdrantCluster` CRD
- Parity with the existing helm-chart output — ServiceAccount, ConfigMap, headless Service, client Service, and StatefulSet
- Status reporting (`phase` / `readyReplicas` / `peers` / `conditions`)
- **scale-up** (naive replica increase — new peers join Raft)

**Out of scope (deferred to later phases)**

- Collection / shard rebalance → Phase B
- Backup / restore → Phase C
- Raft-aware rolling-upgrade orchestration → Phase D
- Autoscaling → Phase E

## Honest limitations (please read)

Phase A is a minimal provisioning layer that weighs "structurally preventing destructive mistakes" over "new features". The following three cases are **intentionally unsupported**; instead of mutating the StatefulSet, the operator surfaces them safely via a `Degraded` condition + Event.

1. **A newly scaled-up peer stays empty.** Increasing `spec.replicas` adds a StatefulSet pod that joins Raft, but existing shard data is not moved onto it automatically (OSS Qdrant has no auto-resharding). The new peer is effectively an empty node until a collection is explicitly recreated or replicated onto it. This is resolved in **Phase B** (collection / shard orchestration).
2. **scale-down is rejected.** In a distributed database, a naive replica decrease loses the highest-ordinal peer and its shards. Lowering `spec.replicas` below the current value is rejected with a `Degraded` condition and the StatefulSet is left unchanged. Safe, drain-based scale-down is not supported until Phase B.
3. **immutable-field changes are unsupported.** Spec changes that touch a StatefulSet immutable field (`serviceName` / `volumeClaimTemplates` / `selector`, e.g. `persistence.size`) are surfaced via a `Degraded` condition + Event instead of a crash-looping patch; the StatefulSet is preserved. Controlled recreate is a Phase D+ task.

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
| **A** | Operator foundation + provisioning | `QdrantCluster` | scaffold · controller · RBAC + declarative distributed cluster bring-up | — | In progress (this repo) |
| **B** | Collection / shard orchestration | `QdrantCollection` | declarative collections + auto-rebalance (observe → plan → `move_shard`) + alias re-shard + safe scale-in drain | A | Planned |
| **C** | Data protection | `QdrantBackup` / `QdrantRestore` | scheduled snapshot-API backups · object storage · restore | A | Planned |
| **D** | Day-2 / upgrades | (status / webhook) | Raft-aware zero-downtime rolling upgrades · health gate · observability · TLS | A | Planned |
| **E** | Autoscaling integration | `QdrantAutoscaler` | scale triggers → wired into the Phase B rebalance machine | B | Planned |

Dependency graph: `A → {B, C, D}` can proceed in parallel; `E` requires `B`. Phase B is the core value of this project (automated shard rebalancing), but Phase A — where the operator takes ownership of the cluster — must come first.

## API

- Group / Version: `qdrant.keiailab.com/v1alpha1`
- Kinds: `QdrantCluster` (Phase A), `QdrantCollection` (Phase B, scaffolded)
- Domain: `keiailab.com` (`kubebuilder init --domain keiailab.com --group qdrant`)

The API is `v1alpha1`; expect changes before a stable release.

## Documentation

- Development guide (scaffold structure, regeneration commands, controller conventions): [`AGENTS.md`](AGENTS.md)
- Design documents (Phase B and beyond): [`docs/design/`](docs/design/)

## Releasing

Maintainers publish to five channels at once — GitHub tag, internal container image, ghcr container image, ghcr OCI chart, and the central catalog — from a single command so no channel is missed:

```bash
make release VERSION=0.7.0     # gate → tag → image → chart → catalog → verify
DRY_RUN=1 hack/release.sh 0.7.0  # print every step without publishing
make verify-publish            # check the 5-channel consistency of the current state
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
