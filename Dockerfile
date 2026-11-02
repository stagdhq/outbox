# outbox as an image: nothing but the binary, CA certificates and time
# zones, as a user without privileges. It answers on 8080 (Resend, /texts)
# and 4566 (Amazon SES and SNS).
#
#   docker build -t outbox .

# The build runs on the builder's platform and cross-compiles for the image's.
FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /outbox ./cmd/outbox

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /outbox /outbox
COPY LICENSE /usr/share/licenses/outbox/LICENSE

EXPOSE 8080 4566

# Up once both ports answer.
HEALTHCHECK --interval=5s --timeout=5s --start-period=10s --retries=3 CMD ["/outbox", "health"]

ENTRYPOINT ["/outbox"]
CMD ["serve"]
