# HelixDeploy ⚙️

**Self-hosted Kubernetes-native CI/CD Platform with Progressive Delivery Engine**

[![CI](https://github.com/KabirMoulana/helixdeploy/actions/workflows/ci.yml/badge.svg)](https://github.com/KabirMoulana/helixdeploy/actions)
[![Go Report Card](https://goreportcard.com/badge/github.com/KabirMoulana/helixdeploy)](https://goreportcard.com/report/github.com/KabirMoulana/helixdeploy)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

HelixDeploy is a Kubernetes-native CI/CD platform built as a **custom controller with CRDs**. Define your entire pipeline as a Kubernetes resource, get GitOps-native deployments, manual approval gates, and progressive canary delivery — without GitHub Actions limitations.

> **Notable:** HelixDeploy uses itself to deploy itself (dogfooding). After bootstrap, all releases go through a `Pipeline` CR.

## Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│                        Kubernetes Cluster                        │
│                                                                  │
│  kubectl apply -f pipeline.yaml                                  │
│          │                                                       │
│          ▼                                                       │
│  ┌───────────────┐    Reconcile Loop    ┌──────────────────────┐ │
│  │ Pipeline CRD  │◄──────────────────── │ HelixDeploy          │ │
│  │ (desired)     │                      │ Controller           │ │
│  └───────────────┘    Creates/watches   │ (controller-runtime) │ │
│                          │              └──────────────────────┘ │
│                          ▼                                       │
│                  ┌───────────────┐                               │
│                  │ Tekton        │  ← Executes steps in          │
│                  │ PipelineRuns  │    isolated containers        │
│                  └───────┬───────┘                               │
│                          │                                       │
│              ┌───────────┴───────────┐                           │
│              ▼                       ▼                           │
│     ┌────────────────┐    ┌─────────────────────┐               │
│     │ Argo Rollouts  │    │ NATS JetStream       │               │
│     │ (canary logic) │    │ (pipeline events bus)│               │
│     └────────────────┘    └─────────────────────┘               │
└──────────────────────────────────────────────────────────────────┘
```

## Quick Start (Local Kind Cluster)

```bash
git clone https://github.com/KabirMoulana/helixdeploy
cd helixdeploy

# Bootstrap a local kind cluster with all dependencies
make bootstrap
# This installs: cert-manager, Tekton, HelixDeploy CRDs + controller (~3 min)

# Run example pipeline
kubectl apply -f config/examples/my-service-pipeline.yaml

# Watch it run
kubectl get pipelines -w
# NAME                   PHASE     CURRENT STAGE    LAST RUN
# my-service-pipeline    Running   build            2m ago

# Check stage details
kubectl describe pipeline my-service-pipeline
```

## Pipeline API

```yaml
apiVersion: helix.io/v1alpha1
kind: Pipeline
metadata:
  name: my-pipeline
spec:
  triggers:
    - type: push
      branches: [main]
  stages:
    - name: build
      steps:
        - name: compile
          image: golang:1.22
          script: go build ./...
    - name: deploy-prod
      approvalRequired: true      # ← Pauses for human approval
      approvalTimeout: "24h"
      steps:
        - name: helm-upgrade
          image: alpine/helm:3.14
          script: helm upgrade --install myapp ./charts/myapp
```

Grant approval:
```bash
kubectl annotate pipeline my-pipeline helix.io/approve=<runId> -n my-namespace
```

## Key Design Patterns

**Reconcile Loop** — The controller follows the standard controller-runtime pattern:
1. Fetch Pipeline object
2. Handle deletion via finalizer
3. Switch on `status.phase` to determine next action
4. Create/monitor Tekton PipelineRuns for each stage
5. Advance through stages, handling approvals and failures

**Sharding** — For large clusters, pipeline namespace is hashed to N controller replicas to distribute load without external coordination.

**Exactly-once execution** — The controller is idempotent: re-running reconcile for the same runID won't double-submit Tekton runs (checked via label selector).

## Running Tests

```bash
# Unit + integration tests (uses envtest — no cluster needed)
make test

# E2E tests (requires kind cluster from make bootstrap)
make test-e2e
```

## Project Structure

```
helixdeploy/
├── api/v1alpha1/          # CRD type definitions (Pipeline, Release)
├── cmd/controller/        # Controller manager entrypoint
├── config/
│   ├── crd/               # Generated CRD manifests
│   ├── rbac/              # RBAC roles and bindings
│   ├── manager/           # Controller deployment manifest
│   └── examples/          # Example Pipeline CRs
├── internal/
│   ├── controller/        # Pipeline + Release reconcilers
│   ├── tekton/            # Tekton PipelineRun client
│   ├── rollout/           # Argo Rollouts integration
│   └── webhook/           # Admission webhook for CRD validation
├── deploy/helm/           # Helm chart for HelixDeploy itself
├── tests/e2e/             # End-to-end tests
└── docs/adr/              # Architecture Decision Records
```

## Architecture Decision Records

- [ADR-001: CRD-based pipeline definition vs webhook receiver](docs/adr/001-crd-vs-webhook.md)
- [ADR-002: Tekton as execution layer vs custom runner](docs/adr/002-tekton-integration.md)
- [ADR-003: Approval via annotation vs separate Approval CRD](docs/adr/003-approval-mechanism.md)
- [ADR-004: Controller sharding strategy](docs/adr/004-controller-sharding.md)

## License

MIT
