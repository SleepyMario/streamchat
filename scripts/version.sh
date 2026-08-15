#!/bin/sh
set -eu

if [ -n "${VERSION:-}" ]; then
	printf '%s\n' "${VERSION#v}"
	exit 0
fi

if tag=$(git describe --tags --exact-match HEAD 2>/dev/null); then
	printf '%s\n' "${tag#v}"
	exit 0
fi

commit_date=$(git show -s --format=%cd --date=format:%Y%m%d HEAD 2>/dev/null || printf unknown)
commit_hash=$(git rev-parse --short=12 HEAD 2>/dev/null || printf unknown)
printf '0.0.0-development+git%s.%s\n' "${commit_date}" "${commit_hash}"
