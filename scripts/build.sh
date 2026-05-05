#!/usr/bin/env bash
set -euo pipefail

. scripts/helpers/git-env.sh

LDFLAGS="-X \"github.com/docker/docker-agent/pkg/version.Version=${GIT_TAG}\" -X \"github.com/docker/docker-agent/pkg/version.Commit=${GIT_COMMIT}\""

BINARY_NAME="docker-agent"
case "$OSTYPE" in
  msys*|cygwin*) BINARY_NAME="${BINARY_NAME}.exe" ;;
esac

(
  set -x
  go build -v -ldflags "$LDFLAGS" -o ./bin/${BINARY_NAME} ./main.go
)
echo "Built ./bin/${BINARY_NAME}"

if [ "${CI:-}" != "true" ]; then
  ln -sf "$(pwd)/bin/${BINARY_NAME}" "${HOME}/bin/${BINARY_NAME}" 2>/dev/null || true
fi
