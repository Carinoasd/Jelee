#!/bin/sh
# List the libraries the signed-in account may see (its own grants).
# Usage: JELEE_URL=... JELEE_TOKEN=... sh library-grants.sh
. "$(dirname "$0")/_common.sh"

JELEE_USER_ID=$(jelee_auth | curl --fail-with-body --silent --show-error \
	--request GET \
	--header @- \
	"$JELEE_URL/api/v1/users/me" | jq -er '.data.id')

jelee_auth | curl --fail-with-body --silent --show-error \
	--request GET \
	--header @- \
	"$JELEE_URL/api/v1/users/$JELEE_USER_ID/libraries"
