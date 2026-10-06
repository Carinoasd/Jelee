#!/bin/sh
# Sign in with name and password and print the bearer token.
# Usage: JELEE_TOKEN=$(JELEE_URL=... JELEE_USER=... JELEE_PASSWORD=... sh login.sh)
# Accounts with a second factor get a challenge instead of a token; this
# example then fails (see POST /api/v1/auth/login/second-factor).
. "$(dirname "$0")/_common.sh"
: "${JELEE_USER:?set JELEE_USER}"
: "${JELEE_PASSWORD:?set JELEE_PASSWORD}"

# jq builds the body from the environment, so the password never appears in
# an argument list and is escaped correctly.
jq -n '{name: env.JELEE_USER, password: env.JELEE_PASSWORD}' |
	curl --fail-with-body --silent --show-error \
		--request POST \
		--header 'Content-Type: application/json' \
		--data-binary @- \
		"$JELEE_URL/api/v1/auth/login" |
	jq -er '.data.token // error("no token: the account needs a second factor")'
