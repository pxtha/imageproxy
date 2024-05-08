# Use an Alpine-based image to build the Go binary
FROM --platform=linux/amd64 golang:1.20-alpine as build

# Install necessary build tools
RUN apk update && apk add build-base git openssh

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -v -ldflags '-extldflags "-static"' -o imageproxy.bin ./cmd/imageproxy

# Use an Alpine-based image to run the Go binary
FROM alpine:3.8

COPY --from=build /app/imageproxy.bin /app/imageproxy.bin

CMD ["-addr", "0.0.0.0:8222"]

ENTRYPOINT ["/app/imageproxy.bin"]

EXPOSE 8222