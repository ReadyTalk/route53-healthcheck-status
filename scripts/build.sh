#!/bin/sh

VERSION=${1:-dev}
GOOS=${2:-linux}
DOCKER_REPO="readytalk/route53-healthcheck-status"

# Directory to house our binaries
# mkdir -p bin

# Build the binary in Docker and extract it from the container
docker build --build-arg VERSION=${VERSION} --build-arg GOOS=${GOOS} -t ${DOCKER_REPO}:${VERSION}-${GOOS} ./
