FROM golang:1.24-alpine AS builder
RUN apk add --no-cache gcc musl-dev opus-dev opusfile-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -o stackchan .

FROM alpine:latest
RUN apk add --no-cache opus opusfile
WORKDIR /app
COPY --from=builder /src/stackchan .
CMD ["./stackchan"]
