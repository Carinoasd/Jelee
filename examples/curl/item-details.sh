#!/bin/sh
# Read the display metadata of one item.
# Usage: JELEE_URL=... JELEE_TOKEN=... JELEE_ITEM_ID=... sh item-details.sh
. "$(dirname "$0")/_common.sh"
: "${JELEE_ITEM_ID:?set JELEE_ITEM_ID to an item ID from items.sh}"

jelee_auth | curl --fail-with-body --silent --show-error \
	--request GET \
	--header @- \
	"$JELEE_URL/api/v1/items/$JELEE_ITEM_ID/details"
