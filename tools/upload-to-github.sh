#!/usr/bin/env bash
# 在可联网的 SSH 主机运行；不需要把 GitHub 密码或 Token 发给任何人。
set -euo pipefail

task_owner="${1:-jenley}"
if [[ "$task_owner" != "jenley" ]]; then
  echo '目标账户固定为本次指定的 jenley。' >&2
  exit 1
fi
for task_command in git curl python3; do
  command -v "$task_command" >/dev/null || { echo "缺少命令：$task_command" >&2; exit 1; }
done
# 默认使用用户指定的代理；统一大小写，避免 curl 能下载但 gh 登录直连。
# 只作用于本脚本及子进程，不修改系统代理或用户的全局 Git 代理配置。
task_proxy="${NOVA_GITHUB_PROXY:-http://10.0.0.120:8888}"
export HTTP_PROXY="$task_proxy" HTTPS_PROXY="$task_proxy" ALL_PROXY="$task_proxy"
export http_proxy="$task_proxy" https_proxy="$task_proxy" all_proxy="$task_proxy"
export NO_PROXY="localhost,127.0.0.1,::1" no_proxy="localhost,127.0.0.1,::1"
echo "已为下载、GitHub 登录和 Git 上传统一启用代理。"

echo "[1/6] 读取当前插件源码…"
task_script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
task_project_root="$(cd -- "$task_script_dir/.." && pwd)"
task_sync="$task_script_dir/github_sync.py"
if [[ ! -f "$task_sync" ]]; then
  echo "缺少源码同步工具：$task_sync" >&2
  exit 1
fi

task_tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/nova-github-upload.XXXXXX")"
task_checkout="$task_tmp_dir/repository"
task_done=0
cleanup() {
  if [[ "$task_done" == 1 || ! -d "$task_checkout/.git" ]]; then
    rm -rf -- "$task_tmp_dir"
  else
    echo "上传未完成，保留检查目录：$task_checkout" >&2
  fi
}
trap cleanup EXIT

task_snapshot="$task_tmp_dir/source-snapshot"
task_plan="$task_tmp_dir/upload-plan.json"
python3 "$task_sync" snapshot "$task_project_root" "$task_snapshot"

# 安装文件保存在用户缓存中，网络失败后重试不会丢失已下载的程序包。
echo "[2/6] 准备 GitHub 工具…"
if ! command -v gh >/dev/null 2>&1; then
  case "$(uname -s)/$(uname -m)" in
    Linux/x86_64) task_arch=amd64 ;;
    Linux/aarch64|Linux/arm64) task_arch=arm64 ;;
    *) echo '自动安装仅支持 Linux amd64/arm64，请先安装 GitHub CLI。' >&2; exit 1 ;;
  esac
  task_cli_root="${XDG_DATA_HOME:-$HOME/.local/share}/nova-plugin-tools"
  if [[ ! -x "$task_cli_root/bin/gh" ]]; then
    task_cache="${XDG_CACHE_HOME:-$HOME/.cache}/nova-plugin-tools"
    mkdir -p -- "$task_cache"
    # 兼容上版尚未清理的下载文件；无法读取的其他用户目录自动跳过。
    python3 - "$task_cache" "${TMPDIR:-/tmp}" <<'PYRECOVER'
from pathlib import Path
import json,shutil,sys,tarfile
cache,temps=map(Path,sys.argv[1:])
if not (cache/'gh-release.json').exists():
    for old in sorted(temps.glob('nova-github-upload.*'),key=lambda p:p.name,reverse=True):
        try:
            meta=json.loads((old/'gh-release.json').read_text())
            assets=meta['assets']
            with tarfile.open(old/'gh.tar.gz') as archive:
                name=archive.getmembers()[0].name.split('/')[0]+'.tar.gz'
            if not any(a['name']==name for a in assets): continue
            shutil.copyfile(old/'gh-release.json',cache/'gh-release.json')
            shutil.copyfile(old/'gh.tar.gz',cache/name)
            print('已找到并复用上次下载的程序包。')
            break
        except (OSError,ValueError,KeyError,IndexError,tarfile.TarError): continue
PYRECOVER
    download() {
      local task_url="$1" task_dest="$2" task_label="$3"
      echo "$task_label（若网络无响应约 20 秒，将自动重试）"
      if ! curl -fLsS --connect-timeout 10 --max-time 180 --speed-limit 100 --speed-time 20 \
        --retry 1 --retry-delay 2 "$task_url" -o "$task_dest.part"; then
        rm -f -- "$task_dest.part"
        echo "网络暂时无法完成下载。已保留成功下载的文件，可重新运行同一条命令重试。" >&2
        return 1
      fi
      mv -- "$task_dest.part" "$task_dest"
    }
    if [[ ! -s "$task_cache/gh-release.json" ]]; then
      download https://api.github.com/repos/cli/cli/releases/latest "$task_cache/gh-release.json" '正在获取官方版本信息'
    else
      echo '复用已缓存的官方版本信息。'
    fi
    python3 - "$task_cache/gh-release.json" "$task_arch" > "$task_tmp_dir/gh-assets.txt" <<'PYASSETS'
