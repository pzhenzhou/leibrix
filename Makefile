ifeq ($(shell uname), Darwin)
SED_IN_PLACE := sed -i ''
else
SED_IN_PLACE := sed -i
endif

# Leibrix Service binary name
BINARY_NAME=leibrix-srv

# Leibrix Service Docker image name
IMG ?= leibrix:latest

# Development image URL for k3d registry (accessible from both host and cluster)
DEV_IMG ?= dev-registry.localhost:5001/leibrix:latest

# Number of replicas for production deployment
REPLICAS ?= 2

# CONTAINER_TOOL defines the container tool to be used for building images.
CONTAINER_TOOL ?= docker

OVERLAY ?= dev

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped cmd fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

export KUBECONFIG= $(HOME)/.kube/config
PROTO_ROOT := proto
PROTO_FILES = $(shell find $(PROTO_ROOT) -name "*.proto")
PROTO_OUT := ./pkg

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUSTOMIZE ?= $(LOCALBIN)/kustomize
ENVTEST ?= $(LOCALBIN)/setup-envtest
PROTOC ?= $(LOCALBIN)/protoc
PROTOC_GEN_GO ?= $(LOCALBIN)/protoc-gen-go
PROTOC_GEN_GO_GRPC ?= $(LOCALBIN)/protoc-gen-go-grpc

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)
	@echo ""
	@echo "Examples:"
	@echo "  make run ARGS=\"--service-port=8080 --pprof=false\""
	@echo "  make run-local ARGS=\"--metrics.enable=true\""
	@echo "  make deploy OVERLAY=prod"
	@echo "  make deploy-prod IMG=docker-registry.micoworld.net/data-test/leibrix:nightly-20250829"
	@echo "  make deploy-prod IMG=custom-image:tag REPLICAS=3"
	@echo "  make k3d-deploy"

##@ Build

.PHONY: all
all: build

.PHONY: fmt
fmt:
	gofmt -l -w -d  ./pkg ./cmd

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: proto-generate
proto-generate: protoc protoc-gen-go protoc-gen-go-grpc
	PATH="$(LOCALBIN):$$PATH" $(PROTOC) \
	--go_out=$(PROTO_OUT) \
	--go-grpc_out=$(PROTO_OUT) \
	--go-grpc_opt=require_unimplemented_servers=false \
	--go_opt=paths=source_relative \
	--go-grpc_opt=paths=source_relative	\
	--experimental_allow_proto3_optional \
	$(PROTO_FILES)

.PHONY: swagger-generate
swagger-generate: ## Generate swagger documentation from annotations
	swag init --generalInfo ./cmd/main.go --output ./docs --parseDependency --parseInternal

.PHONY: build
build: proto-generate fmt
	go build -o bin/$(BINARY_NAME) cmd/main.go

.PHONY: test
test: build
	go test $(shell go list ./... | grep -v /proto | grep -v /cmd) -v -coverprofile cover.out


##@ Docker

.PHONY: docker-build
docker-build: build ## Build docker image with the manager.
	docker build -f ./docker/Dockerfile -t ${IMG} ..

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	docker push ${IMG}

# PLATFORMS defines the target platforms for  the manager image be build to provide support to multiple
# architectures. (i.e. make docker-buildx IMG=myregistry/mypoperator:0.0.1). To use this option you need to:
# - able to use docker buildx . More info: https://docs.docker.com/build/buildx/
# - have enable BuildKit, More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# - be able to push the image for your registry (i.e. if you do not inform a valid value via IMG=<myregistry/image:<tag>> then the export will fail)
# To properly provided solutions that supports more than one platform you should use this option.
#PLATFORMS ?= linux/arm64,linux/amd64,linux/s390x,linux/ppc64le
PLATFORMS ?= linux/arm64,linux/amd64
.PHONY: docker-buildx
docker-buildx: build ##Build and push docker image for the manager for cross-platform support
	#copy existing ./Dockerfile and insert --platform=${BUILDPLATFORM} into ./Dockerfile.cross, and preserve the original ./Dockerfile
    #sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' ./Dockerfile > ./Dockerfile.cross
	- docker buildx create --name project-v3-builder
	docker buildx use project-v3-builder
	- docker buildx build --push --platform=$(PLATFORMS)  --tag ${IMG} -f ./docker/Dockerfile ..
	- docker buildx rm project-v3-builder

