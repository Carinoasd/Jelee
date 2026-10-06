#!/bin/sh
# Public service information: name, version and capabilities.
# Usage: JELEE_URL=... sh system.sh
. "$(dirname "$0")/_common.sh"

curl --fail-with-body --silent --show-error \
	--request GET \
	"$JELEE_URL/api/v1/system"
