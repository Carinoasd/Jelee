# Shared helpers of the Jelee curl examples; sourced, not run.
#
# Every example reads its configuration from the environment:
#   JELEE_URL       server root, for example http://127.0.0.1:8097 (required)
#   JELEE_TOKEN     bearer token printed by login.sh (authenticated examples)
# login.sh additionally reads JELEE_USER and JELEE_PASSWORD. No credential is
# ever written into a script or passed as a command-line argument: the
# password reaches curl through stdin and the token through a header read
# from stdin, so neither shows up in the process list.
set -eu

: "${JELEE_URL:?set JELEE_URL to the server root, for example http://127.0.0.1:8097}"
JELEE_URL=${JELEE_URL%/}

# jelee_auth prints the Authorization header for `curl --header @-`.
jelee_auth() {
	: "${JELEE_TOKEN:?set JELEE_TOKEN, for example JELEE_TOKEN=\$(sh login.sh)}"
	printf 'Authorization: Bearer %s\n' "$JELEE_TOKEN"
}