.PHONY: docker-build-dev
docker-build-dev: build ## Build and push docker image for development to k3d registry (Apple Silicon optimized)
	@echo "🔨 Building image for k3d registry..."
	$(CONTAINER_TOOL) build --platform linux/arm64 -t localhost:5001/leibrix:latest -f ./docker/Dockerfile ..
	@echo "📤 Pushing image to k3d registry: localhost:5001"
	$(CONTAINER_TOOL) push localhost:5001/leibrix:latest
	@echo "🏷️  Tagging image for cluster access: $(DEV_IMG)"
	$(CONTAINER_TOOL) tag localhost:5001/leibrix:latest $(DEV_IMG)
	@echo "✅ Successfully built and pushed to k3d registry"
	@echo "📋 Push URL: localhost:5001/leibrix:latest"
	@echo "📋 Cluster URL: $(DEV_IMG)"

.PHONY: docker-buildx-dev
docker-buildx-dev: docker-build-dev ## Alias for docker-build-dev (for backward compatibility)

##@ Development

.PHONY: run
run: build ## Run the built binary locally. Use ARGS="--flag value" to pass arguments.
	@echo "Running $(BINARY_NAME) with args: $(ARGS)"
	./bin/$(BINARY_NAME) $(ARGS)

.PHONY: run-dev
run-dev: build ## Run with development configuration (pprof enabled, metrics enabled).
	@echo "Running $(BINARY_NAME) in development mode..."
	./bin/$(BINARY_NAME) --pprof=true --metrics.enable=true

.PHONY: run-with-metrics
run-with-metrics: build ## Run with metrics enabled.
	@echo "Running $(BINARY_NAME) with metrics enabled..."
	./bin/$(BINARY_NAME) --metrics.enable=true --metrics.sink=prometheus

.PHONY: run-local
run-local: envtest build fmt vet ## Run using go run (for development with hot reload).
	go run cmd/main.go $(ARGS)

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = true
endif

.PHONY: deploy-rbac
deploy-rbac: kustomize ## Deploy RBAC resources to the K8s cluster specified in ~/.kube/config.
	@echo "Deploying RBAC resources..."
	$(KUSTOMIZE) build config/rbac | kubectl apply -f -

.PHONY: deploy
deploy: kustomize ## Deploy to the K8s cluster specified in ~/.kube/config. Use OVERLAY=dev|prod to specify environment.
	@echo "Deploying leibrix to $(OVERLAY) environment..."
	$(MAKE) create-hive-secret DEST_NAMESPACE=leibrix-system
	$(KUSTOMIZE) build config/overlays/$(OVERLAY) | kubectl apply -f -

.PHONY: deploy-dev
deploy-dev: kustomize ## Deploy to development environment.
	@echo "🚀 Deploying leibrix to development environment..."
	cd config/overlays/dev && $(KUSTOMIZE) edit set image leibrix=$(DEV_IMG)
	$(MAKE) create-hive-secret DEST_NAMESPACE=leibrix-system
	$(KUSTOMIZE) build config/overlays/dev | kubectl apply -f -
	@echo "✅ Successfully deployed to development environment"
	@echo "📋 Namespace: leibrix-system"
	@echo "📋 Image: $(DEV_IMG)"

.PHONY: deploy-prod
deploy-prod: kustomize ## Deploy to production environment. Use IMG=<image> and REPLICAS=<count> to override defaults.
	@echo "🚀 Deploying leibrix to production environment..."
	cd config/overlays/prod && $(KUSTOMIZE) edit set image leibrix=$(IMG)
	cd config/overlays/prod && $(KUSTOMIZE) edit set replicas leibrix=$(REPLICAS)
	$(MAKE) create-hive-secret DEST_NAMESPACE=leibrix-system
	$(KUSTOMIZE) build config/overlays/prod | kubectl apply -f -
	@echo "✅ Successfully deployed to production environment"
	@echo "📋 Namespace: leibrix-system"
	@echo "📋 Image: $(IMG)"
	@echo "📋 Replicas: $(REPLICAS)"

.PHONY: build-and-deploy-dev
build-and-deploy-dev: docker-buildx-dev deploy-dev ## Build and deploy to development environment in one command.
	@echo "🎉 Development build and deploy completed!"

