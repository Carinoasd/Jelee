#!/bin/sh
# Revoke the session of JELEE_TOKEN. Bearer sessions need no CSRF header.
# Usage: JELEE_URL=... JELEE_TOKEN=... sh logout.sh
. "$(dirname "$0")/_common.sh"

jelee_auth | curl --fail-with-body --silent --show-error \
	--request POST \
	--header @- \
	--header 'Content-Type: application/json' \
	--data '{}' \
	"$JELEE_URL/api/v1/auth/logout"
