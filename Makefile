.PHONY: build local relay connect clean frontend

build: frontend bin/pairpad

frontend:
	cd web && npm run build

bin/pairpad: frontend $(shell find cmd internal -name '*.go')
	go build -o bin/pairpad ./cmd/pairpad

# Development shortcuts
local: bin/pairpad
	./bin/pairpad local

relay: bin/pairpad
	./bin/pairpad relay

connect: bin/pairpad
	./bin/pairpad connect --server ws://localhost:8080

clean:
	rm -rf bin/