import json,re,sys
assets=json.load(open(sys.argv[1]))['assets']
package=next(a for a in assets if a['name'].endswith('_linux_'+sys.argv[2]+'.tar.gz'))
sums=next((a['browser_download_url'] for a in assets if a['name'].endswith('_checksums.txt')),'')
url=package['browser_download_url'];name=package['name']
assert url.startswith('https://github.com/cli/cli/releases/download/')
assert re.fullmatch(r'[A-Za-z0-9_.-]+',name)
assert not sums or sums.startswith('https://github.com/cli/cli/releases/download/')
digest=package.get('digest') or ''
sha=digest[7:] if re.fullmatch(r'sha256:[a-fA-F0-9]{64}',digest) else ''
assert sha or sums,'官方元数据缺少可用校验信息，停止安装'
print(url);print(name);print(sha);print(sums)
PYASSETS
    mapfile -t task_assets < "$task_tmp_dir/gh-assets.txt"
    task_asset_url="${task_assets[0]}"
    task_asset_name="${task_assets[1]}"
    task_expected_sha="${task_assets[2]}"
    task_sums_url="${task_assets[3]}"
    task_archive="$task_cache/$task_asset_name"
    if [[ ! -s "$task_archive" ]]; then
      download "$task_asset_url" "$task_archive" '正在下载 GitHub 工具，约十几 MB'
    else
      echo '复用已下载的 GitHub 工具，接下来核对完整性。'
    fi
    if [[ -n "$task_expected_sha" ]]; then
      echo '使用 GitHub 官方版本信息中的 SHA256 校验，无需再下载校验文件。'
    else
      download "$task_sums_url" "$task_cache/checksums.txt" '正在下载官方校验文件'
    fi
    python3 - "$task_archive" "$task_cache/checksums.txt" "$task_expected_sha" "$task_cli_root" <<'PYINSTALL'
from pathlib import Path
import hashlib,shutil,sys,tarfile
archive,sums=map(Path,sys.argv[1:3]);expected=sys.argv[3]
if not expected:
    expected=next(row.split()[0] for row in sums.read_text().splitlines() if len(row.split())>=2 and row.split()[-1].lstrip('*')==archive.name)
if hashlib.sha256(archive.read_bytes()).hexdigest().lower()!=expected.lower():
    archive.unlink()
    raise SystemExit('下载文件不完整或校验不匹配，已删除；请重新运行命令下载。')
dest=Path(sys.argv[4])/'bin'/'gh';dest.parent.mkdir(parents=True,exist_ok=True)
with tarfile.open(archive) as package:
    member=next(m for m in package.getmembers() if m.isfile() and m.name.endswith('/bin/gh'))
    with package.extractfile(member) as source,dest.with_suffix('.new').open('wb') as target:shutil.copyfileobj(source,target)
dest.with_suffix('.new').chmod(0o755)
dest.with_suffix('.new').replace(dest)
print('GitHub 工具已安装并通过 SHA256 校验。')
PYINSTALL
  fi
  export PATH="$task_cli_root/bin:$PATH"
fi

# 设备登录会在终端给出验证码；用户在自己的浏览器中确认。
echo "[3/6] 检查 GitHub 登录…"
echo "正在通过代理检查授权页面，最多等待 20 秒…"
if ! curl -fLsS --proxy "$task_proxy" --connect-timeout 10 --max-time 20   https://github.com/login/device -o /dev/null; then
  echo "暂时无法通过代理连接 GitHub 授权页面，尚未开始登录或上传。请确认代理可访问 GitHub 后重试同一条命令。" >&2
  exit 1
fi
if ! gh auth status --hostname github.com >/dev/null 2>&1; then
  echo "请留意下方的一次性验证码，在浏览器打开 https://github.com/login/device，用 jenley 登录并输入验证码。"
  echo "若提示 Press Enter，请按回车。无需把验证码、密码或 Token 发给助手。"
  if ! gh auth login --hostname github.com --git-protocol https --web --scopes workflow; then
    echo "GitHub 网页授权未完成，插件尚未上传。工具已安装，无需重新下载；可重跑本脚本继续。" >&2
    exit 1
  fi
fi
task_login="$(gh api user --jq .login)"
if [[ "${task_login,,}" != "$task_owner" ]]; then
  echo "当前登录账户为 $task_login，需要登录 jenley 后再运行。" >&2
  exit 1
