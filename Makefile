BINARY  = intercept
VERSION = $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  = $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS = -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

HOSTOS   = $(shell go env GOOS)
HOSTARCH = $(shell go env GOARCH)

PLATFORMS = linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64 windows-arm64

.PHONY: build all dist clean test lint $(PLATFORMS)

build:
	CGO_ENABLED=0 GOOS=$(HOSTOS) GOARCH=$(HOSTARCH) \
		go build -ldflags '$(LDFLAGS)' -o dist/$(HOSTOS)_$(HOSTARCH)/$(BINARY) .

all: $(PLATFORMS)

define build-platform
$(1):
	$$(eval OS := $$(word 1,$$(subst -, ,$(1))))
	$$(eval ARCH := $$(word 2,$$(subst -, ,$(1))))
	$$(eval EXT := $$(if $$(filter windows,$$(OS)),.exe,))
	CGO_ENABLED=0 GOOS=$$(OS) GOARCH=$$(ARCH) \
		go build -ldflags '$$(LDFLAGS)' -o dist/$$(OS)_$$(ARCH)/$$(BINARY)$$(EXT) .
endef

$(foreach platform,$(PLATFORMS),$(eval $(call build-platform,$(platform))))

dist: all
	@for dir in dist/*/; do \
		name=$$(basename $$dir); \
		os=$$(echo $$name | cut -d_ -f1); \
		arch=$$(echo $$name | cut -d_ -f2); \
		if [ "$$os" = "windows" ]; then \
			(cd $$dir && zip -q ../../dist/intercept-$$os-$$arch.zip *); \
		else \
			tar -czf dist/intercept-$$os-$$arch.tar.gz -C $$dir .; \
		fi \
	done
	@cd dist && sha256sum intercept-*.tar.gz intercept-*.zip > checksums.txt

clean:
	rm -rf dist/

test:
	go test ./...

lint:
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run || echo "golangci-lint not installed, skipping"
