.DEFAULT: all
.PHONY: all clean image minikube-publish manifest test kured-all lint 

DH_ORG ?= kubereboot
VERSION=$(shell git rev-parse --short HEAD)
SUDO=$(shell docker info >/dev/null 2>&1 || echo "sudo -E")
KO_DOCKER_REPO=ghcr.io/$(DH_ORG)/kured

all: image

.PHONY: install-tools
install-tools:
	command -v  mise 2>&1 || { echo "please install mise to continue" >&2; exit 127; }
	mise install

image: kured
	ko build --local

dev-image: kured
	$(SUDO) docker buildx build --no-cache --load -t ghcr.io/kubereboot/kured:dev -f Dockerfile.kured .
	$(SUDO) docker buildx build --no-cache --load -t ghcr.io/kubereboot/node-reboot-detector:dev -f Dockerfile.node-reboot-detector .
	$(SUDO) docker buildx build --no-cache --load -t ghcr.io/kubereboot/node-reboot-reporter:dev -f Dockerfile.node-reboot-reporter .
	$(SUDO) docker buildx build --no-cache --load -t ghcr.io/kubereboot/node-maintenance-scheduler:dev -f Dockerfile.node-maintenance-scheduler .


e2e-test: dev-image
	echo "Running ALL go tests"
	go test -count=1 -v --parallel 4 ./... $(ARGS)

# todo: improve minikube handling
minikube-publish: image
	$(SUDO) docker save ghcr.io/$(DH_ORG)/kured | (eval $$(minikube docker-env) && docker load)

# todo: update manifest to add other releasable images
manifest:
	sed -i "s#image: ghcr.io/.*kured.*#image: ghcr.io/$(DH_ORG)/kured:$(VERSION)#g" kured-ds.yaml
	sed -i "s#image: ghcr.io/.*kured.*#image: ghcr.io/$(DH_ORG)/kured:$(VERSION)#g" kured-ds-signal.yaml
	echo "Please generate combined manifest if necessary"

test: lint
	@echo "Running short go tests"
	go test -test.short -json ./... > test.json

lint:
	@echo "Running shellcheck"
	find . -name '*.sh' | xargs -n1 shellcheck
	@echo "Running golangci-lint..."
	golangci-lint run ./...

lint-docs:
	@echo "Running lychee"
	mise x lychee@latest -- lychee --verbose --no-progress '*.md' '*.yaml' '*/*/*.go' --exclude-link-local