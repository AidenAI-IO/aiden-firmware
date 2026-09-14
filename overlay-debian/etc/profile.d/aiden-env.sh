# Load only the environment validated by aiden-environment.service. Never
# source the user-managed file directly from a privileged login shell.
AIDEN_LOGIN_ENV_FILE=${AIDEN_SYSTEM_ENVIRONMENT:-/run/aiden/system.env}
AIDEN_LOGIN_PROXY_ENV=${AIDEN_WIFI_PROXY_ENVIRONMENT:-/run/wifi_proxy/proxy-env}

case "$-" in
    *a*) AIDEN_LOGIN_RESTORE_ALLEXPORT=1 ;;
    *) AIDEN_LOGIN_RESTORE_ALLEXPORT=0 ;;
esac

set -a
if [ -r "${AIDEN_LOGIN_ENV_FILE}" ]; then
    . "${AIDEN_LOGIN_ENV_FILE}"
fi
if [ "${AIDEN_WIFI_PROXY_ENABLED:-1}" != 0 ] && [ -r "${AIDEN_LOGIN_PROXY_ENV}" ]; then
    . "${AIDEN_LOGIN_PROXY_ENV}"
fi
if [ "${AIDEN_LOGIN_RESTORE_ALLEXPORT}" != 1 ]; then
    set +a
fi

unset AIDEN_LOGIN_ENV_FILE AIDEN_LOGIN_PROXY_ENV AIDEN_LOGIN_RESTORE_ALLEXPORT
