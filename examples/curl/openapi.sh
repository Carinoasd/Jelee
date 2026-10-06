#!/bin/sh
# Download the OpenAPI 3.1 document of the running server to openapi.json.
# The browsable rendering of the same document is $JELEE_URL/api-docs.
# Usage: JELEE_URL=... sh openapi.sh
. "$(dirname "$0")/_common.sh"

curl --fail-with-body --silent --show-error \
	--request GET \
	--output openapi.json \
	"$JELEE_URL/api/v1/openapi.json"
