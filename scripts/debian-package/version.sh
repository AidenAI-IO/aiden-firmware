#!/usr/bin/env bash
# Shared defaults for factory images and standalone business packages.
export AIDEN_BUSINESS_VERSION=${AIDEN_BUSINESS_VERSION:-0.0.1}
export AIDEN_BUSINESS_REVISION=${AIDEN_BUSINESS_REVISION:-2}

if [[ ! "${AIDEN_BUSINESS_VERSION}" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] ||
    [[ ! "${AIDEN_BUSINESS_REVISION}" =~ ^[1-9][0-9]*$ ]]; then
    echo 'Business version must be MAJOR.MINOR.PATCH and revision a positive integer' >&2
    return 1
fi
