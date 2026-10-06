#!/bin/sh
# List every library (administrators only; others get 403 forbidden and use
# library-grants.sh).
# Usage: JELEE_URL=... JELEE_TOKEN=... sh libraries.sh
. "$(dirname "$0")/_common.sh"

jelee_auth | curl --fail-with-body --silent --show-error \
	--request GET \
	--header @- \
	"$JELEE_URL/api/v1/libraries?limit=100"
