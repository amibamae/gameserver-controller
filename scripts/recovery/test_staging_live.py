"""Hermetic tests: no SSH connections, Vultr changes, or Wasabi access."""
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import staging_live as live


INSTANCE = "11111111-1111-4111-8111-111111111111"
OLD = "a" * 32
NEW = "b" * 32


def target():
    return live.Target("staging", "recovery-test-paper", INSTANCE,
                       "gsc-test-seattle", "minecraft-vanilla.service")


def sample(invocation=OLD, observed=1000, status="healthy"):
    return {"environment": "staging", "purpose": live.PURPOSE,
            "server_id": "recovery-test-paper", "instance_id": INSTANCE,
            "unit": "minecraft-vanilla.service", "observed_at": observed,
            "probe": {"status": status, "reason": "paper_ready",
                      "invocation_id": invocation}}


class TestStagingTargetSafety(unittest.TestCase):
    def test_explicit_staging_fixture(self):
        target().validate()

    def test_rejects_production_and_ambiguous_targets(self):
        for case in [
            live.Target("production", "recovery-test-paper", INSTANCE, "gsc-test", "minecraft-vanilla.service"),
            live.Target("staging", "vanilla_no_grief", INSTANCE, "gsc-test", "minecraft-vanilla.service"),
            live.Target("staging", "recovery-test-paper", INSTANCE, "vps3691818", "minecraft-vanilla.service"),
            live.Target("staging", "recovery-test-paper", INSTANCE, "-oProxyCommand=evil", "minecraft-vanilla.service"),
            live.Target("staging", "recovery-test-paper", INSTANCE, "gsc-test", "minecraft-production.service"),
            live.Target("staging", "recovery-test-paper", "not-vultr", "gsc-test", "minecraft-vanilla.service"),
        ]:
            with self.subTest(case=case):
                with self.assertRaises(live.UnsafeStagingTarget):
                    case.validate()

    def test_unknown_config_fields_fail_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "staging.json"
            value = dict(target().__dict__)
            value["allow_production"] = True
            path.write_text(json.dumps(value))
            with self.assertRaises(live.UnsafeStagingTarget):
                live.Target.load(path)

    def test_only_fixed_shell_operations_are_embedded(self):
        code = live.REMOTE_OBSERVE + live.REMOTE_RESTORE
        for forbidden in ("systemctl restart", "systemctl kill", "vultr destroy",
                          "rclone sync", "rclone purge", "rm -rf", "hosting.instance.delete"):
            self.assertNotIn(forbidden, code)


class TestFreshPaperRecovery(unittest.TestCase):
    def test_new_healthy_invocation_same_vm(self):
        result = live.verify_recovered(sample(), sample(NEW, 1001), target())
        self.assertEqual(result["result"], "new_invocation_ready")
        self.assertEqual(result["new_invocation"], NEW)

    def test_old_or_unhealthy_or_stale_never_passes(self):
        for before, after in [
            (sample(), sample(OLD, 1001)),
            (sample(), sample(NEW, 999)),
            (sample(), sample(NEW, 1001, "starting")),
            (sample(status="unknown"), sample(NEW, 1001)),
            (sample(), {**sample(NEW, 1001), "instance_id": "different-vm"}),
        ]:
            with self.subTest(before=before, after=after):
                with self.assertRaises(live.UnsafeStagingTarget):
                    live.verify_recovered(before, after, target())

    def test_wrong_environment_unit_or_malformed_evidence_is_rejected(self):
        for altered in [
            {**sample(NEW, 1001), "environment": "production"},
            {**sample(NEW, 1001), "unit": "another.service"},
            {**sample(NEW, 1001), "probe": []},
            [],
        ]:
            with self.subTest(after=altered):
                with self.assertRaises(live.UnsafeStagingTarget):
                    live.verify_recovered(sample(), altered, target())

    def test_probe_rejects_mismatched_pretend_staging_instance(self):
        wrong = {**sample(), "instance_id": "not-this-instance"}
        with patch.object(live, "ssh", return_value=wrong) as ssh:
            with self.assertRaises(live.UnsafeStagingTarget):
                live.observe(target())
            self.assertEqual(ssh.call_count, 1)

    def test_probe_uses_safe_ssh_arguments(self):
        with patch.object(live.subprocess, "run") as run:
            run.return_value.returncode = 0
            run.return_value.stdout = json.dumps(sample())
            result = live.observe(target())
            self.assertEqual(result["instance_id"], INSTANCE)
            call = run.call_args
            argv = call.args[0]
            self.assertEqual(argv[0], "ssh")
            self.assertIn("StrictHostKeyChecking=yes", argv)
            self.assertIn("BatchMode=yes", argv)
            self.assertNotIn("root@production", argv)


class TestWasabiRestoreIsolation(unittest.TestCase):
    source = "stagingwasabi:world-staging/recovery-staging/recovery-test-paper/backup-1"

    def test_rejects_nonstaging_prefixes_without_ssh(self):
        for unsafe in [
            "wasabi:production/server", "stagingwasabi:world-staging/server",
            "stagingwasabi:world-staging/recovery-staging/another-server/backup-1",
            "stagingwasabi:world-staging/recovery-staging/recovery-test-paper/../prod",
            "--config=secret",
        ]:
            with self.subTest(source=unsafe):
                with patch.object(live, "observe") as observed:
                    with self.assertRaises(live.UnsafeStagingTarget):
                        live.restore(target(), unsafe, "a" * 64)
                    observed.assert_not_called()

    def test_restore_requires_exact_expected_marker_hash(self):
        with patch.object(live, "observe") as observed:
            with self.assertRaises(live.UnsafeStagingTarget):
                live.restore(target(), self.source, "invalid-hash")
            observed.assert_not_called()

    def test_restoration_requires_fresh_identity_verification(self):
        with patch.object(live, "observe", side_effect=live.UnsafeStagingTarget("identity")):
            with patch.object(live, "ssh") as ssh:
                with self.assertRaises(live.UnsafeStagingTarget):
                    live.restore(target(), self.source, "f" * 64)
                ssh.assert_not_called()

    def test_remotely_reported_wrong_object_is_rejected(self):
        with patch.object(live, "observe", return_value=sample()):
            with patch.object(live, "ssh", return_value={"result": "verified_restore_files",
                        "instance_id": INSTANCE, "source": "other", "marker_sha256": "f" * 64}):
                with self.assertRaises(live.UnsafeStagingTarget):
                    live.restore(target(), self.source, "f" * 64)


if __name__ == "__main__":
    unittest.main()
