#!/bin/sh
# List the first items of one library, sorted by name (offset form).
# Usage: JELEE_URL=... JELEE_TOKEN=... JELEE_LIBRARY_ID=... sh items.sh
. "$(dirname "$0")/_common.sh"
: "${JELEE_LIBRARY_ID:?set JELEE_LIBRARY_ID to a library ID from library-grants.sh}"

jelee_auth | curl --fail-with-body --silent --show-error \
	--request GET \
	--header @- \
	"$JELEE_URL/api/v1/items?libraryId=$JELEE_LIBRARY_ID&limit=20&sort=name"
