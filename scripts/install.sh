#!/bin/sh
set -eu

repository="rrrrrredy/agent-memory-system"
version="${AGENTMEM_VERSION:-latest}"
install_dir="${AGENTMEM_INSTALL_DIR:-${HOME}/.local/bin}"

if [ "$(uname -s)" != "Darwin" ]; then
  echo "This installer supports macOS. Use a release archive directly on other systems." >&2
  exit 1
fi

case "$(uname -m)" in
  arm64) release_architecture="arm64" ;;
  x86_64) release_architecture="amd64" ;;
  *) echo "Unsupported macOS architecture: $(uname -m)" >&2; exit 1 ;;
esac

if [ "${version}" = "latest" ]; then
  latest_url="$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/${repository}/releases/latest")"
  version="${latest_url##*/}"
fi
if ! printf '%s\n' "${version}" | grep -Eq '^v[0-9][0-9A-Za-z._-]*$'; then
  echo "Invalid release version: ${version}" >&2
  exit 1
fi

asset="agentmem_${version}_darwin_${release_architecture}.tar.gz"
base_url="https://github.com/${repository}/releases/download/${version}"
temporary_root="$(mktemp -d "${TMPDIR:-/tmp}/agentmem-install.XXXXXX")"
staged=""
cleanup() {
  rm -rf -- "${temporary_root}"
  if [ -n "${staged}" ] && [ -f "${staged}" ]; then
    rm -f -- "${staged}"
  fi
}
trap cleanup EXIT HUP INT TERM

curl -fL "${base_url}/${asset}" -o "${temporary_root}/${asset}"
curl -fL "${base_url}/SHA256SUMS" -o "${temporary_root}/SHA256SUMS"
expected="$(awk -v file="${asset}" '$2 == file || $2 == "./" file || $2 == "*" file { print $1 }' "${temporary_root}/SHA256SUMS")"
if [ -z "${expected}" ] || [ "$(printf '%s\n' "${expected}" | wc -l | tr -d ' ')" -ne 1 ]; then
  echo "Release checksum entry is missing or duplicated for ${asset}" >&2
  exit 1
fi
actual="$(shasum -a 256 "${temporary_root}/${asset}" | awk '{ print $1 }')"
if [ "${actual}" != "${expected}" ]; then
  echo "Release checksum verification failed for ${asset}" >&2
  exit 1
fi

mkdir -p "${temporary_root}/extract"
tar -xzf "${temporary_root}/${asset}" -C "${temporary_root}/extract"
if [ ! -f "${temporary_root}/extract/agentmem" ]; then
  echo "Release archive does not contain agentmem" >&2
  exit 1
fi
mkdir -p "${install_dir}"
staged="$(mktemp "${install_dir}/.agentmem.XXXXXX")"
install -m 0755 "${temporary_root}/extract/agentmem" "${staged}"
"${staged}" version >/dev/null
mv -f "${staged}" "${install_dir}/agentmem"
staged=""
printf 'Installed %s to %s\n' "${version}" "${install_dir}/agentmem"
printf 'Add %s to PATH if it is not already available.\n' "${install_dir}"
