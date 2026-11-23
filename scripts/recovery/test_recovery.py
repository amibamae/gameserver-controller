"""Unit tests for conservative recovery and read-only Paper log classification."""
import sys
from pathlib import Path
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "bootstrap"))
import minecraft_probe
import controller_recover as recovery

ID = "a" * 32
FIELDS = {
    "ActiveState": "active",
    "InvocationID": ID,
    "ActiveEnterTimestampMonotonic": "100000000",
}


class MinecraftProbeTests(unittest.TestCase):
    def test_new_invocation_ready(self):
        self.assertEqual(minecraft_probe.classify(
            FIELDS, ["Starting minecraft server", "Done (8.1s)!"], 200),
            ("healthy", "paper_ready"))

    def test_fatal_after_ready(self):
        self.assertEqual(minecraft_probe.classify(
            FIELDS, ["Done (8.1s)!", "Exception in server tick loop"], 200),
            ("unhealthy", "fatal_current_invocation"))

    def test_fatal_before_latest_ready_does_not_restart(self):
        self.assertEqual(minecraft_probe.classify(
            FIELDS, ["Failed to start the minecraft server", "Done (8.1s)!"], 200),
            ("healthy", "paper_ready"))

    def test_initializing_does_not_restart(self):
        self.assertEqual(minecraft_probe.classify(FIELDS, [], 200),
                         ("starting", "awaiting_paper_ready"))

    def test_unknown_log_is_not_failure(self):
        self.assertEqual(minecraft_probe.classify(FIELDS, None, 200),
                         ("unknown", "journal_unavailable"))

    def test_unavailable_invocation_is_not_failure(self):
        self.assertEqual(minecraft_probe.classify(
            dict(FIELDS, InvocationID=""), [], 200),
            ("unknown", "invocation_unavailable"))

    def test_intentionally_stopped_is_not_failure(self):
        self.assertEqual(minecraft_probe.classify(
            dict(FIELDS, ActiveState="inactive"), None, 200),
            ("stopped", "service_inactive"))

    def test_failed_unit_is_unhealthy(self):
        self.assertEqual(minecraft_probe.classify(
            dict(FIELDS, ActiveState="failed"), None, 200),
            ("unhealthy", "service_failed"))


class RecoveryPolicyTests(unittest.TestCase):
    def test_two_samples_of_same_invocation_required(self):
        state = {}
        bad = {"status": "unhealthy", "reason": "service_failed",
               "invocation_id": ID}
        self.assertFalse(recovery.observe(state, bad, 100))
        self.assertTrue(recovery.observe(state, bad, 110))

    def test_changed_invocation_resets_evidence(self):
        state = {}
        bad = {"status": "unhealthy", "reason": "service_failed",
               "invocation_id": ID}
        recovery.observe(state, bad, 100)
        self.assertFalse(recovery.observe(state, dict(bad, invocation_id="b" * 32), 110))

    def test_stale_sample_resets_evidence(self):
        state = {}
        bad = {"status": "unhealthy", "reason": "service_failed",
               "invocation_id": ID}
        recovery.observe(state, bad, 100)
        self.assertFalse(recovery.observe(state, bad, 500))

    def test_unknown_never_triggers_restart(self):
        state = {}
        bad = {"status": "unhealthy", "reason": "service_failed",
               "invocation_id": ID}
        recovery.observe(state, bad, 100)
        self.assertFalse(recovery.observe(
            state, {"status": "unknown", "reason": "probe_unavailable",
                    "invocation_id": ""}, 105))

    def test_hourly_daily_and_cooldown_budget(self):
        self.assertFalse(recovery.allowed_attempt(
            {"attempts": [500, 510]}, 1000)[0])
        self.assertFalse(recovery.allowed_attempt(
            {"attempts": [1000, 2000, 3000]}, 90000 if False else 3100)[0])
        self.assertFalse(recovery.allowed_attempt(
            {"attempts": [1000]}, 1100)[0])
        self.assertTrue(recovery.allowed_attempt({"attempts": [1000]}, 5000)[0])

    def test_dry_run_makes_no_ssh_or_state(self):
        class Args:
            host = "backend"
            execute = False
            confirm_host = ""
            state_dir = "/not-created"
        with mock.patch.object(recovery, "ssh") as ssh:
            self.assertEqual(recovery.run(Args()), 0)
            ssh.assert_not_called()

    def test_no_restart_on_uncertain_health(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "state.json"
            with mock.patch.object(recovery, "probe", return_value={
                "status": "unknown", "reason": "probe_unavailable",
                "invocation_id": ""}), mock.patch.object(recovery, "ssh") as ssh:
                self.assertEqual(recovery.run_locked("backend", {}, path,
                                                     Path(directory)), 0)
                ssh.assert_not_called()
                self.assertEqual(path.exists(), True)

    def test_restart_only_after_confirmed_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            state, path = {}, Path(directory) / "state.json"
            bad = {"status": "unhealthy", "reason": "service_failed",
                   "invocation_id": ID}
            with mock.patch.object(recovery, "probe", return_value=bad), \
                 mock.patch.object(recovery, "ssh", return_value="") as ssh, \
                 mock.patch.object(recovery, "wait_for_new_ready", return_value=True), \
                 mock.patch.object(recovery.time, "time", return_value=5000):
                self.assertEqual(recovery.run_locked("backend", state, path,
                                                     Path(directory)), 0)
                ssh.assert_not_called()
                self.assertEqual(recovery.run_locked("backend", state, path,
                                                     Path(directory)), 0)
                ssh.assert_called_once_with("backend", recovery.RESTART,
                                            timeout=140)
                self.assertEqual(len(state["attempts"]), 1)


if __name__ == "__main__":
    unittest.main()
