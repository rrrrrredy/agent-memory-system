#!/bin/sh
set -eu

if [ "$(uname -s)" != "Darwin" ]; then
  echo "The macOS installer test requires macOS." >&2
  exit 1
fi

repository_root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
temporary_parent="${TMPDIR:-/tmp}"
temporary_parent="${temporary_parent%/}"
[ -n "${temporary_parent}" ] || temporary_parent="/"
temporary_root="$(mktemp -d "${temporary_parent}/agentmem-installer-test.XXXXXX")"
artifact_root="${temporary_root}/artifacts"
install_root="${temporary_root}/installed"
mock_bin="${temporary_root}/mock-bin"
sentinel="${temporary_root}/evidence-sentinel.txt"

cleanup() {
  case "${temporary_root}" in
    "${temporary_parent}"/agentmem-installer-test.*) rm -rf -- "${temporary_root}" ;;
    *) echo "Refusing to remove unexpected test path: ${temporary_root}" >&2 ;;
  esac
}
trap cleanup EXIT HUP INT TERM

case "$(uname -m)" in
  arm64) release_architecture="arm64" ;;
  x86_64) release_architecture="amd64" ;;
  *) echo "Unsupported macOS test architecture: $(uname -m)" >&2; exit 1 ;;
esac

mkdir -p "${artifact_root}" "${install_root}" "${mock_bin}"
printf '%s\n' "preserve local evidence" > "${sentinel}"

cat > "${mock_bin}/curl" <<'EOF'
#!/bin/sh
set -eu
output=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) output="$2"; shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
if [ -z "${output}" ] || [ -z "${url}" ]; then
  echo "Mock curl requires a URL and output path." >&2
  exit 1
fi
cp "${AGENTMEM_TEST_ASSET_ROOT}/${url##*/}" "${output}"
EOF
chmod 0755 "${mock_bin}/curl"

build_release() {
  version="$1"
  binary_version="${2:-${version}}"
  asset="agentmem_${version}_darwin_${release_architecture}.tar.gz"
  package_root="${temporary_root}/package-${version}"
  mkdir -p "${package_root}"
  (
    cd "${repository_root}"
    CGO_ENABLED=0 GOOS=darwin GOARCH="${release_architecture}" \
      go build -trimpath -buildvcs=false -ldflags "-s -w -X main.version=${binary_version}" \
      -o "${package_root}/agentmem" ./cmd/agentmem
  )
  tar -C "${package_root}" -czf "${artifact_root}/${asset}" agentmem
  digest="$(shasum -a 256 "${artifact_root}/${asset}" | awk '{ print $1 }')"
  printf '%s  %s\n' "${digest}" "${asset}" > "${artifact_root}/SHA256SUMS"
}

export AGENTMEM_TEST_ASSET_ROOT="${artifact_root}"
PATH="${mock_bin}:${PATH}"
export PATH

for version in v0.0.0-test v0.0.1-test; do
  build_release "${version}"
  AGENTMEM_VERSION="${version}" AGENTMEM_INSTALL_DIR="${install_root}" \
    sh "${repository_root}/scripts/install.sh" >/dev/null
  "${install_root}/agentmem" version | grep -F "\"version\": \"${version}\"" >/dev/null
  if [ "$(cat "${sentinel}")" != "preserve local evidence" ]; then
    echo "Installer changed data outside the binary directory." >&2
    exit 1
  fi
done

installed_digest="$(shasum -a 256 "${install_root}/agentmem" | awk '{ print $1 }')"
asset="agentmem_v0.0.1-test_darwin_${release_architecture}.tar.gz"
printf '%s\n' "tampered" >> "${artifact_root}/${asset}"
if AGENTMEM_VERSION="v0.0.1-test" AGENTMEM_INSTALL_DIR="${install_root}" \
  sh "${repository_root}/scripts/install.sh" >/dev/null 2>&1; then
  echo "Installer accepted a release archive with the wrong checksum." >&2
  exit 1
fi
if [ "$(shasum -a 256 "${install_root}/agentmem" | awk '{ print $1 }')" != "${installed_digest}" ]; then
  echo "Failed checksum verification changed the installed binary." >&2
  exit 1
fi

build_release v0.0.2-test v9.9.9-test
if AGENTMEM_VERSION="v0.0.2-test" AGENTMEM_INSTALL_DIR="${install_root}" \
  sh "${repository_root}/scripts/install.sh" >/dev/null 2>&1; then
  echo "Installer accepted a release whose binary version mismatched its tag." >&2
  exit 1
fi
if [ "$(shasum -a 256 "${install_root}/agentmem" | awk '{ print $1 }')" != "${installed_digest}" ]; then
  echo "Failed version verification changed the installed binary." >&2
  exit 1
fi

for installed_path in "${install_root}"/.[!.]* "${install_root}"/..?* "${install_root}"/*; do
  [ -e "${installed_path}" ] || continue
  if [ "${installed_path}" != "${install_root}/agentmem" ]; then
    echo "Installer left an unexpected file: ${installed_path}" >&2
    exit 1
  fi
done
