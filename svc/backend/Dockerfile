FROM golang:1.27.3-alpine

# Python for the dev stand-in script runner (ANALYSIS_DEBUG). pandas pulls in numpy.
RUN apk add --no-cache python3 py3-pandas

RUN go install github.com/air-verse/air@v1.61.7

WORKDIR /app
COPY go.mod go.sum* ./
RUN go mod download
COPY . .

EXPOSE 8080
CMD ["air", "-c", ".air.toml"]
