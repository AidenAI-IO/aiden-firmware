# Test image

`Dockerfile` is the single `linux/amd64` toolchain image for the manifest
runner. The explicit platform is intentional: the pinned Go and Node binaries
are amd64, while the ARM compiler targets the device. It pins the Debian base
digest and Go/Node toolchains, installs the C++/ARM/Python tools, and fails its
build if the expected versions are unavailable.

The host wrapper `scripts/run_tests_in_docker.sh` mounts the checkout and runs
`tests/run_manifest.py`. `make check` selects its quick profile for local
feedback; `make check-full` runs every required suite and is the CI gate.
It does not expose the host Docker socket by default. The
`docker-package-contract` suite requires `--docker-socket`; the CI-only
`docker-sandbox-smoke` suite also requires `--host-network` to reach its sibling
Compose services. Both options are explicit because the socket grants access
to the Docker daemon and host networking is Linux-runner specific.
