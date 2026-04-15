.PHONY: build server daemon clean frontend

build: frontend bin/server bin/pairpad

frontend:
	cd web && npm run build

bin/server: frontend $(shell find cmd/server internal -name '*.go')
	go build -o bin/server ./cmd/server

bin/pairpad: $(shell find cmd/pairpad internal -name '*.go')
	go build -o bin/pairpad ./cmd/pairpad

server: bin/server
	./bin/server

daemon: bin/pairpad
	PAIRPAD_SERVER=ws://localhost:8080 ./bin/pairpad serve

clean:
	rm -rf bin/
