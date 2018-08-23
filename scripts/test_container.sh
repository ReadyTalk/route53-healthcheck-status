#!/bin/bash

docker run \
  --rm \
  -it \
  -e AWS_ACCESS_KEY_ID_FETCH="${AWS_ACCESS_KEY_ID_FETCH}" \
  -e AWS_SECRET_ACCESS_KEY_FETCH="${AWS_SECRET_ACCESS_KEY_FETCH}" \
  -e AWS_ACCESS_KEY_ID_POST="${AWS_ACCESS_KEY_ID_POST}" \
  -e AWS_SECRET_ACCESS_KEY_POST="${AWS_SECRET_ACCESS_KEY_POST}" \
  -e CONFIG_PATH="/config.json" \
  -e RUN_INTERVAL=5000 \
  -v $(pwd)/config.json:/config.json \
  readytalk/route53-healthcheck-status
