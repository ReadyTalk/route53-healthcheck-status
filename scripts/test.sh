docker run \
  --rm \
  -it \
  -e AWS_ACCESS_KEY_ID_FETCH="${AWS_ACCESS_KEY_ID_FETCH}" \
  -e AWS_SECRET_ACCESS_KEY_FETCH="${AWS_SECRET_ACCESS_KEY_FETCH}" \
  -e AWS_ACCESS_KEY_ID_POST="${AWS_ACCESS_KEY_ID_POST}" \
  -e AWS_SECRET_ACCESS_KEY_POST="${AWS_SECRET_ACCESS_KEY_POST}" \
  -e CONFIG_PATH="/config.json" \
  -v $(pwd)/config.json:/config.json \
  -v $(pwd)/..:/go/src/github.com/readytalk/route53-healthcheck-status \
  -w /go/src/github.com/readytalk/route53-healthcheck-status \
  golang:1.10.2-alpine sh -c "go run main.go"
