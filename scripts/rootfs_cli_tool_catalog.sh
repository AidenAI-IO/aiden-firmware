#!/usr/bin/env bash

rootfs_cli_catalog_records() {
    local catalog_path="$1"

    if [ ! -f "$catalog_path" ]; then
        echo "rootfs CLI tool catalog not found: $catalog_path" >&2
        return 1
    fi

    awk -F '|' '
        function fail(message) {
            print "rootfs CLI tool catalog line " NR ": " message > "/dev/stderr"
            failed = 1
            exit 1
        }

        /^[[:space:]]*($|#)/ { next }

        {
            if (NF != 8) {
                fail("expected 8 fields, got " NF)
            }

            name = $1
            version = $2
            kind = $3
            source = $4
            target = $5
            source_sha256 = $6
            artifact_path = $7
            strip_policy = $8

            if (name !~ /^[A-Za-z0-9][A-Za-z0-9._+-]*$/) {
                fail("invalid tool name: " name)
            }
            if (seen[name]++) {
                fail("duplicate tool name: " name)
            }
            if (version == "") {
                fail("missing version for " name)
            }
            if (kind != "go" && kind != "tar_gz") {
                fail("invalid build kind for " name ": " kind)
            }
            if (source == "") {
                fail("missing source for " name)
            }
            if (target == "") {
                fail("missing target for " name)
            }
            if (strip_policy != "preserve" && strip_policy != "normal") {
                fail("invalid strip policy for " name ": " strip_policy)
            }

            if (kind == "go") {
                if (target != "linux/arm/v7") {
                    fail("unsupported Go target for " name ": " target)
                }
                if (source_sha256 != "-" || artifact_path != "-") {
                    fail("go tool " name " must use - for source_sha256 and artifact_path")
                }
            } else {
                if (target != "armv7-unknown-linux-musleabihf") {
                    fail("unsupported tar_gz target for " name ": " target)
                }
                if (source !~ /^https?:\/\//) {
                    fail("tar_gz source for " name " must be http(s)")
                }
                if (length(source_sha256) != 64 || source_sha256 !~ /^[0-9A-Fa-f]+$/) {
                    fail("tar_gz source_sha256 for " name " must be 64 hexadecimal characters")
                }
                if (artifact_path == "-" || artifact_path ~ /^\// || artifact_path ~ /(^|\/)\.\.(\/|$)/) {
                    fail("invalid tar_gz artifact_path for " name ": " artifact_path)
                }
            }

            print name "|" version "|" kind "|" source "|" target "|" tolower(source_sha256) "|" artifact_path "|" strip_policy
            count++
        }

        END {
            if (!failed && count == 0) {
                print "rootfs CLI tool catalog has no tools" > "/dev/stderr"
                exit 1
            }
        }
    ' "$catalog_path"
}

rootfs_cli_catalog_name_policy_records() {
    local catalog_path="$1"
    local records

    records="$(rootfs_cli_catalog_records "$catalog_path")" || return 1
    printf '%s\n' "$records" | awk -F '|' '{ print $1 "|" $8 }'
}

rootfs_cli_catalog_names() {
    local catalog_path="$1"
    local strip_policy="${2:-all}"
    local records

    case "$strip_policy" in
        all|preserve|normal) ;;
        *)
            echo "invalid rootfs CLI tool catalog policy filter: $strip_policy" >&2
            return 1
            ;;
    esac

    records="$(rootfs_cli_catalog_name_policy_records "$catalog_path")" || return 1
    if [ "$strip_policy" = "all" ]; then
        printf '%s\n' "$records" | awk -F '|' '{ print $1 }'
    else
        printf '%s\n' "$records" | awk -F '|' -v strip_policy="$strip_policy" '$2 == strip_policy { print $1 }'
    fi
}
