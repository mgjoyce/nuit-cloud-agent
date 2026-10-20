#!/bin/sh
# Prepare a machine as a nuit-cloud server from a PROFILE (what kind of machine it is) made of FEATURES (small
# check-and-fix steps in features.sh). Run as root on the machine itself:
#
#   su - -c "sh /path/to/bootstrap.sh <profile> [--with <feature>]... [--check]"
#
#   --check   change nothing; report which features have drifted (exit 1 if any)
#   --with    add a feature the profile does not include (e.g. nginx)
#   --list    list profiles and features
#
# Safe to run again: a feature that is already satisfied is left alone, and a file someone else wrote is never overwritten.
# ROOT=<dir> prefixes every system path (used by test-bootstrap.sh); leave it unset on a real machine.
set -eu
PATH="$PATH:/usr/local/sbin:/usr/sbin:/sbin"
here="$(cd "$(dirname "$0")" && pwd)"
ROOT="${ROOT:-}"
NC_USER="${NC_USER:-nuitcloud}"
MODE=apply
EXTRA=""
PROFILE=""
REBOOT=0

while [ $# -gt 0 ]; do
  case "$1" in
    --check) MODE=check ;;
    --with) shift; EXTRA="$EXTRA ${1:?--with needs a feature name}" ;;
    --list)
      echo "profiles:"; for f in "$here"/profiles/*.sh; do b="$(basename "$f" .sh)"; echo "  $b"; done
      echo "features:"; grep -o '^f_[a-z0-9_]*_ok' "$here/features.sh" | sed 's/^f_//;s/_ok$//;s/^/  /'
      exit 0 ;;
    -*) echo "unknown option $1" >&2; exit 2 ;;
    *) PROFILE="$1" ;;
  esac
  shift
done
[ -n "$PROFILE" ] && [ -f "$here/profiles/$PROFILE.sh" ] || { echo "usage: sh $0 <profile> [--with <feature>] [--check] | --list" >&2; exit 2; }
[ "$MODE" = check ] || [ -n "$ROOT" ] || [ "$(id -u)" = 0 ] || { echo "Run this as root, for example: su - -c 'sh $0 $PROFILE'" >&2; exit 1; }

p() { printf '%s%s\n' "$ROOT" "$1"; }                     # a system path, under ROOT in tests
say() { printf '%s\n' "$*"; }
write_if_missing() { # write_if_missing <path> — content on stdin; never overwrites
  f="$(p "$1")"; if [ -e "$f" ]; then cat >/dev/null; return 0; fi
  mkdir -p "$(dirname "$f")"; cat >"$f"
}
# rewrite <file> <command…>: replace a file with a command's output, via a temp file (no sed -i: differs between BSD and GNU)
rewrite() { f="$1"; shift; t="$f.nuit-new"; "$@" >"$t" && mv "$t" "$f"; }

. "$here/features.sh"
. "$here/profiles/$PROFILE.sh"   # sets PROFILE_FEATURES (and may override defaults such as LOG_MAX_SIZE)

drift=0; manual=0; failed=0
for feat in $PROFILE_FEATURES $EXTRA; do
  command -v "f_${feat}_ok" >/dev/null 2>&1 || { echo "unknown feature: $feat" >&2; exit 2; }
  if "f_${feat}_ok"; then say "ok      $feat"; continue; fi
  if [ "$MODE" = check ]; then say "DRIFT   $feat"; drift=1; continue; fi
  say "applying ${feat} ..."
  rc=0; "f_${feat}_do" || rc=$?
  if [ "$rc" = 3 ]; then say "MANUAL  $feat"; manual=1
  elif [ "$rc" = 0 ] && "f_${feat}_ok"; then say "done    $feat"
  else say "FAILED  $feat"; failed=1; fi
done

echo
if [ "$REBOOT" = 1 ]; then echo "A reboot is needed for the changes above to take effect."; fi
if [ "$MODE" = apply ]; then summary; fi
[ "$failed" = 0 ] && [ "$drift" = 0 ] && [ "$manual" = 0 ]
