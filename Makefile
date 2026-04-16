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
	PAIRPAD_SERVER=ws://localhost:8080 ./bin/pairpad connect

clean:
	rm -rf bin/
