#!/bin/sh
set -eu

# build_image.sh spawns the Docker container in which `go build` actually runs.
# The workflow's "Configure Go proxy" step only exports GOPROXY on the host, so
# unless build_image.sh forwards the Go module proxy configuration into the
# container the build falls back to the default proxy.golang.org and cannot
# fetch modules from restricted networks. A fake docker on PATH captures the
# real `docker run` arguments to prove the variables are forwarded.

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BUILD_IMAGE_SH="$ROOT_DIR/build_image.sh"

test_dir=$(mktemp -d "${TMPDIR:-/tmp}/aiden-build-image-proxy-test.XXXXXX")
cleanup() { rm -rf "$test_dir"; }
trap cleanup EXIT

mkdir -p "$test_dir/bin"
docker_args_log="$test_dir/docker-args.log"
cat > "$test_dir/bin/docker" <<EOF
#!/bin/sh
for arg in "\$@"; do
    printf '%s\n' "\$arg" >> "$docker_args_log"
done
exit 0
EOF
chmod +x "$test_dir/bin/docker"

# Run from an isolated workdir so build_image.sh's ownership-restore step finds
# no build outputs to touch and cannot affect the real workspace.
(
    cd "$test_dir"
    GOPROXY="https://goproxy.cn" GOSUMDB="off" GONOSUMDB="example.com" \
        HTTPS_PROXY="http://proxy.example:8080" \
        PATH="$test_dir/bin:$PATH" \
        "$BUILD_IMAGE_SH"
) > "$test_dir/build.log" 2>&1 || true

if [ ! -s "$docker_args_log" ]; then
    echo "build_image.sh did not invoke docker run; cannot verify Go proxy forwarding" >&2
    echo "build_image.sh output:" >&2
    cat "$test_dir/build.log" >&2
    exit 1
fi

for expected in \
    'GOPROXY=https://goproxy.cn' \
    'GOSUMDB=off' \
    'GONOSUMDB=example.com' \
    'HTTPS_PROXY=http://proxy.example:8080'; do
    if ! grep -Fxq "$expected" "$docker_args_log"; then
        echo "build_image.sh must forward Go proxy env into the Docker build container: missing $expected" >&2
        exit 1
    fi
done

# Variables that are unset on the host must not be forwarded as empty values,
# which would override the container/default configuration with a blank string.
if grep -Eq '^(GOPRIVATE|GONOPROXY|ALL_PROXY)=$' "$docker_args_log"; then
    echo "build_image.sh must not forward unset proxy variables as empty values" >&2
    exit 1
fi

echo "build_image.sh Go proxy forwarding test passed"
