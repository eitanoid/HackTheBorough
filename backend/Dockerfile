FROM golang:1.27.3-alpine

RUN go install github.com/air-verse/air@v1.61.7

WORKDIR /app
COPY go.mod ./
RUN go mod download
COPY . .

EXPOSE 8080
CMD ["air", "-c", ".air.toml"]
