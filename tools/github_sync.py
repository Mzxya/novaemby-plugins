#!/usr/bin/env python3
"""Capture public source files, prepare a versioned commit, then record successful sync."""
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import tempfile

PLUGIN = "plugins/playback-history"
EXCLUDED = {".git", ".aws", ".codex", ".agents", "node_modules", "__pycache__", "data", "bin", "dist"}
SOURCE_EXT = {".go", ".md"}
ASSET_EXT = {".js", ".css", ".html", ".svg", ".png", ".jpg", ".jpeg", ".webp", ".gif", ".ico", ".woff", ".woff2", ".ttf", ".otf", ".json"}
INFRA = ("go.mod", "go.sum", "tools/test-web.mjs", "tools/upload-to-github.sh", "tools/github_sync.py", "tools/test_github_sync.py", "tools/github_publish.py", "tools/test_github_publish.py", ".github/workflows/release.yml")
BLOCKED_CONTENT = (
    re.compile(rb"-----BEGIN (?:[A-Z0-9]+ )?PRIVATE KEY-----"),
    re.compile(rb"\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{30,})\b"),
    re.compile(rb"\b(?:AKIA|ASIA)[A-Z0-9]{16}\b"),
)


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest() if path.is_file() else None


def version(value):
    if not isinstance(value, str) or not re.fullmatch(r"\d+\.\d+\.\d+", value):
        raise ValueError("版本号必须是数字格式，例如 0.4.3")
    return tuple(map(int, value.split(".")))


def write_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")


def excluded(relative):
    return any(part.startswith(".") or part in EXCLUDED for part in relative.parts)


def snapshot(root, out):
    root, out = root.resolve(), out.resolve()
    plugin = root / PLUGIN
    manifest = json.loads((plugin / "manifest.json").read_text())
    if manifest.get("id") != "playback-history":
        raise ValueError("插件 ID 与上传目标不一致")
    version(manifest.get("version"))
    selected = {f"{PLUGIN}/manifest.json"}
    for path in plugin.rglob("*"):
        rel = path.relative_to(plugin)
        if excluded(rel):
            continue
        if path.is_symlink():
            raise ValueError(f"源码不能包含符号链接：{PLUGIN}/{rel}")
        if not path.is_file():
            continue
        if path.suffix.lower() in SOURCE_EXT or path.name in {"LICENSE", "NOTICE"} or (rel.parts[0] == "assets" and path.suffix.lower() in ASSET_EXT):
            selected.add(path.relative_to(root).as_posix())
    for rel in INFRA:
        if (root / rel).is_file():
            selected.add(rel)
    for folder in ("sdk", "tools/mkrepo"):
        for path in (root / folder).rglob("*.go"):
            if not excluded(path.relative_to(root / folder)):
                selected.add(path.relative_to(root).as_posix())
    files = {}
    for rel in sorted(selected):
        src = root / rel
        if src.is_symlink():
            raise ValueError(f"源码不能包含符号链接：{rel}")
        data = src.read_bytes()
        if any(pattern.search(data) for pattern in BLOCKED_CONTENT):
            raise ValueError(f"发现疑似密钥内容，已停止上传；请检查文件：{rel}")
        dest = out / "files" / rel
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_bytes(data)
        dest.chmod(0o644)
        files[rel] = hashlib.sha256(data).hexdigest()
    write_json(out / "snapshot.json", {"version": manifest["version"], "files": files})
    print(f"已读取当前版本 {manifest['version']}，准备同步 {len(files)} 个源码文件。")


def git(checkout, *args):
    return subprocess.check_output(["git", "-C", str(checkout), *args], text=True)


def ensure_regular_parents(root, rel):
    for parent in (root / rel).parents:
        if parent == root:
            break
        if parent.is_symlink():
            raise ValueError(f"目标目录包含符号链接：{rel}")


