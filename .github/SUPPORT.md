# Support

Thanks for using `qdrant-operator`. This page explains where to get
help.

## Decide what you need

| Situation | Where to go |
|---|---|
| **You think you found a security vulnerability.** | **Do not open a public issue.** Use [SECURITY.md](SECURITY.md) — GitHub Security Advisory or `security@keiailab.com`. |
| You have an "is this supposed to work like X?" or "how do I configure Y?" question. | [GitHub Discussions](https://github.com/keiailab/qdrant-operator/discussions). Searchable and indexed by future operators. |
| You found a bug — something behaves differently from the docs. | [Open an issue](https://github.com/keiailab/qdrant-operator/issues/new/choose) using the **Bug report** template. |
| You want a feature added or behaviour changed. | [Open an issue](https://github.com/keiailab/qdrant-operator/issues/new/choose) using the **Feature request** template. Check the [Roadmap](../README.md#roadmap) first to see if it's already planned. |
| You have a "this should be in the FAQ" question. | [Open an issue](https://github.com/keiailab/qdrant-operator/issues/new/choose) using the **Question** template. |
| You want to contribute code or docs. | [CONTRIBUTING.md](CONTRIBUTING.md). |

## Before opening an issue, please

1. Search [existing issues](https://github.com/keiailab/qdrant-operator/issues) and [Discussions](https://github.com/keiailab/qdrant-operator/discussions) — your question may already be answered.
2. Have the following ready in your report:
   - `qdrant-operator` version (`kubectl get deploy -n qdrant-operator-system -o jsonpath='{.items[0].spec.template.spec.containers[0].image}'`)
   - Kubernetes version (`kubectl version`)
   - Helm chart version (`helm list -A | grep qdrant-operator`)
   - The smallest reproduction you can produce
   - The output of `kubectl describe qdrantcluster <name>`

## Response expectations

This is an open-source project maintained on best-effort time.
[GOVERNANCE.md](GOVERNANCE.md) describes the decision-making and
review process. We typically respond on issues within a few business
days; security reports are handled per the SLA in
[SECURITY.md](SECURITY.md) (initial ack within 72 h, severity triage
within 7 days).

## Code of Conduct

Every channel above is governed by the
[Code of Conduct](CODE_OF_CONDUCT.md). Please read it before
participating.