fi
gh auth setup-git --hostname github.com

echo "[4/6] 准备 jenley 的 fork 仓库…"
task_repo="$task_owner/novaemby-plugins"
if gh api "repos/$task_repo" >/dev/null 2>&1; then
  task_source="$(gh api "repos/$task_repo" --jq '.source.full_name // .parent.full_name // ""')"
  if [[ "${task_source,,}" != "mzxya/novaemby-plugins" ]]; then
    echo "$task_repo 已存在，但不是指定上游的 fork；停止以免上传到错误仓库。" >&2
    exit 1
  fi
else
  gh repo fork Mzxya/novaemby-plugins --clone=false
fi
task_branch="$(gh api "repos/$task_repo" --jq .default_branch)"
echo "[5/6] 比较本地与远端源码，准备本次版本…"
git -c http.proxy="$task_proxy" -c http.https://github.com.proxy="$task_proxy" clone --branch "$task_branch" "https://github.com/$task_repo.git" "$task_checkout"
git -C "$task_checkout" config user.name "$task_login"
task_email="$(gh api user --jq '"\(.id)+\(.login)@users.noreply.github.com"')"
git -C "$task_checkout" config user.email "$task_email"
python3 "$task_sync" prepare "$task_project_root" "$task_snapshot" "$task_checkout" "$task_plan"
mapfile -t task_plan_values < <(python3 - "$task_plan" <<'PYPLAN'
import json,sys
p=json.load(open(sys.argv[1]))
print(p['version']);print('yes' if p['changed'] else 'no')
PYPLAN
)
task_version="${task_plan_values[0]}"
task_changed="${task_plan_values[1]}"

echo "[6/6] 同步到 GitHub 并安排新版构建…"
# 启用 fork 中原有的发布工作流，让默认分支推送可自动构建安装包。
if ! gh workflow enable release.yml --repo "$task_repo"; then
  echo "无法启用 GitHub 构建流程。请检查仓库 Actions 权限；本次源码尚未推送，可重跑同一命令。" >&2
  exit 1
fi
if [[ "$task_changed" == yes ]]; then
  git -C "$task_checkout" -c commit.gpgsign=false commit \
    -m "Update playback-history to $task_version" \
    -m "Sync current local source, tests and build configuration." \
    -m "Nova-Plugin-Upload: playback-history-$task_version"
  git -C "$task_checkout" -c http.proxy="$task_proxy" -c http.https://github.com.proxy="$task_proxy" push origin "HEAD:$task_branch"
  python3 "$task_sync" finish "$task_project_root" "$task_plan"
  echo "源码已更新到 $task_version，GitHub 将自动检查并构建安装包。"
else
  # 没有代码变化时，只重试尚未生成安装包的构建，不制造新版本。
  if python3 "$task_script_dir/github_publish.py" check "$task_repo" "$task_branch" "$task_version"; then
    echo "版本 $task_version 的安装包与索引均已确认，无需重新发布。"
  else
    gh run list --repo "$task_repo" --workflow release.yml --branch "$task_branch" --limit 10 \
      --json headSha,status > "$task_tmp_dir/runs.json"
    task_head="$(git -C "$task_checkout" rev-parse HEAD)"
    task_running="$(python3 - "$task_tmp_dir/runs.json" "$task_head" <<'PYRUN'
import json,sys
runs=json.load(open(sys.argv[1]))
print('yes' if any(r.get('headSha')==sys.argv[2] and r.get('status')!='completed' for r in runs) else 'no')
PYRUN
)"
    if [[ "$task_running" == yes ]]; then
      echo "当前版本正在构建，请稍后查看安装包。"
    else
      gh workflow run release.yml --repo "$task_repo" --ref "$task_branch"
      echo "已安排构建当前版本 $task_version，不增加版本号。"
    fi
  fi
fi
task_commit="$(git -C "$task_checkout" rev-parse HEAD)"
echo "源码已同步，继续确认构建和索引，可能需要几分钟…"
if ! python3 "$task_script_dir/github_publish.py" check "$task_repo" "$task_branch" "$task_version"; then
  python3 "$task_script_dir/github_publish.py" wait "$task_repo" "$task_commit"
  if ! python3 "$task_script_dir/github_publish.py" check "$task_repo" "$task_branch" "$task_version"; then
    echo "构建显示成功，但索引或安装包仍不完整。请把本次终端输出发来排查；不要手动向索引填假地址。" >&2
    exit 1
  fi
fi
echo "完成：https://github.com/$task_repo/commit/$task_commit"
echo "构建进度：https://github.com/$task_repo/actions"
echo "安装包：https://github.com/$task_repo/releases"
task_done=1
