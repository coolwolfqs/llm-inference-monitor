.PHONY: build run clean test dev docker frontend

build:
	cd src/gateway && go build -o ../../inference-hub-v3 .

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
