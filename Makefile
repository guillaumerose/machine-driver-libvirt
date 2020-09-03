PREFIX=/go
CMD=crc-driver-libvirt
GO_VERSION=1.13.4
DESCRIBE=$(shell git describe --tags)

TARGETS=$(addprefix $(CMD)-, centos8 ubuntu20.04)

build: $(TARGETS)

$(CMD)-%: Dockerfile.%
	docker rmi -f $@ >/dev/null  2>&1 || true
	docker rm -f $@-extract > /dev/null 2>&1 || true
	echo "Building binaries for $@"
	docker build --build-arg "GO_VERSION=$(GO_VERSION)" -t $@ -f $< .
	docker create --name $@-extract $@ sh
	docker cp $@-extract:$(PREFIX)/bin/$(CMD) ./
	mv ./$(CMD) ./$@
	docker rm $@-extract || true
	docker rmi $@ || true

clean:
	rm -f ./$(CMD)-*

release: build
	@echo "Paste the following into the release page on github and upload the binaries..."
	@echo ""
	@for bin in $(CMD)-* ; do \
	    target=$$(echo $${bin} | cut -f5- -d-) ; \
	    md5=$$(md5sum $${bin}) ; \
	    echo "* $${target} - md5: $${md5}" ; \
	    echo '```' ; \
	    echo "  curl -L https://github.com/dhiltgen/docker-machine-kvm/releases/download/$(DESCRIBE)/$${bin} > /usr/local/bin/$(CMD) \\ " ; \
	    echo "  chmod +x /usr/local/bin/$(CMD)" ; \
	    echo '```' ; \
	done

.PHONY: validate
validate: test lint

test:
	go test ./...

.PHONY: lint
lint:
	golangci-lint run