def prepare(root, captured, checkout, plan_path):
    root, captured, checkout = root.resolve(), captured.resolve(), checkout.resolve()
    snap = json.loads((captured / "snapshot.json").read_text())
    old_path = checkout / PLUGIN / "manifest.json"
    remote = json.loads(old_path.read_text())["version"] if old_path.is_file() else None
    local = snap["version"]
    if remote and version(remote) > version(local):
        raise ValueError(f"远端版本 {remote} 高于本地 {local}，请先同步新版源码，避免覆盖较新的版本")
    # Only this plugin's source tree is authoritative. Other plugins and release indexes stay intact.
    target = checkout / PLUGIN
    ensure_regular_parents(checkout, PLUGIN)
    if target.is_symlink():
        raise ValueError("目标插件目录是符号链接，停止同步")
    if target.exists():
        shutil.rmtree(target)
    for rel, expected in snap["files"].items():
        src = captured / "files" / rel
        if digest(src) != expected:
            raise ValueError(f"源码快照校验失败：{rel}")
        ensure_regular_parents(checkout, rel)
        dest = checkout / rel
        if dest.is_symlink():
            raise ValueError(f"目标文件是符号链接：{rel}")
        dest.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(src, dest)
        dest.chmod(0o644)
    readme = checkout / "README.md"
    content = readme.read_text() if readme.exists() else "# NovaEmby 插件\n"
    if "plugins/playback-history/README.md" not in content:
        readme.write_text(content.rstrip() + "\n\n## 历史播放记录\n\n手机优先的播放看板与历史查询，见 [插件说明](plugins/playback-history/README.md)。\n")
    ignore = checkout / ".gitignore"
    content = ignore.read_text() if ignore.exists() else ""
    for entry in ("/plugins/插件安装包/", "*.log", ".DS_Store", "__pycache__/", "*.py[cod]"):
        if entry not in content.splitlines():
            content = content.rstrip() + "\n" + entry + "\n"
    ignore.write_text(content)
    managed = sorted(set(snap["files"]) | {PLUGIN, "README.md", ".gitignore"})
    # Resolve any tracked managed files removed from the local snapshot.
    git(checkout, "add", "-A", "--", *managed)
    changes = git(checkout, "diff", "--cached", "--name-only").splitlines()
    new = local
    updates = {}
    if changes and remote and version(local) == version(remote):
        major, minor, patch = version(remote)
        new = f"{major}.{minor}.{patch + 1}"
        manifest_path = f"{PLUGIN}/manifest.json"
        manifest = json.loads((checkout / manifest_path).read_text())
        manifest["version"] = new
        write_json(checkout / manifest_path, manifest)
        updates[manifest_path] = (checkout / manifest_path).read_text()
    if changes:
        rel = f"{PLUGIN}/CHANGELOG/{new}.md"
        if (remote is None or new != remote) and not (checkout / rel).exists():
            origin = f"从 {remote} 升级至 {new}" if remote else f"首次提交版本 {new}"
            text = origin + "，同步本地源码更新。\n\n修改文件：\n\n" + "".join(f"- `{name}`\n" for name in changes)
            (checkout / rel).parent.mkdir(parents=True, exist_ok=True)
            (checkout / rel).write_text(text)
            updates[rel] = text
        rel = f"{PLUGIN}/README.md"
        if (checkout / rel).exists():
            text = (checkout / rel).read_text()
            updated = re.sub(r"(当前版本[：:]?\s*)\d+\.\d+\.\d+", lambda m: m[1] + new, text)
            if updated != text:
                (checkout / rel).write_text(updated)
                updates[rel] = updated
    git(checkout, "add", "-A", "--", *managed)
    git(checkout, "diff", "--cached", "--check")
    changed = bool(git(checkout, "diff", "--cached", "--name-only").strip())
    write_json(plan_path, {"version": new, "remoteVersion": remote, "changed": changed,
                           "updates": {rel: {"before": snap["files"].get(rel), "text": text} for rel, text in updates.items()}})
    if not changed:
        print(f"本地与远端源码一致，当前版本 {new}，无需重复提交。")
    elif new != local:
        print(f"检测到新改动，自动升级版本：{local} → {new}。")
    else:
        print(f"准备提交版本 {new}。")


def finish(root, plan_path):
    plan = json.loads(plan_path.read_text())
    for rel, update in plan["updates"].items():
        if digest(root / rel) != update["before"]:
            raise ValueError(f"源码已上传，但本地文件在上传期间被修改，未覆盖：{rel}；请核对远端版本 {plan['version']}")
    # Version is written last, after all generated documentation has been persisted.
    updates = sorted(plan["updates"], key=lambda rel: rel.endswith("/manifest.json"))
    for rel in updates:
        path = root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        mode = stat.S_IMODE(path.stat().st_mode) if path.exists() else 0o644
        fd, temp = tempfile.mkstemp(prefix=".nova-sync-", dir=path.parent)
        try:
            with os.fdopen(fd, "w") as out:
                out.write(plan["updates"][rel]["text"])
            os.chmod(temp, mode)
            os.replace(temp, path)
        finally:
            if os.path.exists(temp):
                os.unlink(temp)
    print(f"本地版本已与成功上传的版本 {plan['version']} 对齐。")


def main():
    if len(sys.argv) < 2:
        raise ValueError("需要 snapshot / prepare / finish 子命令")
    cmd = sys.argv[1]
    if cmd == "snapshot" and len(sys.argv) == 4:
        snapshot(Path(sys.argv[2]), Path(sys.argv[3]))
    elif cmd == "prepare" and len(sys.argv) == 6:
        prepare(*(Path(value) for value in sys.argv[2:]))
    elif cmd == "finish" and len(sys.argv) == 4:
        finish(Path(sys.argv[2]), Path(sys.argv[3]))
    else:
        raise ValueError("参数不正确")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, KeyError, subprocess.CalledProcessError) as error:
        print(f"同步停止：{error}", file=sys.stderr)
        sys.exit(1)
