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
test_dir=$(CDPATH= cd -- "$test_dir" && pwd)
cleanup() { rm -rf "$test_dir"; }
trap cleanup EXIT

mkdir -p "$test_dir/bin"
mkdir -p "$test_dir/host-go"
mkdir -p "$test_dir/.toolchains/go1.26.0.linux-amd64/bin"
printf 'go1.26.0\n' > "$test_dir/.toolchains/go1.26.0.linux-amd64/VERSION"
cat > "$test_dir/.toolchains/go1.26.0.linux-amd64/bin/go" <<'SH'
#!/bin/sh
echo 'go version go1.26.0 linux/amd64'
SH
chmod +x "$test_dir/.toolchains/go1.26.0.linux-amd64/bin/go"
cat > "$test_dir/bin/go" <<EOF
#!/bin/sh
case "\$1:\$2" in
    env:GOROOT) printf '%s\n' '$test_dir/host-go' ;;
    env:GOHOSTOS) printf '%s\n' 'darwin' ;;
    env:GOHOSTARCH) printf '%s\n' 'arm64' ;;
    env:GOVERSION) printf '%s\n' 'go1.25.0' ;;
    version:$test_dir/.toolchains/go1.26.0.linux-amd64/bin/go)
        printf '%s\n' 'go version go1.26.0 linux/amd64'
        ;;
    *) exit 1 ;;
esac
EOF
chmod +x "$test_dir/bin/go"
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

# A host linux/amd64 Go installation must also match the pinned version. A
# mismatched host toolchain should fail before Docker starts when no valid
# cached toolchain is available.
: > "$docker_args_log"
cat > "$test_dir/bin/go" <<EOF
#!/bin/sh
case "\$1:\$2" in
    env:GOROOT) printf '%s\n' '$test_dir/host-go' ;;
    env:GOHOSTOS) printf '%s\n' 'linux' ;;
    env:GOHOSTARCH) printf '%s\n' 'amd64' ;;
    env:GOVERSION) printf '%s\n' 'go1.25.0' ;;
    *) exit 1 ;;
esac
EOF
chmod +x "$test_dir/bin/go"

if (
    cd "$test_dir"
    AIDEN_GO_ROOT="$test_dir/missing-go-cache" \
        PATH="$test_dir/bin:$PATH" \
        "$BUILD_IMAGE_SH"
) > "$test_dir/wrong-go.log" 2>&1; then
    echo "build_image.sh accepted a mismatched host Go version" >&2
    exit 1
fi
if [ -s "$docker_args_log" ]; then
    echo "build_image.sh invoked Docker before rejecting a mismatched host Go version" >&2
    exit 1
fi
if ! grep -Fq 'detected go1.25.0 linux/amd64' "$test_dir/wrong-go.log"; then
    echo "build_image.sh did not report the mismatched host Go version clearly" >&2
    cat "$test_dir/wrong-go.log" >&2
    exit 1
fi

# A cache with the pinned VERSION file but a binary for the wrong target must
# also be rejected before Docker starts.
: > "$docker_args_log"
mkdir -p "$test_dir/wrong-target-go/bin"
printf 'go1.26.0\n' > "$test_dir/wrong-target-go/VERSION"
cat > "$test_dir/wrong-target-go/bin/go" <<'SH'
#!/bin/sh
echo 'wrong target'
SH
chmod +x "$test_dir/wrong-target-go/bin/go"
cat > "$test_dir/bin/go" <<EOF
#!/bin/sh
case "\$1:\$2" in
    env:GOROOT) printf '%s\n' '$test_dir/host-go' ;;
    env:GOHOSTOS) printf '%s\n' 'linux' ;;
    env:GOHOSTARCH) printf '%s\n' 'amd64' ;;
    env:GOVERSION) printf '%s\n' 'go1.25.0' ;;
    version:$test_dir/wrong-target-go/bin/go)
        printf '%s\n' 'go version go1.26.0 darwin/arm64'
        ;;
    *) exit 1 ;;
esac
EOF
chmod +x "$test_dir/bin/go"

if (
    cd "$test_dir"
    AIDEN_GO_ROOT="$test_dir/wrong-target-go" \
        PATH="$test_dir/bin:$PATH" \
        "$BUILD_IMAGE_SH"
) > "$test_dir/wrong-target-go.log" 2>&1; then
    echo "build_image.sh accepted a cached Go binary for the wrong target" >&2
    exit 1
fi
if [ -s "$docker_args_log" ]; then
    echo "build_image.sh invoked Docker before rejecting the wrong cached Go target" >&2
    exit 1
fi
if ! grep -Fq 'go version go1.26.0 darwin/arm64' "$test_dir/wrong-target-go.log"; then
    echo "build_image.sh did not report the cached Go target mismatch clearly" >&2
    cat "$test_dir/wrong-target-go.log" >&2
    exit 1
fi

echo "build_image.sh Go proxy forwarding test passed"
