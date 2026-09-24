#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bin_dir="${repo_root}/.eino-cli/bin"
install_dir="${SGADK_INSTALL_DIR:-${HOME}/.local/bin}"

mkdir -p "${bin_dir}" "${install_dir}"

(cd "${repo_root}" && go build -o "${bin_dir}/sgadk" ./cmd/deepagent_web)

cat >"${install_dir}/sgadk" <<EOF
#!/usr/bin/env bash
exec "${bin_dir}/sgadk" --root "${repo_root}" "\$@"
EOF
chmod +x "${install_dir}/sgadk"

case ":${PATH}:" in
  *":${install_dir}:"*) ;;
  *)
    echo "Installed sgadk to ${install_dir}/sgadk"
    echo "Add ${install_dir} to PATH, then run: sgadk"
    exit 0
    ;;
esac

echo "Installed sgadk to ${install_dir}/sgadk"
echo "Start a separate Worker with: go run ./cmd/deepagent_worker --config yaml/deepagent.yaml"
echo "Run: sgadk --config yaml/deepagent.yaml --addr :8080, then open http://localhost:8080"
