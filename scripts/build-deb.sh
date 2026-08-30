#!/bin/sh
set -eu

architecture=${ARCHITECTURE:-amd64}
goarch=${GOARCH:-amd64}
maintainer=${MAINTAINER:-Sleepy Mario <tech@sleepymario.com>}

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository=$(CDPATH= cd -- "${script_dir}/.." && pwd)
dist_dir=${DIST_DIR:-"${repository}/dist"}

for command_name in awk cmake dpkg dpkg-deb dpkg-shlibdeps du find git go install mktemp sed; do
	if ! command -v "${command_name}" >/dev/null 2>&1; then
		echo "error: required command not found: ${command_name}" >&2
		exit 1
	fi
done

cd "${repository}"
upstream_version=${VERSION:-$(./scripts/version.sh)}
debian_version=$(printf '%s\n' "${upstream_version}" | sed 's/-beta\./~beta./')

if ! dpkg --validate-version "${debian_version}" >/dev/null 2>&1; then
	echo "error: invalid Debian package version: ${debian_version}" >&2
	exit 1
fi

SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-$(git show -s --format=%ct HEAD)}
export SOURCE_DATE_EPOCH
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/streamchat-deb.XXXXXX")
trap 'rm -rf "${work_dir}"' EXIT HUP INT TERM

cli_root="${work_dir}/streamchat-cli"
server_root="${work_dir}/streamchat-server"
gui_root="${work_dir}/streamchat-gui"
build_dir="${work_dir}/desktop-build"

install -d \
	"${cli_root}/DEBIAN" \
	"${cli_root}/usr/bin" \
	"${cli_root}/usr/libexec/streamchat" \
	"${cli_root}/usr/share/doc/streamchat-cli/examples" \
	"${server_root}/DEBIAN" \
	"${server_root}/lib/systemd/system" \
	"${server_root}/usr/share/doc/streamchat-server/examples" \
	"${gui_root}/DEBIAN" \
	"${gui_root}/usr/bin" \
	"${gui_root}/usr/libexec/streamchat" \
	"${gui_root}/usr/share/applications" \
	"${gui_root}/usr/share/icons/hicolor/scalable/apps" \
	"${gui_root}/usr/share/doc/streamchat-gui" \
	"${build_dir}" \
	"${dist_dir}"

CGO_ENABLED=0 GOOS=linux GOARCH="${goarch}" go build \
	-trimpath \
	-buildvcs=false \
	-ldflags="-s -w -X main.version=${upstream_version}" \
	-o "${cli_root}/usr/libexec/streamchat/streamchat-core" \
	./cmd/streamchat
ln -s ../libexec/streamchat/streamchat-core "${cli_root}/usr/bin/streamchat"

CGO_ENABLED=0 GOOS=linux GOARCH="${goarch}" go build \
	-trimpath \
	-buildvcs=false \
	-ldflags="-s -w -X main.version=${upstream_version}" \
	-o "${gui_root}/usr/libexec/streamchat/streamchat-gui-runtime" \
	./cmd/streamchat-gui

cmake -S "${repository}/desktop" -B "${build_dir}" -G Ninja \
	-DCMAKE_BUILD_TYPE=Release \
	-DCMAKE_INSTALL_PREFIX=/usr
cmake --build "${build_dir}" --parallel "${BUILD_JOBS:-2}"
install -m 0755 "${build_dir}/streamchat-gui" \
	"${gui_root}/usr/libexec/streamchat/streamchat-gui"
ln -s ../libexec/streamchat/streamchat-gui "${gui_root}/usr/bin/streamchat-gui"
install -m 0644 desktop/packaging/com.sleepymario.streamchat.desktop \
	"${gui_root}/usr/share/applications/com.sleepymario.streamchat.desktop"
install -m 0644 desktop/assets/com.sleepymario.streamchat.svg \
	"${gui_root}/usr/share/icons/hicolor/scalable/apps/com.sleepymario.streamchat.svg"

for package_root in "${cli_root}" "${server_root}" "${gui_root}"; do
	package_name=$(basename "${package_root}")
	install -m 0644 LICENSE "${package_root}/usr/share/doc/${package_name}/copyright"
done
install -m 0644 README.md "${cli_root}/usr/share/doc/streamchat-cli/README.md"
install -m 0644 examples/config.example.json \
	"${cli_root}/usr/share/doc/streamchat-cli/examples/config.example.json"
