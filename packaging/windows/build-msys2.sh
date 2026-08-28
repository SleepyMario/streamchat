#!/usr/bin/env bash
set -euo pipefail

export PATH="/ucrt64/bin:/usr/bin:${PATH}"

if [[ -z "${GOROOT:-}" && -d /ucrt64/lib/go ]]; then
    export GOROOT=/ucrt64/lib/go
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
version="${VERSION:-2.0}"
build_dir="${repo_root}/build/windows-ucrt64"
stage_dir="${repo_root}/packaging/windows/stage"
dist_dir="${repo_root}/dist"

case "${build_dir}:${stage_dir}:${dist_dir}" in
    "${repo_root}"/*:"${repo_root}"/*:"${repo_root}"/*) ;;
    *) echo "Refusing to clean paths outside the Streamchat checkout" >&2; exit 2 ;;
esac

cd "${repo_root}"

cmake -E rm -rf "${build_dir}" "${stage_dir}"
cmake -E make_directory "${build_dir}" "${stage_dir}" "${dist_dir}"

go build -trimpath -ldflags "-s -w -X main.version=${version}" \
    -o "${stage_dir}/streamchat-core.exe" "${repo_root}/cmd/streamchat"
go build -trimpath -ldflags "-s -w -X main.version=${version}" \
    -o "${stage_dir}/streamchat-gui-runtime.exe" "${repo_root}/cmd/streamchat-gui"

cmake -S "${repo_root}/desktop" -B "${build_dir}" -G Ninja \
    -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_PREFIX_PATH=/ucrt64
cmake --build "${build_dir}" --parallel 2
cmake -E copy "${build_dir}/streamchat-gui.exe" "${stage_dir}/streamchat-gui.exe"

windeployqt6 --release --no-translations \
    --qmldir "${repo_root}/desktop/qml" \
    "${stage_dir}/streamchat-gui.exe"

# windeployqt deploys Qt modules and plugins. Add their recursively discovered
# MSYS2/MinGW dependencies and verify that the app-local bundle is closed.
"${repo_root}/packaging/windows/collect-msys2-runtime.sh" "${stage_dir}"

makensis \
    -DVERSION="${version}" \
    -DSTAGE="$(cygpath -w "${stage_dir}")" \
    -DOUTPUT="$(cygpath -w "${dist_dir}/Streamchat-${version}-windows-x86_64.exe")" \
    "$(cygpath -w "${repo_root}/packaging/windows/Streamchat.nsi")"

printf '%s\n' "${dist_dir}/Streamchat-${version}-windows-x86_64.exe"
