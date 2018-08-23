FROM golang:1.10.2-alpine as builder

ARG VERSION=master
ARG GOOS=linux

# RUN apk update && apk --no-cache add curl git

WORKDIR /go/src/github.com/readytalk/route53-healthcheck-status/
COPY main.go .
COPY vendor vendor
RUN ls
RUN CGO_ENABLED=0 GOOS=${GOOS} go build -ldflags "-X main.version=${VERSION}" -v -a -o route53-healthcheck-status .

# CMD ["/bin/sh", "-c", "route53-status-client"]

# Stage 2

FROM alpine:latest

RUN apk --no-cache add ca-certificates

ENV CONFIG_PATH=/config.json
ENV RUN_INTERVAL=15000

COPY --from=builder /go/src/github.com/readytalk/route53-healthcheck-status/ /usr/bin

CMD ["route53-healthcheck-status"]
