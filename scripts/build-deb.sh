#!/bin/sh
set -eu

architecture=${ARCHITECTURE:-amd64}
goarch=${GOARCH:-amd64}
maintainer=${MAINTAINER:-Sleepy Mario <tech@sleepymario.com>}

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository=$(CDPATH= cd -- "${script_dir}/.." && pwd)
dist_dir=${DIST_DIR:-"${repository}/dist"}

for command_name in awk dpkg dpkg-deb du find git go install mktemp sed; do
	if ! command -v "${command_name}" >/dev/null 2>&1; then
		echo "error: required command not found: ${command_name}" >&2
		exit 1
	fi
done

cd "${repository}"
upstream_version=$(./scripts/version.sh)
debian_version=$(printf '%s\n' "${upstream_version}" | sed 's/-beta\./~beta./')

if ! dpkg --validate-version "${debian_version}" >/dev/null 2>&1; then
	echo "error: invalid Debian package version: ${debian_version}" >&2
	exit 1
fi

SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-$(git show -s --format=%ct HEAD)}
export SOURCE_DATE_EPOCH
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/streamchat-deb.XXXXXX")
trap 'rm -rf "${work_dir}"' EXIT HUP INT TERM

client_root="${work_dir}/streamchat"
server_root="${work_dir}/streamchat-server"
install -d \
	"${client_root}/DEBIAN" \
	"${client_root}/usr/bin" \
	"${client_root}/usr/share/doc/streamchat/examples" \
	"${server_root}/DEBIAN" \
	"${server_root}/lib/systemd/system" \
	"${server_root}/usr/share/doc/streamchat-server/examples" \
	"${dist_dir}"

CGO_ENABLED=0 GOOS=linux GOARCH="${goarch}" go build \
	-trimpath \
	-buildvcs=false \
	-ldflags="-s -w -X main.version=${upstream_version}" \
	-o "${client_root}/usr/bin/streamchat" \
	./cmd/streamchat

install -m 0644 README.md "${client_root}/usr/share/doc/streamchat/README.md"
install -m 0644 LICENSE "${client_root}/usr/share/doc/streamchat/LICENSE"
install -m 0644 LICENSE "${client_root}/usr/share/doc/streamchat/copyright"
install -m 0644 examples/config.example.json \
	"${client_root}/usr/share/doc/streamchat/examples/config.example.json"

install -m 0644 README.md "${server_root}/usr/share/doc/streamchat-server/README.md"
install -m 0644 LICENSE "${server_root}/usr/share/doc/streamchat-server/copyright"
install -m 0644 examples/config.example.json \
	"${server_root}/usr/share/doc/streamchat-server/examples/config.example.json"
install -m 0644 systemd/streamchat-server.service \
	"${server_root}/lib/systemd/system/streamchat-server.service"
install -m 0755 packaging/debian/streamchat-server.postinst "${server_root}/DEBIAN/postinst"
install -m 0755 packaging/debian/streamchat-server.prerm "${server_root}/DEBIAN/prerm"
install -m 0755 packaging/debian/streamchat-server.postrm "${server_root}/DEBIAN/postrm"

client_size=$(du -sk "${client_root}/usr" | awk '{print $1}')
server_size=$(du -sk "${server_root}/usr" "${server_root}/lib" | awk '{total += $1} END {print total}')

cat >"${client_root}/DEBIAN/control" <<EOF
Package: streamchat
Version: ${debian_version}
Section: net
Priority: optional
Architecture: ${architecture}
Maintainer: ${maintainer}
Installed-Size: ${client_size}
Depends: ca-certificates
Homepage: https://github.com/SleepyMario/streamchat
Description: Kick-focused interactive live-chat client (public beta)
 Streamchat is a terminal client for reading and writing Kick chat through
 official APIs. YouTube and Twitch support is preliminary in this beta.
EOF

cat >"${server_root}/DEBIAN/control" <<EOF
Package: streamchat-server
Version: ${debian_version}
Section: net
Priority: optional
Architecture: ${architecture}
Maintainer: ${maintainer}
Installed-Size: ${server_size}
Depends: streamchat (= ${debian_version}), adduser
Homepage: https://github.com/SleepyMario/streamchat
Description: Streamchat headless ingestion, archive, and relay service
 Installs the Streamchat server systemd unit and configuration example. The
 shared executable is provided by the exact-version streamchat dependency.
EOF

find "${client_root}" "${server_root}" -exec touch -h -d "@${SOURCE_DATE_EPOCH}" {} +

client_output="${dist_dir}/streamchat_${debian_version}_${architecture}.deb"
server_output="${dist_dir}/streamchat-server_${debian_version}_${architecture}.deb"
dpkg-deb --root-owner-group -Zxz --build "${client_root}" "${client_output}"
dpkg-deb --root-owner-group -Zxz --build "${server_root}" "${server_output}"
printf '%s\n%s\n' "${client_output}" "${server_output}"
