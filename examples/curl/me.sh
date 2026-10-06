#!/bin/sh
# Read the signed-in account.
# Usage: JELEE_URL=... JELEE_TOKEN=... sh me.sh
. "$(dirname "$0")/_common.sh"

jelee_auth | curl --fail-with-body --silent --show-error \
	--request GET \
	--header @- \
	"$JELEE_URL/api/v1/users/me"
