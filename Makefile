IMAGE_REPO   ?= ghcr.io/sradco/csi-volume-device-exporter
IMAGE_TAG    ?= latest
BINARY       := bin/csi-volume-device-exporter
VERSION      ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT       ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS      := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)
CRI          ?= podman
.PHONY: build generate test test-e2e test-alerts image push deploy deploy-podmonitor deploy-openshift clean lint vet

build:
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/exporter

# Re-generate alert YAML files from the Go definitions in pkg/monitoring/rules/.
# Run this whenever alert rules change; commit the output alongside the code.
# Pass NAMESPACE=<ns> to embed a real namespace in alerts.yaml, e.g.:
#   make generate NAMESPACE=kubevirt
NAMESPACE ?=
generate:
	go run ./tools/generate-rules -namespace=$(NAMESPACE)

test:
	go test -race -count=1 ./pkg/... ./cmd/... ./tools/...

test-e2e: build
	go test -tags=e2e -v -timeout=60s ./test/e2e/

# Lint Prometheus rules and run unit tests.
# 1. Go tests: validate alert structure (required fields, runbook_url, etc.)
# 2. promtool: lint rule syntax and run scenario-based unit tests.
# Requires podman or docker (set CRI=docker if needed).
test-alerts:
	go test -race -count=1 ./pkg/monitoring/...
	hack/prom-rule-ci/verify-rules.sh

image:
	$(CRI) build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		-t $(IMAGE_REPO):$(IMAGE_TAG) .

push: image
	$(CRI) push $(IMAGE_REPO):$(IMAGE_TAG)

deploy:
	kubectl apply -f deploy/daemonset.yaml

# Deploy the PodMonitor for Prometheus Operator-based clusters.
# On OpenShift, deploy into a namespace with openshift.io/cluster-monitoring=true
# (e.g. openshift-cnv) so the platform Prometheus scrapes it.
deploy-podmonitor:
	kubectl apply -f deploy/podmonitor.yaml

# Deploy to OpenShift in the recommended namespace (openshift-cnv).
# openshift-cnv already carries openshift.io/cluster-monitoring=true so the
# platform Prometheus scrapes the exporter alongside node-exporter, enabling
# the PromQL join with node_dmmultipath_path_state.
OPENSHIFT_NAMESPACE ?= openshift-cnv
deploy-openshift:
	kubectl apply -n $(OPENSHIFT_NAMESPACE) -f deploy/scc.yaml
	kubectl apply -n $(OPENSHIFT_NAMESPACE) -f deploy/daemonset.yaml
	kubectl apply -n $(OPENSHIFT_NAMESPACE) -f deploy/podmonitor.yaml

clean:
	rm -rf bin/

lint:
	golangci-lint run ./...

vet:
	go vet ./...
