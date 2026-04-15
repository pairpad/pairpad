.PHONY: build server daemon clean

build: bin/server bin/pairpad

bin/server: $(shell find cmd/server internal -name '*.go') $(shell find internal/server/static -type f)
	go build -o bin/server ./cmd/server

bin/pairpad: $(shell find cmd/pairpad internal -name '*.go')
	go build -o bin/pairpad ./cmd/pairpad

server: bin/server
	./bin/server

daemon: bin/pairpad
	PAIRPAD_SERVER=ws://localhost:8080 ./bin/pairpad serve

clean:
	rm -rf bin/
