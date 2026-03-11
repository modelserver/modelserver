FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /payserver ./cmd/payserver

FROM alpine:3.23
RUN apk --no-cache add ca-certificates
WORKDIR /app
COPY --from=builder /payserver .
COPY config.example.yml ./config.yml
EXPOSE 8090
ENTRYPOINT ["/app/payserver"]
