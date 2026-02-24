BINARY=opik-logger
PLATFORMS=darwin/arm64 darwin/amd64 linux/amd64 windows/amd64

.PHONY: build clean build-local e2e integration benchmark

build:
	@mkdir -p bin
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		echo "Building $$os/$$arch..."; \
		GOOS=$$os GOARCH=$$arch go build -ldflags="-s -w" -o bin/$(BINARY)-$$os-$$arch$$ext ./src; \
	done
	@echo "Done. Binaries in bin/"

build-local:
	@mkdir -p bin
	@go build -o bin/$(BINARY)-$$(uname -s | tr '[:upper:]' '[:lower:]')-$$(uname -m | sed 's/x86_64/amd64/' | sed 's/aarch64/arm64/') ./src
	@echo "Built local binary"

clean:
	rm -rf bin/

e2e:
	./test/e2e-test.sh

integration:
	./test/integration-test.sh

benchmark:
	./test/benchmark.sh
