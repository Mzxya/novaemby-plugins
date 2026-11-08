#!/usr/bin/env python3
"""Verify a plugin's published archive and committed repository index agree."""
import base64
import json
import re
import subprocess
import sys
import time


def api(path):
    result = subprocess.run(["gh", "api", path], text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if result.returncode:
        raise RuntimeError(f"无法读取 GitHub 数据：{path}；请检查连接或仓库权限。")
    return json.loads(result.stdout)


def verify(repository, branch, version):
    from urllib.parse import quote
    obj = api(f"repos/{repository}/contents/index.json?ref={quote(branch, safe='')}")
    if obj.get("encoding") != "base64":
        raise RuntimeError("远端 index.json 不是可读取的普通文件")
    idx = json.loads(base64.b64decode(obj["content"]))
    plugin = next((p for p in idx.get("plugins", []) if p.get("id") == "playback-history"), None)
    item = next((v for v in (plugin or {}).get("versions", []) if v.get("version") == version), None)
    if item is None:
        print(f"索引尚未包含 playback-history {version}。需要完成构建、发布安装包，并提交 index.json。", file=sys.stderr)
        return False
    tag = f"playback-history-v{version}"
    name = f"playback-history-{version}.zip"
    expected = f"https://github.com/{repository}/releases/download/{tag}/{name}"
    if item.get("url") != expected or not re.fullmatch(r"[a-fA-F0-9]{64}", str(item.get("sha256", ""))) or not isinstance(item.get("size"), int) or item["size"] <= 0:
        print("索引中的安装包地址、校验值或大小不正确，需要重新发布。", file=sys.stderr)
        return False
    release = api(f"repos/{repository}/releases/tags/{tag}")
    asset = next((a for a in release.get("assets", []) if a.get("name") == name and a.get("state") == "uploaded"), None)
    if release.get("draft") or asset is None or asset.get("size") != item["size"] or asset.get("browser_download_url") != expected:
        print("索引已更新，但 Release 安装包未完整发布，需要重试构建。", file=sys.stderr)
        return False
    digest = asset.get("digest")
    if digest and digest != "sha256:" + item["sha256"].lower():
        print("索引校验值与 Release 安装包不匹配，需要重新发布。", file=sys.stderr)
        return False
    print(f"发布验证成功：索引已包含 playback-history {version}，并对应可下载的安装包。")
    print(f"插件仓库地址：https://raw.githubusercontent.com/{repository}/{branch}/index.json")
    return True


def wait_for_build(repository, commit):
    for attempt in range(20):
        runs = api(f"repos/{repository}/actions/workflows/release.yml/runs?per_page=30").get("workflow_runs", [])
        matches = [r for r in runs if r.get("head_sha") == commit and r.get("event") in {"push", "workflow_dispatch"}]
        if matches:
            # Prefer an active rerun over an older completed failure.
            matches.sort(key=lambda r: (r.get("status") != "completed", r.get("run_number", 0), r.get("run_attempt", 1), r.get("id", 0)), reverse=True)
            run = matches[0]
            print(f"构建进度：{run.get('html_url', '')}", flush=True)
            if run.get("status") == "completed":
                if run.get("conclusion") == "success":
                    return
                build_failed(repository, run)
            result = subprocess.run(["gh", "run", "watch", str(run["id"]), "--repo", repository, "--exit-status", "--interval", "10"])
            if result.returncode:
                build_failed(repository, run)
            return
        if attempt == 0:
            print("正在等待 GitHub 创建构建任务…", flush=True)
        time.sleep(3)
    raise RuntimeError("源码已上传，但 60 秒内未找到对应构建。请查看仓库 Actions 是否允许工作流运行，然后重跑同一命令。")


def build_failed(repository, run):
    print("GitHub 构建或发布失败，索引尚未确认更新。下面是失败日志：", file=sys.stderr, flush=True)
    subprocess.run(["gh", "run", "view", str(run["id"]), "--repo", repository, "--log-failed"])
    raise RuntimeError(f"请保留上面的错误输出。构建详情：{run.get('html_url', '')}")


def main():
    if len(sys.argv) < 2:
        raise ValueError("需要 check / wait 子命令")
    if sys.argv[1] == "check" and len(sys.argv) == 5:
        return 0 if verify(*sys.argv[2:]) else 1
    if sys.argv[1] == "wait" and len(sys.argv) == 4:
        wait_for_build(*sys.argv[2:])
        return 0
    raise ValueError("参数不正确")


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (ValueError, KeyError, RuntimeError, OSError) as error:
        print(f"发布验证停止：{error}", file=sys.stderr)
        sys.exit(1)
