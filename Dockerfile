FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY src/ ./src/

RUN CGO_ENABLED=0 GOOS=linux go build -o /app/inference-hub-v3 ./src/gateway/

# Frontend build stage
FROM node:20-alpine AS frontend-builder

WORKDIR /app/frontend
COPY frontend/package*.json ./
RUN npm install
COPY frontend/ ./
RUN npm run build

# Production stage
FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/inference-hub-v3 /app/inference-hub-v3
COPY --from=frontend-builder /app/frontend/dist /app/static
COPY configs/ /app/configs/

RUN apk add --no-cache ca-certificates

EXPOSE 9092

CMD ["/app/inference-hub-v3"]
