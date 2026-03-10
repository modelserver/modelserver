FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /modelserver ./cmd/modelserver

FROM alpine:3.21
RUN apk --no-cache add ca-certificates
WORKDIR /app
COPY --from=builder /modelserver .
COPY config.example.yml ./config.yml
EXPOSE 8080 8081
ENTRYPOINT ["/app/modelserver"]
