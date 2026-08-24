FROM golang:1.25-alpine AS builder
# git lets the Go toolchain embed VCS revision/time into the binary,
# so `homelabmon version` reports the real commit even without ldflags.
RUN apk add --no-cache git
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags "\
    -X github.com/dx111ge/homelabmon/cmd.Version=${VERSION} \
    -X github.com/dx111ge/homelabmon/cmd.Commit=${COMMIT} \
    -X github.com/dx111ge/homelabmon/cmd.BuildDate=${BUILD_DATE}" \
    -o /homelabmon .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /homelabmon /usr/local/bin/homelabmon
VOLUME /data
ENV HOME=/data
EXPOSE 9600
ENTRYPOINT ["homelabmon"]
# Default: UI + monitoring only. Add --scan if running with host networking.
CMD ["--ui", "--bind", ":9600"]
