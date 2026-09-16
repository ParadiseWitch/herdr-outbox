BINARY   := herdr-outbox
CMD      := ./cmd/herdr-outbox
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)

GOOS   ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

.PHONY: build install uninstall clean test lint run server

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY)$(if $(filter windows,$(GOOS)),.exe) $(CMD)

install: build
	@mkdir -p $(HOME)/.local/bin
	@cp $(BINARY)$(if $(filter windows,$(GOOS)),.exe) $(HOME)/.local/bin/
	@chmod +x $(HOME)/.local/bin/$(BINARY)$(if $(filter windows,$(GOOS)),.exe)
	@echo "Installed to $(HOME)/.local/bin/$(BINARY)"

uninstall:
	@rm -f $(HOME)/.local/bin/$(BINARY) $(HOME)/.local/bin/$(BINARY).exe
	@echo "Uninstalled $(BINARY)"

clean:
	@rm -f $(BINARY) $(BINARY).exe $(BINARY).exe~
	@rm -rf dist/

test:
	go test ./...

lint:
	go vet ./...

run: build
	./$(BINARY)$(if $(filter windows,$(GOOS)),.exe)

server: build
	./$(BINARY)$(if $(filter windows,$(GOOS)),.exe) server

# Cross-compilation targets
.PHONY: dist dist-linux dist-darwin dist-windows

dist: dist-linux dist-darwin dist-windows

dist-linux:
	@mkdir -p dist
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-linux-amd64 $(CMD)
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-linux-arm64 $(CMD)

dist-darwin:
	@mkdir -p dist
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-darwin-amd64 $(CMD)
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-darwin-arm64 $(CMD)

dist-windows:
	@mkdir -p dist
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-windows-amd64.exe $(CMD)
