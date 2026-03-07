# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Leibrix is the **Master/control plane** of a distributed in-memory acceleration system for interactive analytics. It orchestrates data placement, lifecycle management, and cluster coordination across Worker nodes. Written in Go 1.24, it uses embedded etcd for consensus and gRPC for communication.

The design principles of Leibrix are predicated on algorithmic consistency, usability, and performance, all of which must be preserved in any modifications. The data plane implementation of Leibrix is available at https://github.com/pzhenzhou/leibrix-worker.

## Build & Development Commands

```bash
make build              # Generate protos, format, build binary (bin/leibrix-srv)
make test               # Build + run tests with coverage (excludes proto/ and cmd/)
make fmt                # Format code with gofmt
make vet                # Run go vet
make proto-generate     # Compile .proto files to Go (auto-downloads protoc tools)
make run ARGS="..."     # Build and run locally
make run-dev            # Run with pprof and metrics enabled
make docker-build       # Build Docker image
```

Run a single test:
```bash
go test ./internal/api/grpc/ -run TestEpochGenerator -v
```

## Architecture

### Entry Point & Lifecycle (`cmd/main.go`)
Loads YAML config → starts embedded etcd → runs leader election → launches gRPC server → graceful shutdown.

### Two gRPC Services

1. **ManagementService** (unary RPCs): Dataset admission (`AdmitDataset`), tenant quota management (`UpsertTenantQuota`). External-facing API.
2. **ControlPlaneService** (bidirectional streaming): Worker coordination via `CoordinateWorker` stream. Handles registration, heartbeats, data assignment, and pull status updates. Internal API between Master and Workers.

### Key Modules

- **`internal/cluster/`** — Embedded etcd server lifecycle, leader election (etcd concurrency), membership tracking. `server.go` handles bootstrap with automatic cluster state detection.
- **`internal/api/grpc/`** — gRPC service implementations, session management (`SessionManager`), event dispatching, epoch computation from time ranges.
- **`internal/conf/`** — YAML configuration loading. See `examples/config/` for single-node and 3-node HA examples.
- **`pkg/proto/`** — Generated protobuf Go code. **Do not edit manually.** Source `.proto` files live in `proto/`.
- **`pkg/common/`** — Shared utilities (logger, etcd client helpers, constants).

### Protobuf Workflow

Proto sources in `proto/`, generated code in `pkg/proto/`. Run `make proto-generate` after changing `.proto` files. Uses `protoc-gen-go` v1.36.8 and `protoc-gen-go-grpc` v1.5.1 (auto-downloaded to `bin/`).

### Key Dependencies

- `go.etcd.io/etcd` (client/v3 + server/v3) — distributed consensus, leader election, state store
- `google.golang.org/grpc` — RPC framework
- `go.uber.org/zap` + `github.com/go-logr/zapr` — structured logging
- `github.com/goccy/go-yaml` — config parsing
- `github.com/cenkalti/backoff/v5` — retry logic

### Deployment

Kubernetes deployment via Kustomize overlays in `config/`. Supports dev (k3d) and prod environments. Requires 3+ nodes for production HA (etcd quorum).
