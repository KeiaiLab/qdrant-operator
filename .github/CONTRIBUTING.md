# Contributing

Thanks for your interest in `qdrant-operator`. This document describes
the PR process, how to run the tests, and when an Architecture Decision
Record (ADR) is required.

GitHub (`github.com/keiailab/qdrant-operator`) is the canonical
development and publishing home for this project. Open issues and pull
requests here against the `main` branch.

## Getting started

### Prerequisites

| Tool | Minimum | Notes |
|---|---|---|
| Go | 1.26 | Matches `go.mod` |
| Docker | 24+ | buildx default builder |
| kind | 0.27+ | Local end-to-end tests |
| kubectl | 1.29+ | Matches the chart's `kubeVersion` floor |
| make | GNU make | Drives every Makefile target |

### First build and test

```sh
git clone https://github.com/keiailab/qdrant-operator.git
cd qdrant-operator

# Unit tests (envtest binaries are fetched automatically).
make test

# End-to-end (deploys the operator on a throwaway kind cluster).
make test-e2e
```

Run `make help` for the full list of build targets.

## Pull-request workflow

1. **Open an issue first** for any non-trivial change (architecture,
   API, security). A short alignment thread saves rewrites later.
2. **DCO sign-off is mandatory.** Every commit must end with a
   `Signed-off-by:` trailer (`git commit -s`). The `DCO` GitHub Actions
   check enforces this and unsigned PRs cannot be merged. See the
   [Developer Certificate of Origin](https://developercertificate.org/).
3. **Conventional Commits.** Subject line follows
   `<type>(<scope>): <subject>`, e.g. `feat(controller): reject unsafe scale-down`.
   The body can be English or Korean.
4. **Tests required.** Any behaviour change ships with at least one
   unit test that exercises it; `make test` must pass.
5. **Lint must pass.** `make lint` runs `golangci-lint` (with the
   Kubernetes `logcheck` module plugin built from `.custom-gcl.yml`);
   a failing lint blocks the PR in CI.
6. **PR body should include:**
   - The user-visible scenario (why this change is needed)
   - Verification commands and trimmed output (`make test`,
     `kubectl apply -f …`, etc.)
   - The blast radius — which areas you re-tested for regressions
   - Links to any related ADR or issue
7. **Review SLA**: best-effort first review within 24 hours.

## Architecture Decision Records (ADR)

Write an ADR (in `docs/kb/adr/NNNN-<slug>.md`) when the change involves:

- A new CRD or a semantic change to an existing CRD field
- A new third-party dependency
- Security, authentication, or data-flow surface changes
- The third or later attempt to solve the same problem differently
  (convergence ADR)

Use Nygard's five-section template (Context / Decision / Consequences
/ Alternatives Considered / Status). Larger design explorations live
under [`docs/design/`](../docs/design/).

## Code style

- **Go**: `gofmt`, `goimports`, and `golangci-lint` (run via
  `make lint`). `errcheck` is enforced.
- **Comments**: English or Korean both welcome. Explain *why*, not
  *what* — the code already shows what it does.
- **Tests**: prefer the fake client; use `envtest` only for genuine
  controller integration paths. Always use `WithStatusSubresource` so
  spec and status remain isolated.
- **Generated code**: after editing `*_types.go` or `+kubebuilder`
  markers, run `make manifests generate` and commit the regenerated
  CRDs / RBAC / DeepCopy code. Never hand-edit `zz_generated.*` or
  `config/crd/bases/*`.

## Security issues

Do **not** open public issues for vulnerabilities. See
[SECURITY.md](SECURITY.md) for the private reporting channels (GitHub
Security Advisory and a PGP-signed email address).

## License

This project is MIT License. By contributing you agree that your
contribution is distributed under the same license.