.PHONY: build-and-deploy-prod
build-and-deploy-prod: docker-buildx deploy-prod ## Build and deploy to production environment in one command.
	@echo "🎉 Production build and deploy completed!"

.PHONY: deploy-all
deploy-all: deploy-rbac deploy ## Deploy RBAC and application resources.

.PHONY: deploy-all-dev
deploy-all-dev: deploy-rbac deploy-dev ## Deploy RBAC and application resources to development environment.

.PHONY: deploy-all-prod
deploy-all-prod: deploy-rbac deploy-prod ## Deploy RBAC and application resources to production environment.

.PHONY: undeploy
undeploy: kustomize ## Remove resources from the K8s cluster specified in ~/.kube/config.
	@echo "Removing leibrix from $(OVERLAY) environment..."
	$(KUSTOMIZE) build config/overlays/$(OVERLAY) | kubectl delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: undeploy-dev
undeploy-dev: kustomize ## Remove resources from development environment.
	@echo "🗑️  Removing leibrix from development environment..."
	$(KUSTOMIZE) build config/overlays/dev | kubectl delete --ignore-not-found=$(ignore-not-found) -f -
	@echo "✅ Successfully removed from development environment"
	@echo "📋 Namespace: leibrix-system"

.PHONY: undeploy-prod
undeploy-prod: kustomize ## Remove resources from production environment.
	@echo "🗑️  Removing leibrix from production environment..."
	$(KUSTOMIZE) build config/overlays/prod | kubectl delete --ignore-not-found=$(ignore-not-found) -f -
	@echo "✅ Successfully removed from production environment"
	@echo "📋 Namespace: leibrix-system"

.PHONY: undeploy-rbac
undeploy-rbac: kustomize ## Remove RBAC resources from the K8s cluster.
	@echo "Removing RBAC resources..."
	$(KUSTOMIZE) build config/rbac | kubectl delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: undeploy-all
undeploy-all: undeploy undeploy-rbac ## Remove all resources from the K8s cluster.

.PHONY: undeploy-all-dev
undeploy-all-dev: undeploy-dev undeploy-rbac ## Remove all resources from development environment.

.PHONY: undeploy-all-prod
undeploy-all-prod: undeploy-prod undeploy-rbac ## Remove all resources from production environment.

.PHONY: redeploy
redeploy: undeploy deploy ## Redeploy to the K8s cluster (undeploy then deploy).

.PHONY: redeploy-all
redeploy-all: undeploy-all deploy-all ## Redeploy all resources to the K8s cluster.

.PHONY: redeploy-dev
redeploy-dev: undeploy-dev deploy-dev ## Redeploy to development environment.

.PHONY: redeploy-prod
redeploy-prod: undeploy-prod deploy-prod ## Redeploy to production environment.

.PHONY: redeploy-all-dev
redeploy-all-dev: undeploy-all-dev deploy-all-dev ## Redeploy all resources to development environment.

.PHONY: redeploy-all-prod
redeploy-all-prod: undeploy-all-prod deploy-all-prod ## Redeploy all resources to production environment.

##@ K3D Development

.PHONY: k3d-build-push
k3d-build-push: docker-build ## Build and push image to k3d registry for development.
	@echo "Tagging and pushing image to k3d registry..."
	docker tag $(IMG) k3d-leibrix-registry:5000/leibrix:latest
	docker push k3d-leibrix-registry:5000/leibrix:latest

.PHONY: k3d-deploy
k3d-deploy: k3d-build-push deploy-all-dev ## Build, push to k3d registry and deploy to development.

##@ Legacy Support

.PHONY: rbac
rbac: deploy-rbac ## Alias for deploy-rbac (backward compatibility).

.PHONY: uninstall-local
uninstall-local: kustomize  ## Uninstall resources from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	@if [ -z "$(USE_DEV_NS)" ]; then \
		$(KUSTOMIZE) build config/rbac | kubectl delete --ignore-not-found=$(ignore-not-found) -f - && \
		$(KUSTOMIZE) build config/overlays/$(OVERLAY) | kubectl delete --ignore-not-found=$(ignore-not-found) -f - ; \
	else \
		echo "Uninstalling resources from local tmp" && \
		$(KUSTOMIZE) build tmp/rbac | kubectl delete --ignore-not-found=$(ignore-not-found) -f - && \
		$(KUSTOMIZE) build tmp/config/overlays/$(OVERLAY) | kubectl delete --ignore-not-found=$(ignore-not-found) -f - ; \
	fi

