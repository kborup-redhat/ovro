# OVRO — OpenShift Virtualization Rightsizing Operator

OVRO analyses CPU and memory utilisation of KubeVirt virtual machines on OpenShift, generates rightsizing recommendations, and lets cluster administrators apply or revert changes through an OpenShift Console dynamic plugin.

## Features

- **Automated analysis** — queries Prometheus/Thanos for P95 and max utilisation over a configurable lookback window.
- **Rightsizing calculator** — recommends downsize or upsize with configurable headroom, minimum-savings thresholds, and percentile tuning.
- **Hotplug-aware** — detects live CPU/memory hotplug capability; applies changes without restart when possible.
- **Console plugin** — browse recommendations, apply/revert changes, exclude VMs, and view cluster-wide savings from the OpenShift Console.
- **RBAC-enforced** — every API call is scoped to the namespaces the requesting user can access via TokenReview and SubjectAccessReview.
- **Scheduled restarts** — for non-hotplug VMs, schedule a restart window or trigger it immediately.
- **Policy-driven** — a cluster-scoped `RightsizingPolicy` CR controls lookback, percentile, headroom, thresholds, and reconcile interval.

## Architecture

| Component | Description |
|-----------|-------------|
| **Recommendation controller** | Watches `VirtualMachine` objects, queries Prometheus, runs the calculator, and creates/updates `RightsizingRecommendation` CRs. |
| **Restart controller** | Watches recommendations in `applied-pending-restart` state and triggers VM restarts at the scheduled time. |
| **REST API server** | Serves the console plugin with filtered, RBAC-scoped data. Runs as a manager-managed runnable. |
| **Console plugin** | React/TypeScript OpenShift dynamic plugin using PatternFly. |

## Prerequisites

- OpenShift 4.14+
- OpenShift Virtualization (KubeVirt) installed
- Prometheus / Thanos Querier available in-cluster
- `oc` CLI

## Quick Start

```bash
# Install CRDs
make install

# Deploy (uses internal registry by default)
make deploy IMG=image-registry.openshift-image-registry.svc:5000/ovro-system/ovro:latest

# Apply a default policy
oc apply -f config/samples/
```

## Building

```bash
# Backend
make build

# Console plugin
cd console-plugin && npm ci && npm run build

# Container images
make docker-build IMG=<registry>/ovro:<tag>
cd console-plugin && podman build -t <registry>/ovro-console-plugin:<tag> .
```

## Testing

```bash
# Go unit tests
make test

# Console plugin tests
cd console-plugin && npm test

# Lint
go vet ./...
cd console-plugin && npx eslint src/
```

## CI/CD

A Tekton pipeline (`tekton/pipeline.yaml`) runs on every push via a GitHub webhook EventListener. It lints, tests, builds both container images, and deploys to the target namespace.

## Configuration

The `RightsizingPolicy` CR controls the operator behaviour:

| Field | Default | Description |
|-------|---------|-------------|
| `lookbackDays` | 14 | Days of metric history to analyse |
| `algorithm.percentile` | 95 | Utilisation percentile for sizing |
| `algorithm.headroomPercent` | 20 | Extra capacity above the percentile |
| `thresholds.minCpuSavings` | 1 | Minimum CPU core savings to recommend |
| `thresholds.minMemorySavings` | 1Gi | Minimum memory savings to recommend |
| `thresholds.upsizeUtilizationPercent` | 90 | P95 above this triggers upsize |
| `reconcileIntervalMinutes` | 60 | How often to re-evaluate each VM |
| `revertRetentionDays` | 30 | Days a revert option stays available |

## License

Copyright 2026. Licensed under the Apache License, Version 2.0.
