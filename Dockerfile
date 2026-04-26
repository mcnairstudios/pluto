FROM golang:1.24-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.buildVersion=$VERSION" -o /pluto .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /pluto /usr/local/bin/pluto

EXPOSE 8080

ENTRYPOINT ["pluto"]
