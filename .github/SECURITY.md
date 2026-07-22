# Security Policy

## Reporting a vulnerability

**Do not file public issues for security reports.** Public exposure
before a patch ships puts every adopter at risk.

### Private reporting channels

Choose either:

1. **GitHub Security Advisory** (recommended):
   <https://github.com/keiailab/qdrant-operator/security/advisories/new>
2. **Email**: `security@keiailab.com` (PGP optional):
   - PGP fingerprint:
     `89A4 0947 6828 CB99 2338  C378 651E 51AF 520B CB78`
   - This key is shared across all keiailab operator repositories.

### What to include

- Affected versions (release tag or commit SHA)
- Reproduction steps (the smallest reliable repro you can produce)
- Impact assessment (include a CVSS self-score if available)
- Reporter identity — let us know if you would like a credit

## Response SLA

| Stage | Target |
|---|---|
| Initial acknowledgement | within 72 hours |
| Severity triage | within 7 days |
| Patch release | by severity (Critical: 14 days, High: 30 days, Medium: 60 days) |
| Public disclosure | 14 days after the patch ships (coordinated disclosure on request) |

## Supported versions

| Version | Supported |
|---------|-----------|
| 0.x (alpha) | ✅ Latest minor only |
| 1.0+ (stable) | TBD — updated after the first stable release |

The API is currently `v1alpha1`. There is **no backward compatibility
guarantee**; security fixes ship only on the latest release.

## Operational security recommendations

When you run `qdrant-operator`:

1. **Protect the Qdrant API key.** `QdrantCluster.spec.apiKey`
   references a `Secret`; keep it in its own namespace-scoped `Secret`
   gated by RBAC and never commit it in plaintext.
2. **Enable TLS.** Set `spec.config.tlsEnabled=true` and supply a
   certificate `Secret` so cluster and client traffic is encrypted.
3. **Run non-root.** The chart defaults to a restricted Pod Security
   Standard (`runAsNonRoot`, read-only root filesystem, all
   capabilities dropped). Keep `spec.runAsUser` / `spec.fsGroup`
   non-zero and apply
   `pod-security.kubernetes.io/enforce=restricted` to your namespace.
4. **Guard your data.** PVCs (`volumeClaimTemplates`) are intentionally
   **not** owned by the operator — deleting a `QdrantCluster` leaves
   the PVCs behind unless you set
   `persistence.retentionPolicy: Delete`. Back up before destructive
   changes.
5. **Verify your container image.** When you build your own operator
   image variant, scan the result with `trivy` or `grype`.

## Dependency security

Every transitive Go dependency is scanned for a permissive license
(`go-licenses.yml`) and for known vulnerabilities (`govulncheck` +
Trivy in `security-scan.yml`). Dependabot / Renovate auto-update PRs
are reviewed at the front of the queue.

## Known limitations

The Phase A operator intentionally refuses unsafe operations
(naive scale-down, immutable-field mutation) rather than risking data
loss — see the "Honest limitations" section of the
[README](../README.md). Report anything that bypasses those guards
through the private channels above.
