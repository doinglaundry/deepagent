#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bin_dir="${repo_root}/.eino-cli/bin"
install_dir="${SGADK_INSTALL_DIR:-${HOME}/.local/bin}"

mkdir -p "${bin_dir}" "${install_dir}"

(cd "${repo_root}" && go build -o "${bin_dir}/sgadk" .)

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
echo "Run: sgadk"
