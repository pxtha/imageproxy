FROM --platform=linux/amd64 cgr.dev/chainguard/wolfi-base as build

RUN apk update && apk add build-base git openssh go-1.20

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -v ./cmd/imageproxy

FROM cgr.dev/chainguard/static:latest

COPY --from=build /app/imageproxy /app/imageproxy

CMD ["-addr", "0.0.0.0:8222"]
ENTRYPOINT ["/app/imageproxy"]

EXPOSE 8222
