export GIT_TAG=$(git describe --tags --exact-match 2>/dev/null || echo dev)
export GIT_COMMIT=$(git rev-parse HEAD)
