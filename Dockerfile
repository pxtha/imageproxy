# syntax=docker/dockerfile:1.4
FROM --platform=linux/amd64 cgr.dev/chainguard/wolfi-base as build
LABEL maintainer="Will Norris <will@willnorris.com>"

RUN apk update && apk add build-base git openssh go-1.20

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=1 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -v ./cmd/imageproxy -o imageproxy.bin

FROM alpine:3.8

COPY --from=build /app/imageproxy /app/imageproxy

RUN chmod +x /app/imageproxy/imageproxy.bin

EXPOSE 8222

RUN ls -la /app/imageproxy

CMD ["/app/imageproxy/imageproxy.bin"]