##@ Tools

## Tool Versions
KUSTOMIZE_VERSION ?= v4.5.7
CONTROLLER_TOOLS_VERSION ?= v0.11.1
PROTOC_VERSION ?= 32.0
PROTOC_GEN_GO_VERSION ?= v1.36.8
PROTOC_GEN_GO_GRPC_VERSION ?= v1.5.1

KUSTOMIZE_INSTALL_SCRIPT ?= "https://raw.githubusercontent.com/kubernetes-sigs/kustomize/master/hack/install_kustomize.sh"
.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary. If wrong version is installed, it will be removed before downloading.
$(KUSTOMIZE): $(LOCALBIN)
	@if test -x $(LOCALBIN)/kustomize && ! $(LOCALBIN)/kustomize version | grep -q $(KUSTOMIZE_VERSION); then \
		echo "$(LOCALBIN)/kustomize version is not expected $(KUSTOMIZE_VERSION). Removing it before installing."; \
		rm -rf $(LOCALBIN)/kustomize; \
	fi
	test -s $(LOCALBIN)/kustomize || { curl -Ss $(KUSTOMIZE_INSTALL_SCRIPT) | bash -s -- $(subst v,,$(KUSTOMIZE_VERSION)) $(LOCALBIN); }

.PHONY: envtest
envtest: $(ENVTEST) ## Download envtest-setup locally if necessary.
$(ENVTEST): $(LOCALBIN)
	test -s $(LOCALBIN)/setup-envtest || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

.PHONY: protoc
protoc: $(PROTOC) ## Download protoc locally if necessary. If wrong version is installed, it will be removed before downloading.
$(PROTOC): $(LOCALBIN)
	@if test -x $(LOCALBIN)/protoc && ! $(LOCALBIN)/protoc --version | grep -q $(PROTOC_VERSION); then \
		echo "$(LOCALBIN)/protoc version is not expected $(PROTOC_VERSION). Removing it before installing."; \
		rm -rf $(LOCALBIN)/protoc $(LOCALBIN)/include; \
	fi
	@if ! test -s $(LOCALBIN)/protoc; then \
		echo "Downloading protoc $(PROTOC_VERSION)..."; \
		case $$(uname) in \
			Darwin) \
				case $$(uname -m) in \
					x86_64) PLATFORM=osx-x86_64 ;; \
					arm64) PLATFORM=osx-aarch_64 ;; \
					*) echo "Unsupported macOS architecture: $$(uname -m)"; exit 1 ;; \
				esac ;; \
			Linux) \
				case $$(uname -m) in \
					x86_64) PLATFORM=linux-x86_64 ;; \
					aarch64) PLATFORM=linux-aarch_64 ;; \
					*) echo "Unsupported Linux architecture: $$(uname -m)"; exit 1 ;; \
				esac ;; \
			*) echo "Unsupported OS: $$(uname)"; exit 1 ;; \
		esac; \
		curl -L -o $(LOCALBIN)/protoc.zip \
			"https://github.com/protocolbuffers/protobuf/releases/download/v$(PROTOC_VERSION)/protoc-$(PROTOC_VERSION)-$$PLATFORM.zip"; \
		cd $(LOCALBIN) && unzip -o protoc.zip && rm protoc.zip; \
		chmod +x $(LOCALBIN)/bin/protoc; \
		mv $(LOCALBIN)/bin/protoc $(LOCALBIN)/protoc; \
		rmdir $(LOCALBIN)/bin 2>/dev/null || true; \
	fi

.PHONY: protoc-gen-go
protoc-gen-go: $(PROTOC_GEN_GO) ## Download protoc-gen-go locally if necessary.
$(PROTOC_GEN_GO): $(LOCALBIN)
	test -s $(LOCALBIN)/protoc-gen-go || GOBIN=$(LOCALBIN) go install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)

.PHONY: protoc-gen-go-grpc
protoc-gen-go-grpc: $(PROTOC_GEN_GO_GRPC) ## Download protoc-gen-go-grpc locally if necessary.
$(PROTOC_GEN_GO_GRPC): $(LOCALBIN)
	test -s $(LOCALBIN)/protoc-gen-go-grpc || GOBIN=$(LOCALBIN) go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC_VERSION)t 