#!/bin/sh
# Go gate for smart-srun 2.0: format, vet, unit tests, coverage, race.
#
# This is the entry point spec 05 fixes by name. It never reports success for
# work it did not run: a missing toolchain or a missing core/ tree is a failure,
# not a skip. Race detection needs cgo, so it is reported as an explicit skip
# with a reason rather than silently dropped.
#
# Usage: scripts/verify-go.sh [--no-race] [packages...]
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
core_dir="$repo_root/core"
want_race=1

while [ $# -gt 0 ]; do
    case "$1" in
        --no-race) want_race=0; shift ;;
        --) shift; break ;;
        *) break ;;
    esac
done
packages=${*:-./...}

fail() { printf 'verify-go: FAIL: %s\n' "$1" >&2; exit 1; }
step() { printf '\n=== %s ===\n' "$1"; }

command -v go >/dev/null 2>&1 || fail "go toolchain not on PATH"
[ -d "$core_dir" ] || fail "core/ does not exist yet; no Go gate can pass (M01 creates it)"
[ -f "$core_dir/go.mod" ] || fail "core/go.mod missing"

cd "$core_dir"
printf 'go: %s\n' "$(go version)"
printf 'core: %s\n' "$core_dir"

step "gofmt"
unformatted=$(gofmt -l . || true)
[ -z "$unformatted" ] || fail "gofmt reports unformatted files:
$unformatted"
echo "ok"

step "go vet"
go vet ./...

step "go test (shuffled, coverage)"
test_log="$core_dir/.verify-go-test.log"
if ! CGO_ENABLED=0 go test -count=1 -shuffle=on -coverprofile=coverage.out $packages \
        >"$test_log" 2>&1; then
    cat "$test_log"
    fail "unit tests failed"
fi
cat "$test_log"

step "coverage summary"
go tool cover -func=coverage.out | tail -n 1

# Spec 05 line 99 sets floors, and printing a number is not checking it. The
# independent review of d33528a found this entry point printed the total and
# enforced nothing, so a package could slide under its floor while the gate
# still said everything passed.
step "coverage thresholds (spec 05)"
module=$(go list -m)

# coverage_of prints the measured percentage for one package, or nothing.
coverage_of() {
    awk -v want="$module/$1" '
        $1 == "ok" && $2 == want {
            for (i = 1; i <= NF; i++)
                if ($i == "coverage:") { sub(/%/, "", $(i+1)); print $(i+1) }
        }' "$test_log"
}

require_coverage() {
    pkg_path=$1
    floor=$2
    [ -d "$core_dir/$pkg_path" ] || fail "required package $pkg_path is missing"
    value=$(coverage_of "$pkg_path")
    [ -n "$value" ] || fail "no coverage was reported for $pkg_path; a floor cannot be met by a package the run did not measure"
    if ! awk -v v="$value" -v f="$floor" 'BEGIN { exit (v + 0 >= f + 0) ? 0 : 1 }'; then
        fail "$pkg_path coverage is ${value}%, below the spec 05 floor of ${floor}%"
    fi
    printf '  %-30s %s%% (floor %s%%)\n' "$pkg_path" "$value" "$floor"
}

require_coverage internal/protocol/srun 90
require_coverage internal/policy 90
require_coverage internal/config 90
# Portal discovery/parsing was implemented under portal, not the provisional
# discovery directory in the architecture plan. A stale path must not skip it.
require_coverage internal/portal 90

total=$(go tool cover -func=coverage.out |
    awk '$1 == "total:" { sub(/%/, "", $NF); print $NF }')
[ -n "$total" ] || fail "no total coverage was produced"
if ! awk -v v="$total" 'BEGIN { exit (v + 0 >= 80) ? 0 : 1 }'; then
    fail "total coverage is ${total}%, below the spec 05 floor of 80%"
fi
printf '  %-30s %s%% (floor 80%%)\n' "whole core" "$total"

skipped=""
if [ "$want_race" -eq 0 ]; then
    step "race"
    echo "SKIPPED: --no-race requested"
    skipped="race"
elif ! command -v cc >/dev/null 2>&1 && ! command -v gcc >/dev/null 2>&1; then
    step "race"
    # Spec 05: an unrun gate must say so. Do not let this masquerade as a pass.
    fail "race gate needs a C compiler for CGO_ENABLED=1; none found"
else
    step "go test -race"
    CGO_ENABLED=1 go test -race -count=1 $packages
fi

# A run that skipped a required gate does not get to print the acceptance line.
# The review found --no-race printing SKIPPED and then "all gates passed", which
# is the one thing this script's own header promises never to do.
if [ -n "$skipped" ]; then
    printf '\nverify-go: development check only -- %s did not run.\n' "$skipped"
    printf 'This is NOT an acceptance result; run without --no-race for that.\n'
    exit 0
fi

printf '\nverify-go: all gates passed\n'
