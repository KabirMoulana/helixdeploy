IMG ?= ghcr.io/yourusername/helixdeploy:latest
ENVTEST_K8S_VERSION = 1.29.x

# Tool binaries
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

.PHONY: all
all: build

## ── Development ─────────────────────────────────────────────────────────────

.PHONY: fmt vet lint
fmt:
	go fmt ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

## ── Test ─────────────────────────────────────────────────────────────────────

.PHONY: test
test: manifests generate fmt vet envtest
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-path $(LOCALBIN) -p path)" \
	go test ./... -coverprofile cover.out -race

.PHONY: test-e2e
test-e2e:
	go test ./tests/e2e/... -v -tags=e2e

## ── Build ────────────────────────────────────────────────────────────────────

.PHONY: build
build: manifests generate fmt vet
	go build -o bin/manager cmd/controller/main.go

.PHONY: docker-build
docker-build:
	docker build -t ${IMG} .

.PHONY: docker-push
docker-push:
	docker push ${IMG}

## ── Code Generation ──────────────────────────────────────────────────────────

.PHONY: manifests
manifests: controller-gen
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd output:rbac:artifacts:config=config/rbac

.PHONY: generate
generate: controller-gen
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

## ── Cluster Bootstrap (kind) ─────────────────────────────────────────────────

.PHONY: bootstrap
bootstrap: ## Bootstrap a local kind cluster with all dependencies
	@echo "Creating kind cluster..."
	kind create cluster --name helixdeploy --config hack/kind-config.yaml || true

	@echo "Installing cert-manager..."
	kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml
	kubectl wait --for=condition=Available deployment/cert-manager-webhook -n cert-manager --timeout=120s

	@echo "Installing Tekton Pipelines..."
	kubectl apply -f https://storage.googleapis.com/tekton-releases/pipeline/latest/release.yaml
	kubectl wait --for=condition=Available deployment/tekton-pipelines-controller -n tekton-pipelines --timeout=120s

	@echo "Installing HelixDeploy CRDs..."
	kubectl apply -f config/crd/

	@echo "Deploying HelixDeploy controller..."
	kubectl apply -f config/rbac/
	kubectl apply -f config/manager/

	@echo "✅ HelixDeploy ready! Try: kubectl apply -f config/examples/my-service-pipeline.yaml"

.PHONY: teardown
teardown:
	kind delete cluster --name helixdeploy

## ── Tools ────────────────────────────────────────────────────────────────────

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN)
$(CONTROLLER_GEN): $(LOCALBIN)
	test -s $(LOCALBIN)/controller-gen && \
	$(LOCALBIN)/controller-gen --version | grep -q v0.14.0 || \
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.14.0

.PHONY: envtest
envtest: $(ENVTEST)
$(ENVTEST): $(LOCALBIN)
	test -s $(LOCALBIN)/setup-envtest || \
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

.PHONY: help
help: ## Print this help message
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'
