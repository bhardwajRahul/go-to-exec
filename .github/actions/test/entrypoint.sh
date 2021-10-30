#!/usr/bin/env bash
set -Eeumo pipefail

QV_DEBUG=false go test -v ./...

# Run AWS SNS testing
ngrok authtoken "$NGROK_AUTH_TOKEN"
bash ./hack/test-awssns/ngrok.sh