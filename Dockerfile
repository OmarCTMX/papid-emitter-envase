FROM golang:1.26-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/emitter .

FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S app && \
    adduser -S -G app app

ENV TZ=America/Mexico_City

WORKDIR /app
COPY --from=builder /out/emitter /app/emitter

EXPOSE 3200

USER app
ENTRYPOINT ["/app/emitter"]