install -m 0644 README.md "${server_root}/usr/share/doc/streamchat-server/README.md"
install -m 0644 examples/config.example.json \
	"${server_root}/usr/share/doc/streamchat-server/examples/config.example.json"
install -m 0644 README.md "${gui_root}/usr/share/doc/streamchat-gui/README.md"

install -m 0644 systemd/streamchat-server.service \
	"${server_root}/lib/systemd/system/streamchat-server.service"
install -m 0755 packaging/debian/streamchat-server.postinst "${server_root}/DEBIAN/postinst"
install -m 0755 packaging/debian/streamchat-server.prerm "${server_root}/DEBIAN/prerm"
install -m 0755 packaging/debian/streamchat-server.postrm "${server_root}/DEBIAN/postrm"

install -d "${work_dir}/debian"
cat >"${work_dir}/debian/control" <<EOF
Source: streamchat
Section: net
Priority: optional
Maintainer: ${maintainer}

Package: streamchat-gui
Architecture: any
Description: Native Qt 6 Streamchat desktop application
EOF

gui_shlibs=$(cd "${work_dir}" && dpkg-shlibdeps -O -e"${gui_root}/usr/libexec/streamchat/streamchat-gui" |
	sed -n 's/^shlibs:Depends=//p')
if [ -z "${gui_shlibs}" ]; then
	echo "error: unable to determine Streamchat GUI shared-library dependencies" >&2
	exit 1
fi

cli_size=$(du -sk "${cli_root}/usr" | awk '{print $1}')
server_size=$(du -sk "${server_root}/usr" "${server_root}/lib" | awk '{total += $1} END {print total}')
gui_size=$(du -sk "${gui_root}/usr" | awk '{print $1}')

cat >"${cli_root}/DEBIAN/control" <<EOF
Package: streamchat-cli
Version: ${debian_version}
Section: net
Priority: optional
Architecture: ${architecture}
Maintainer: ${maintainer}
Installed-Size: ${cli_size}
Depends: ca-certificates
Homepage: https://github.com/SleepyMario/streamchat
Description: Streamchat terminal client and shared runtime
 Unified Kick, Twitch, and YouTube live chat through documented platform APIs.
 This package provides the terminal client and shared core used by the optional
 server and native GUI packages.
EOF

cat >"${server_root}/DEBIAN/control" <<EOF
Package: streamchat-server
Version: ${debian_version}
Section: net
Priority: optional
Architecture: ${architecture}
Maintainer: ${maintainer}
Installed-Size: ${server_size}
Depends: streamchat-cli (= ${debian_version}), adduser
Homepage: https://github.com/SleepyMario/streamchat
Description: Streamchat headless ingestion, archive, and relay server
 Installs the Streamchat systemd service, dedicated service account setup,
 and configuration example. Persistent configuration and archives are retained
 when the package is removed.
EOF

cat >"${gui_root}/DEBIAN/control" <<EOF
Package: streamchat-gui
Version: ${debian_version}
Section: net
Priority: optional
Architecture: ${architecture}
Maintainer: ${maintainer}
Installed-Size: ${gui_size}
Depends: streamchat-cli (= ${debian_version}), ${gui_shlibs}, qml6-module-qtqml-workerscript, qml6-module-qtquick, qml6-module-qtquick-controls, qml6-module-qtquick-layouts
Homepage: https://github.com/SleepyMario/streamchat
Description: Native Qt 6 Streamchat desktop application
 Provides the Streamchat graphical client, private local frontend runtime,
 desktop launcher, and icon. It can use its local server or connect to a
 manually configured remote Streamchat server.
EOF

find "${cli_root}" "${server_root}" "${gui_root}" -exec touch -h -d "@${SOURCE_DATE_EPOCH}" {} +

cli_output="${dist_dir}/streamchat-cli_${debian_version}_${architecture}.deb"
server_output="${dist_dir}/streamchat-server_${debian_version}_${architecture}.deb"
gui_output="${dist_dir}/streamchat-gui_${debian_version}_${architecture}.deb"
dpkg-deb --root-owner-group -Zxz --build "${cli_root}" "${cli_output}"
dpkg-deb --root-owner-group -Zxz --build "${server_root}" "${server_output}"
dpkg-deb --root-owner-group -Zxz --build "${gui_root}" "${gui_output}"
printf '%s\n%s\n%s\n' "${cli_output}" "${server_output}" "${gui_output}"
