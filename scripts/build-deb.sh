#!/bin/sh
set -eu

package=streamchat
architecture=amd64
maintainer=${MAINTAINER:-Sleepy Mario <tech@sleepymario.com>}

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository=$(CDPATH= cd -- "${script_dir}/.." && pwd)
dist_dir=${DIST_DIR:-"${repository}/dist"}

for command_name in go git dpkg dpkg-deb install mktemp; do
	if ! command -v "${command_name}" >/dev/null 2>&1; then
		echo "error: required command not found: ${command_name}" >&2
		exit 1
	fi
done

cd "${repository}"

if [ -n "${VERSION:-}" ]; then
	version=${VERSION}
elif tag=$(git describe --tags --exact-match HEAD 2>/dev/null); then
	version=${tag#v}
else
	commit_date=$(git show -s --format=%cd --date=format:%Y%m%d HEAD)
	commit_hash=$(git rev-parse --short=12 HEAD)
	version="0.0.0+git${commit_date}.${commit_hash}"
fi

if ! dpkg --validate-version "${version}" >/dev/null 2>&1; then
	echo "error: invalid Debian package version: ${version}" >&2
	exit 1
fi

SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-$(git show -s --format=%ct HEAD)}
export SOURCE_DATE_EPOCH
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/streamchat-deb.XXXXXX")
trap 'rm -rf "${work_dir}"' EXIT HUP INT TERM

package_root="${work_dir}/${package}"
install -d \
	"${package_root}/DEBIAN" \
	"${package_root}/usr/bin" \
	"${package_root}/usr/share/doc/${package}/examples" \
	"${dist_dir}"

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
	-trimpath \
	-buildvcs=false \
	-ldflags='-s -w' \
	-o "${package_root}/usr/bin/streamchat" \
	./cmd/streamchat

install -m 0644 README.md "${package_root}/usr/share/doc/${package}/README.md"
install -m 0644 LICENSE "${package_root}/usr/share/doc/${package}/LICENSE"
install -m 0644 LICENSE "${package_root}/usr/share/doc/${package}/copyright"
install -m 0644 examples/config.example.json \
	"${package_root}/usr/share/doc/${package}/examples/config.example.json"

installed_size=$(du -sk "${package_root}/usr" | awk '{print $1}')
cat >"${package_root}/DEBIAN/control" <<EOF
Package: ${package}
Version: ${version}
Section: net
Priority: optional
Architecture: ${architecture}
Maintainer: ${maintainer}
Installed-Size: ${installed_size}
Depends: ca-certificates
Homepage: https://github.com/SleepyMario/streamchat
Description: Merged live chat reader for YouTube, Kick, and Twitch
 Streamchat is a command-line application for reading and merging live chat
 from the official YouTube, Kick, and Twitch platform APIs.
EOF

find "${package_root}" -exec touch -h -d "@${SOURCE_DATE_EPOCH}" {} +

output="${dist_dir}/${package}_${version}_${architecture}.deb"
dpkg-deb --root-owner-group -Zxz --build "${package_root}" "${output}"
echo "${output}"
