#!/usr/bin/env bash
set -euo pipefail

export PATH="/ucrt64/bin:/usr/bin:${PATH}"

stage_dir="${1:?usage: collect-msys2-runtime.sh STAGE_DIR}"
if [[ ! -d "${stage_dir}" ]]; then
    echo "Windows stage directory does not exist: ${stage_dir}" >&2
    exit 2
fi

forwarders=(/c/Windows/System32/downlevel/api-ms-win-*.dll)
if [[ ! -f "${forwarders[0]}" ]]; then
    echo "Windows API-set forwarding DLLs were not found" >&2
    exit 1
fi
for dependency in "${forwarders[@]}"; do
    cmake -E copy_if_different "${dependency}" "${stage_dir}/"
done

mapfile -d '' dependency_queue < <(
    find "${stage_dir}" -type f \( -iname '*.exe' -o -iname '*.dll' \) \
        ! -iname 'api-ms-win-*.dll' -print0
)
declare -A inspected=()
for ((index = 0; index < ${#dependency_queue[@]}; ++index)); do
    binary="${dependency_queue[index]}"
    [[ -n "${inspected[${binary}]:-}" ]] && continue
    inspected["${binary}"]=1
    while IFS= read -r dependency; do
        destination="${stage_dir}/$(basename "${dependency}")"
        if [[ ! -f "${destination}" ]]; then
            cmake -E copy "${dependency}" "${destination}"
            dependency_queue+=("${destination}")
        fi
    done < <(
        PATH="${stage_dir}:/ucrt64/bin:/usr/bin:/c/Windows/System32" \
            ldd "${binary}" 2>/dev/null |
            sed -n 's|.*=> \(/ucrt64/bin/[^ ]*\.dll\).*|\1|p'
    )
done

missing=0
while IFS= read -r -d '' binary; do
    unresolved="$({
        PATH="${stage_dir}:/usr/bin:/c/Windows/System32" ldd "${binary}" 2>/dev/null || true
    } | sed -n '/=> not found/p')"
    if [[ -n "${unresolved}" ]]; then
        printf '%s\n%s\n' "Unresolved dependencies for ${binary}:" "${unresolved}" >&2
        missing=1
    fi
done < <(
    find "${stage_dir}" -type f \( -iname '*.exe' -o -iname '*.dll' \) \
        ! -iname 'api-ms-win-*.dll' -print0
)

if [[ ${missing} -ne 0 ]]; then
    exit 1
fi
