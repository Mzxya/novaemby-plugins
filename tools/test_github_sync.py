#!/usr/bin/env python3
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

import github_sync as sync


class SourceSyncTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="nova-source-sync-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.local = self.root / "local"
        self.remote = self.root / "checkout"
        self.local.mkdir()
        self.remote.mkdir()
        self.write(self.local, "plugins/playback-history/manifest.json", json.dumps({"id": "playback-history", "version": "0.4.2"}))
        self.write(self.local, "plugins/playback-history/main.go", "package main\n")
        self.write(self.local, "plugins/playback-history/README.md", "当前版本 0.4.2。\n")
        self.write(self.local, "plugins/playback-history/assets/app.js", 'console.log("hello");\n')
        self.write(self.local, "go.mod", "module example.test/plugin\n")
        self.git("init", "-b", "main")
        self.git("config", "user.name", "test")
        self.git("config", "user.email", "test@example.invalid")
        self.write(self.remote, "README.md", "# Upstream\n")
        self.write(self.remote, "index.json", '{"signed":"preserve"}\n')
        self.write(self.remote, "index.json.sig", "preserve-signature\n")
        self.write(self.remote, "plugins/hello/main.go", "package main\n")
        self.commit()

    @staticmethod
    def write(root, rel, text):
        path = root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)

    def git(self, *args):
        return subprocess.check_output(["git", "-C", str(self.remote), *args], text=True, stderr=subprocess.DEVNULL)

    def commit(self):
        self.git("add", "-A")
        self.git("commit", "-qm", "test")

    def prepare(self):
        captured = Path(tempfile.mkdtemp(dir=self.root))
        sync.snapshot(self.local, captured)
        plan = captured / "plan.json"
        sync.prepare(self.local, captured, self.remote, plan)
        return plan, json.loads(plan.read_text())

    def first_upload(self):
        plan, data = self.prepare()
        self.assertTrue(data["changed"])
        self.assertEqual(data["version"], "0.4.2")
        self.commit()
        sync.finish(self.local, plan)

    def test_first_upload_preserves_other_plugins_and_signed_index(self):
        self.first_upload()
        self.assertEqual((self.remote / "index.json").read_text(), '{"signed":"preserve"}\n')
        self.assertEqual((self.remote / "index.json.sig").read_text(), "preserve-signature\n")
        self.assertTrue((self.remote / "plugins/hello/main.go").exists())

    def test_repeat_is_noop_then_changes_bump_once_and_stay_idempotent(self):
        self.first_upload()
        _, plan = self.prepare()
        self.assertFalse(plan["changed"])
        self.write(self.local, "plugins/playback-history/assets/app.js", 'console.log("changed");\n')
        path, plan = self.prepare()
        self.assertEqual(plan["version"], "0.4.3")
        # Preparing a push must not prematurely mutate the source version.
        self.assertEqual(json.loads((self.local / sync.PLUGIN / "manifest.json").read_text())["version"], "0.4.2")
        self.commit()
        sync.finish(self.local, path)
        self.assertIn("0.4.3", (self.local / sync.PLUGIN / "README.md").read_text())
        self.assertTrue((self.local / sync.PLUGIN / "CHANGELOG/0.4.3.md").exists())
        _, plan = self.prepare()
        self.assertFalse(plan["changed"])

    def test_explicit_version_is_respected(self):
        self.first_upload()
        self.write(self.local, f"{sync.PLUGIN}/manifest.json", json.dumps({"id": "playback-history", "version": "0.5.0"}))
        path, plan = self.prepare()
        self.assertEqual(plan["version"], "0.5.0")
        sync.finish(self.local, path)
        self.assertIn("0.5.0", (self.local / sync.PLUGIN / "README.md").read_text())
        self.assertTrue((self.local / sync.PLUGIN / "CHANGELOG/0.5.0.md").exists())

    def test_removed_source_is_removed_on_remote(self):
        self.first_upload()
        self.write(self.remote, f"{sync.PLUGIN}/obsolete.go", "package main\n")
        self.commit()
        _, plan = self.prepare()
        self.assertFalse((self.remote / sync.PLUGIN / "obsolete.go").exists())
        self.assertEqual(plan["version"], "0.4.3")

    def test_credentials_installers_and_runtime_data_are_not_snapshotted(self):
        for rel in [".env", ".aws/credentials", "plugins/插件安装包/plugin.zip", f"{sync.PLUGIN}/.env", f"{sync.PLUGIN}/data/history.jsonl", f"{sync.PLUGIN}/private.key", f"{sync.PLUGIN}/assets/.env"]:
            self.write(self.local, rel, "do-not-upload")
        captured = self.root / "snapshot"
        sync.snapshot(self.local, captured)
        paths = json.loads((captured / "snapshot.json").read_text())["files"]
        self.assertFalse(any(name.endswith((".env", ".zip", ".jsonl", ".key")) for name in paths))
        self.assertFalse(any(".aws" in name for name in paths))

    def test_secret_in_public_source_blocks_snapshot(self):
        self.write(self.local, f"{sync.PLUGIN}/assets/app.js", "const token = '" + "ghp_" + "a" * 40 + "';")
        with self.assertRaisesRegex(ValueError, "疑似密钥"):
            sync.snapshot(self.local, self.root / "snapshot")

    def test_remote_newer_version_blocks_overwrite(self):
        self.first_upload()
        self.write(self.remote, f"{sync.PLUGIN}/manifest.json", json.dumps({"id": "playback-history", "version": "0.9.0"}))
        self.commit()
        with self.assertRaisesRegex(ValueError, "远端版本"):
            self.prepare()

    def test_concurrent_local_edits_are_not_overwritten_after_push(self):
        self.first_upload()
        self.write(self.local, f"{sync.PLUGIN}/main.go", "package main\n// new\n")
        path, _ = self.prepare()
        self.write(self.local, f"{sync.PLUGIN}/manifest.json", json.dumps({"id": "playback-history", "version": "1.0.0"}))
        with self.assertRaisesRegex(ValueError, "本地文件"):
            sync.finish(self.local, path)
        self.assertEqual(json.loads((self.local / sync.PLUGIN / "manifest.json").read_text())["version"], "1.0.0")


if __name__ == "__main__":
    unittest.main()
