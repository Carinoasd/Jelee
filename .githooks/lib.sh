# Shared helpers of the Jelee Git hooks (G01.6); sourced, not executed.
# The hooks use the project toolchain only: .bin/go after `make init`, or
# scripts/run-go.ps1 through pwsh after `scripts/make.ps1 init` on Windows.

root=$(git rev-parse --show-toplevel)
cd "$root" || exit 1

if [ -x .bin/go ]; then
	run_go() { .bin/go "$@"; }
elif [ -f scripts/run-go.ps1 ] && command -v pwsh >/dev/null 2>&1; then
	run_go() { pwsh -NoProfile -File scripts/run-go.ps1 "$@"; }
else
	echo "jelee hooks: the project Go toolchain is missing; run make init (Windows: pwsh -File scripts/make.ps1 init)" >&2
	exit 1
fi

# step NAME COMMAND...: run one check and remember a failure.
failed=0
step() {
	name=$1
	shift
	if ! "$@"; then
		echo "jelee hooks: $name failed" >&2
		failed=1
	fi
}
