#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: conformance/live-provider.sh resolve|test

resolve reads the existing STOW_LIVE_* configuration, selects one provider
profile, and writes active/configured/provider/reason values to GITHUB_OUTPUT.
With no configuration it is an explicit skip unless
STOW_CONFORMANCE_REQUIRE_CONFIGURED=1.

test runs only the opt-in live provider test. It never prints credentials or
endpoint values.
EOF
}

fail() {
  printf 'live-provider: %s\n' "$*" >&2
  exit 1
}

parse_bool() {
  local name=$1
  local default=$2
  local value=${!name:-}
  value=${value,,}
  value=${value//$'\n'/}
  value=${value//$'\r'/}
  value=${value#"${value%%[![:space:]]*}"}
  value=${value%"${value##*[![:space:]]}"}
  case "$value" in
    "") printf '%s' "$default" ;;
    1|true|yes|on) printf 'true' ;;
    0|false|no|off) printf 'false' ;;
    *) fail "$name must be a boolean" ;;
  esac
}

emit() {
  local key=$1
  local value=$2
  local line="$key=$value"
  printf '%s\n' "$line"
  if [[ -n ${GITHUB_OUTPUT:-} ]]; then
    printf '%s\n' "$line" >>"$GITHUB_OUTPUT"
  fi
}

# Only active/provider/reason are consumed by the workflow, so a profile is
# reported either as skipped or as the single profile that will run.
skip_profile() {
  emit active false
  emit provider none
  emit reason "$1"
}

run_profile() {
  emit active true
  emit provider "$1"
  emit reason "$2"
}

classify_endpoint() {
  local endpoint=$1
  local authority host
  if [[ $endpoint != https://* ]]; then
    fail "STOW_ENDPOINT must use https for live conformance"
  fi
  authority=${endpoint#https://}
  authority=${authority%%/*}
  authority=${authority%%\?*}
  if [[ -z $authority || $authority == *[[:space:]]* ]]; then
    fail "STOW_ENDPOINT is not a valid HTTPS endpoint"
  fi
  host=${authority,,}
  if [[ $host == *.amazonaws.com || $host == *.amazonaws.com.cn ]]; then
    printf 'aws-s3'
  elif [[ $host == *.r2.cloudflarestorage.com ]]; then
    printf 'cloudflare-r2'
  else
    printf 'custom'
  fi
}

# profile_provider is the endpoint class a profile reports during a dry run,
# before a real endpoint is known.
profile_provider() {
  if [[ $1 == aws-s3 ]]; then
    printf 'aws-s3'
  else
    printf 'cloudflare-r2'
  fi
}

# profile_accepts keeps the profile-to-endpoint mapping in one place: the AWS
# profile only runs against AWS, and the R2/custom profile runs against
# anything that is not AWS.
profile_accepts() {
  if [[ $1 == aws-s3 ]]; then
    [[ $2 == aws-s3 ]]
  else
    [[ $2 != aws-s3 ]]
  fi
}

resolve() {
  local profile=${STOW_LIVE_PROFILE:-}
  local requested=${STOW_CONFORMANCE_REQUESTED_PROVIDER:-auto}
  local dry_run require_configured actual
  local missing=()

  case "$profile" in
    aws-s3|cloudflare-r2-custom) ;;
    *) fail "STOW_LIVE_PROFILE must be aws-s3 or cloudflare-r2-custom" ;;
  esac
  case "$requested" in
    auto|aws-s3|cloudflare-r2-custom) ;;
    *) fail "requested provider must be auto, aws-s3, or cloudflare-r2-custom" ;;
  esac
  dry_run=$(parse_bool STOW_CONFORMANCE_DRY_RUN false)
  require_configured=$(parse_bool STOW_CONFORMANCE_REQUIRE_CONFIGURED false)

  if [[ $requested != auto && $requested != "$profile" ]]; then
    skip_profile profile-not-selected
    return 0
  fi
  if [[ $dry_run == true ]]; then
    run_profile "$(profile_provider "$profile")" dry-run
    return 0
  fi

  for name in STOW_ENDPOINT STOW_ACCESS_KEY_ID STOW_SECRET_ACCESS_KEY STOW_LIVE_BUCKET STOW_LIVE_BUCKET_PREFIX; do
    if [[ -z ${!name:-} ]]; then
      missing+=("$name")
    fi
  done
  if ((${#missing[@]} > 0)); then
    local missing_csv
    missing_csv=$(IFS=,; printf '%s' "${missing[*]}")
    skip_profile missing-configuration
    if [[ $require_configured == true ]]; then
      fail "live provider configuration is required (missing: $missing_csv)"
    fi
    return 0
  fi

  actual=$(classify_endpoint "$STOW_ENDPOINT")
  if ! profile_accepts "$profile" "$actual"; then
    skip_profile endpoint-does-not-match-profile
    if [[ $requested != auto ]]; then
      fail "the configured endpoint is $actual, which does not match the $profile profile"
    fi
    return 0
  fi

  run_profile "$actual" configured
}

run_test() {
  export STOW_CONFORMANCE_UPSTREAM=1
  go test ./conformance -run '^TestUpstreamRunThrough$' -count=1 -v
}

main() {
  local command=${1:-}
  case "$command" in
    resolve) resolve ;;
    test) run_test ;;
    -h|--help|help) usage; exit 0 ;;
    *) usage; exit 64 ;;
  esac
}

main "$@"
