VERSION ?= 0.12.11
PREFIX=/go
CMD=crc-driver-libvirt
DESCRIBE=$(shell git describe --tags)
CONTAINER_RUNTIME ?= podman

TARGETS=$(addprefix $(CMD)-, centos8 ubuntu20.04)

build: $(TARGETS)

local:
	go build -v -ldflags="-s -w -X github.com/code-ready/machine-driver-libvirt/pkg/libvirt.DriverVersion=$(VERSION)" -o crc-driver-libvirt-local ./cmd/machine-driver-libvirt

$(CMD)-%: Containerfile.%
	${CONTAINER_RUNTIME} rmi -f $@ >/dev/null  2>&1 || true
	${CONTAINER_RUNTIME} rm -f $@-extract > /dev/null 2>&1 || true
	echo "Building binaries for $@"
	${CONTAINER_RUNTIME} build -t $@ -f $< .
	${CONTAINER_RUNTIME} create --name $@-extract $@ sh
	${CONTAINER_RUNTIME} cp $@-extract:$(PREFIX)/bin/$(CMD) ./
	mv ./$(CMD) ./$@
	${CONTAINER_RUNTIME} rm $@-extract || true
	${CONTAINER_RUNTIME} rmi $@ || true

clean:
	rm -f ./$(CMD)-*

release:
	goreleaser

.PHONY: validate
validate: test lint

.PHONY: test
test:
	go test ./...

.PHONY: lint
lint:
	golangci-lint run
