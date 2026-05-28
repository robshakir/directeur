# Makefile for directeur

.PHONY: all build clean run serve fmt vet help

# Default target
all: build

## build: Build the directeur binary
build:
	go build -o directeur directeur.go

## clean: Remove build artifacts
clean:
	rm -f directeur

## run: Run the binary with default inputs
run: build
	./directeur -input example.fit -config config.json

## serve: Start the local dashboard server on http://localhost:8080/
serve: build
	./directeur -serve -port 8080 -config config.json

## fmt: Format Go source files
fmt:
	go fmt ./...

## vet: Run go vet on source files
vet:
	go vet ./...

## help: Show this help message
help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@fgrep -h "##" $(MAKEFILE_LIST) | fgrep -v fgrep | sed -e 's/\\$$//' | sed -e 's/##//'
