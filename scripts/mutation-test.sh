#!/usr/bin/env bash
# Mutation testing for dacli — the only honest audit of a test suite.
#
# Coverage says a line RAN. It does not say anything asserted its behavior.
# This flips one operator at a time in real source, re-runs the package's tests,
# and asks the only question that matters: did the suite NOTICE? A mutant that
# survives is a line your tests execute but do not actually check.
#
# Dependency-free by design: dacli's go.mod has zero requires and this must not
# change that, so there is no mutation framework here — just sed-style operator
# flips over a scratch copy, plus the repo's own `go test`. It is viable because
# focused checks avoid repeatedly running the full repository suite.
#
# Usage:
#   scripts/mutation-test.sh                       # audit the default package set
#   scripts/mutation-test.sh ./internal/gates      # audit one package
#   MUTANTS=40 scripts/mutation-test.sh            # cap the sample per package
#
# Exit status is 0 unless a package's kill rate falls below THRESHOLD, so this
# can gate CI once the suite is strong enough to hold a floor.
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1

THRESHOLD="${THRESHOLD:-60}"   # minimum acceptable kill rate, percent
MUTANTS="${MUTANTS:-25}"       # max mutants sampled per package

PACKAGES=("$@")
if [ ${#PACKAGES[@]} -eq 0 ]; then
  # The packages where a surviving mutant would matter most: the capability
  # model, the gates that certify work, the store that records it, and the
  # native authority that binds current credentials, leases and evidence.
  PACKAGES=(
    ./internal/agentid
    ./internal/gates
    ./internal/mdstore
    ./internal/model
    ./internal/features/acceptance
    ./internal/governance/sessions
  )
fi

# Operator flips. Each is a behavior change a competent test should catch.
# Deliberately excludes changes that are usually no-ops (e.g. `+` to `-` on
# string concat) to keep the signal high.
declare -a MUTATIONS=(
  's/ >= / > /'
  's/ <= / < /'
  's/ == / != /'
  's/ != / == /'
  's/ && / || /'
  's/ || / \&\& /'
)

RESTORE_FILE=""
RESTORE_BACKUP=""
restore() {
  if [ -n "$RESTORE_FILE" ] && [ -f "$RESTORE_BACKUP" ]; then
    mv -f "$RESTORE_BACKUP" "$RESTORE_FILE"
    RESTORE_FILE=""; RESTORE_BACKUP=""
  fi
}
# A mutation harness that leaves a repo mutated is a catastrophe, so restore on
# every exit path including SIGINT.
trap 'restore; exit 130' INT TERM
trap 'restore' EXIT

total_killed=0
total_run=0
failed_pkgs=()

for pkg in "${PACKAGES[@]}"; do
  files=$(find "${pkg#./}" -maxdepth 1 -name '*.go' ! -name '*_test.go' 2>/dev/null)
  [ -z "$files" ] && continue

  # Baseline: a suite that is already failing tells us nothing.
  if ! go test "$pkg" >/dev/null 2>&1; then
    echo "SKIP  $pkg — tests already failing before mutation"
    continue
  fi

  killed=0; run=0
  for f in $files; do
    for m in "${MUTATIONS[@]}"; do
      [ "$run" -ge "$MUTANTS" ] && break 2
      # How many sites does this mutation have in this file?
      sites=$(sed -n "${m}p" "$f" 2>/dev/null | wc -l | tr -d ' ')
      [ "${sites:-0}" -eq 0 ] && continue

      backup="${f}.mutbak"
      cp "$f" "$backup"
      RESTORE_FILE="$f"; RESTORE_BACKUP="$backup"

      # sed replaces the first occurrence on each matching line, so one operator
      # class is flipped file-wide per mutant. That is coarser than one-site-at-
      # a-time mutation: a SURVIVOR is therefore a strong signal (no site of
      # that operator in the file is asserted), while a kill only proves at
      # least one site is covered. Read survivors, not the kill count.
      sed "1,\$ ${m}" "$f" > "${f}.tmp" 2>/dev/null && mv "${f}.tmp" "$f"

      if ! go build "$pkg" >/dev/null 2>&1; then
        restore; continue          # uncompilable mutant is not a real mutant
      fi
      run=$((run + 1))
      if go test "$pkg" >/dev/null 2>&1; then
        echo "  SURVIVED  $f  [${m}]"
      else
        killed=$((killed + 1))
      fi
      restore
    done
  done

  if [ "$run" -eq 0 ]; then
    echo "----  $pkg — no viable mutants"
    continue
  fi
  rate=$(( killed * 100 / run ))
  total_killed=$((total_killed + killed)); total_run=$((total_run + run))
  status="ok"
  if [ "$rate" -lt "$THRESHOLD" ]; then status="BELOW THRESHOLD"; failed_pkgs+=("$pkg"); fi
  printf '%-42s %3d/%-3d killed  %3d%%  %s\n' "$pkg" "$killed" "$run" "$rate" "$status"
done

echo
if [ "$total_run" -gt 0 ]; then
  printf 'TOTAL  %d/%d mutants killed (%d%%), threshold %d%%\n' \
    "$total_killed" "$total_run" "$(( total_killed * 100 / total_run ))" "$THRESHOLD"
fi
if [ ${#failed_pkgs[@]} -gt 0 ]; then
  echo "below threshold: ${failed_pkgs[*]}"
  exit 1
fi
