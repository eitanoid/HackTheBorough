FROM golang:1.27.3-alpine3.24 AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/backend ./cmd/server

FROM alpine:3.24.4 AS runtime

# Python runs the generated statistical scripts. py3-pandas also installs numpy.
RUN apk add --no-cache ca-certificates python3 py3-pandas \
    && addgroup -S app \
    && adduser -S -G app app \
    && mkdir -p /app \
    && chown app:app /app

WORKDIR /app
COPY --from=builder --chown=app:app /out/backend /app/backend

ENV PYTHON_BIN=python3
EXPOSE 8080
USER app
ENTRYPOINT ["/app/backend"]
