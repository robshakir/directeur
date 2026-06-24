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

## docker-build: Build the docker image
docker-build:
	docker build -t directeur .

## docker-run: Run the server in a docker container mounting ~/.directeur
docker-run:
	docker run -d --rm -p 8080:8080 -v ~/.directeur:/root/.directeur --name directeur-server directeur

## docker-stop: Stop the running docker container
docker-stop:
	docker stop directeur-server

## help: Show this help message
help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@fgrep -h "##" $(MAKEFILE_LIST) | fgrep -v fgrep | sed -e 's/\\$$//' | sed -e 's/##//'
