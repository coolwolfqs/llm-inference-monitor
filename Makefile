.PHONY: build run clean test dev docker frontend

build:
	@set -eu; \
	commit=$$(git rev-parse --short HEAD); \
	tree=$$(git rev-parse HEAD^{tree}); \
	state=clean; test -z "$$(git status --porcelain --untracked-files=all)" || state=dirty; \
	built=$$(date -u +%Y-%m-%dT%H:%M:%SZ); \
	cd src/gateway && go build -trimpath -ldflags "-X main.buildCommit=$$commit -X main.buildTree=$$tree -X main.buildTime=$$built -X main.buildState=$$state" -o ../../inference-hub-v3.new .; \
	test -x ../../inference-hub-v3.new; \
	mv -f ../../inference-hub-v3.new ../../inference-hub-v3

run: build
	./inference-hub-v3

dev:
	cd src/gateway && go run main.go

clean:
	rm -f inference-hub-v3
	rm -rf frontend/dist
	rm -rf frontend/node_modules

test:
	go test -v ./src/...

frontend:
	cd frontend && npm install && npm run build

docker:
	docker-compose up -d --build

docker-stop:
	docker-compose down

docker-logs:
	docker-compose logs -f inference-hub

deps:
	GOPROXY=https://goproxy.cn,direct go mod tidy
	cd frontend && npm install

fmt:
	go fmt ./src/...

lint:
	golangci-lint run ./src/...
