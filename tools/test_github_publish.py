#!/usr/bin/env python3
import base64
from contextlib import redirect_stderr, redirect_stdout
import io
import json
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import github_publish as publish


class PublishTests(unittest.TestCase):
    def setUp(self):
        self.repo = "jenley/novaemby-plugins"
        self.version = "0.5.0"
        self.url = f"https://github.com/{self.repo}/releases/download/playback-history-v0.5.0/playback-history-0.5.0.zip"
        self.item = {"version": self.version, "url": self.url, "size": 1024, "sha256": "a" * 64}
        self.release = {"draft": False, "assets": [{"name": "playback-history-0.5.0.zip", "state": "uploaded", "size": 1024, "browser_download_url": self.url, "digest": "sha256:" + "a" * 64}]}
        self.out = io.StringIO()
        self.stderr = redirect_stderr(self.out)
        self.stdout = redirect_stdout(self.out)
        self.stderr.__enter__()
        self.stdout.__enter__()
        self.addCleanup(self.stderr.__exit__, None, None, None)
        self.addCleanup(self.stdout.__exit__, None, None, None)

    def encoded(self, plugins):
        return {"encoding": "base64", "content": base64.b64encode(json.dumps({"plugins": plugins}).encode()).decode()}

    def verify(self, item=None, release=None):
        idx = self.encoded([{"id": "playback-history", "versions": [item or self.item]}])
        with patch.object(publish, "api", side_effect=[idx, release or self.release]):
            return publish.verify(self.repo, "main", self.version)

    def test_release_and_index_agree(self):
        self.assertTrue(self.verify())
        self.assertIn("发布验证成功", self.out.getvalue())

    def test_release_exists_but_index_missing(self):
        with patch.object(publish, "api", return_value=self.encoded([])) as api:
            self.assertFalse(publish.verify(self.repo, "main", self.version))
            self.assertEqual(api.call_count, 1)
        self.assertIn("索引尚未包含", self.out.getvalue())

    def test_index_contains_only_older_version(self):
        old = {**self.item, "version": "0.4.2"}
        with patch.object(publish, "api", return_value=self.encoded([{"id": "playback-history", "versions": [old]}])):
            self.assertFalse(publish.verify(self.repo, "main", self.version))

    def test_invalid_index_asset_metadata(self):
        for field, value in [("url", "dist/file.zip"), ("sha256", "not-sha256"), ("size", 0)]:
            with self.subTest(field=field):
                idx = self.encoded([{"id": "playback-history", "versions": [{**self.item, field: value}]}])
                with patch.object(publish, "api", return_value=idx):
                    self.assertFalse(publish.verify(self.repo, "main", self.version))

    def test_missing_or_mismatching_release_asset(self):
        for rel in [{**self.release, "draft": True}, {"assets": []}, {"assets": [{**self.release["assets"][0], "size": 123}]}, {"assets": [{**self.release["assets"][0], "digest": "sha256:" + "b" * 64}]}]:
            with self.subTest(rel=rel):
                self.assertFalse(self.verify(release=rel))

    def test_wait_does_not_accept_old_success(self):
        runs = {"workflow_runs": [
            {"id": 1, "head_sha": "old", "event": "push", "status": "completed", "conclusion": "success"},
            {"id": 2, "head_sha": "current", "event": "push", "status": "in_progress", "html_url": "https://example.invalid/2"},
        ]}
        with patch.object(publish, "api", return_value=runs), patch.object(publish.subprocess, "run", return_value=SimpleNamespace(returncode=0)) as run:
            publish.wait_for_build(self.repo, "current")
        self.assertIn("watch", run.call_args.args[0])
        self.assertIn("2", run.call_args.args[0])

    def test_failed_build_prints_failed_logs(self):
        runs = {"workflow_runs": [{"id": 3, "head_sha": "current", "event": "push", "status": "completed", "conclusion": "failure", "html_url": "https://example.invalid/3"}]}
        with patch.object(publish, "api", return_value=runs), patch.object(publish.subprocess, "run") as run:
            with self.assertRaisesRegex(RuntimeError, "构建详情"):
                publish.wait_for_build(self.repo, "current")
        self.assertIn("--log-failed", run.call_args.args[0])

    def test_new_active_rerun_is_preferred_over_old_failure(self):
        runs = {"workflow_runs": [
            {"id": 1, "head_sha": "current", "event": "push", "status": "completed", "conclusion": "failure"},
            {"id": 4, "head_sha": "current", "event": "workflow_dispatch", "status": "queued", "html_url": "https://example.invalid/4"},
        ]}
        with patch.object(publish, "api", return_value=runs), patch.object(publish.subprocess, "run", return_value=SimpleNamespace(returncode=0)) as run:
            publish.wait_for_build(self.repo, "current")
        self.assertIn("4", run.call_args.args[0])

    def test_wait_times_out_without_uploading_anything(self):
        with patch.object(publish, "api", return_value={"workflow_runs": []}), patch.object(publish.time, "sleep"), patch.object(publish.subprocess, "run") as run:
            with self.assertRaisesRegex(RuntimeError, "未找到对应构建"):
                publish.wait_for_build(self.repo, "current")
        run.assert_not_called()


if __name__ == "__main__":
    unittest.main()
