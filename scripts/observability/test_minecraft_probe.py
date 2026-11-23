"""Read-only Paper probe tests; no production service or SSH connection."""
import sys
from pathlib import Path
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "bootstrap"))
import minecraft_probe

INVOCATION = "a" * 32
FIELDS = {
    "ActiveState": "active",
    "InvocationID": INVOCATION,
    "ActiveEnterTimestampMonotonic": "100000000",
}


class MinecraftProbeTests(unittest.TestCase):
    def test_ready_only_in_current_invocation(self):
        self.assertEqual(minecraft_probe.classify(
            FIELDS, ["[11:00:00 INFO]: Done (8.1s)! For help, type help"], 200),
            ("healthy", "paper_ready"))

    def test_known_paper_console_formats(self):
        for line in (
            "Done (8.1s)!",
            "[11:00:00 INFO]: Done (8.1s)!",
            "[11:00:00] [Server thread/INFO]: Done (8.1s)!",
        ):
            with self.subTest(line=line):
                self.assertEqual(minecraft_probe.classify(FIELDS, [line], 200),
                                 ("healthy", "paper_ready"))

    def test_fatal_after_ready(self):
        self.assertEqual(minecraft_probe.classify(
            FIELDS, ["Done (8.1s)!", "[11:00:01 ERROR]: Exception in server tick loop"], 200),
            ("unhealthy", "fatal_current_invocation"))

    def test_fatal_before_later_ready_not_failure(self):
        self.assertEqual(minecraft_probe.classify(
            FIELDS, ["Failed to start the minecraft server", "Done (8.1s)!"], 200),
            ("healthy", "paper_ready"))

    def test_chat_lines_cannot_spoof_fatal_or_ready(self):
        self.assertEqual(minecraft_probe.classify(
            FIELDS, ["Done (8.1s)!", "[11:00:01 INFO]: <Player> Exception in server tick loop"], 200),
            ("healthy", "paper_ready"))
        self.assertEqual(minecraft_probe.classify(
            FIELDS, ["[11:00:01 INFO]: <Player> Done (8.1s)!"], 200),
            ("starting", "awaiting_paper_ready"))

    def test_unknown_is_not_unhealthy(self):
        self.assertEqual(minecraft_probe.classify(FIELDS, None, 200),
                         ("unknown", "journal_unavailable"))
        self.assertEqual(minecraft_probe.classify(
            dict(FIELDS, InvocationID=""), [], 200),
            ("unknown", "invocation_unavailable"))

    def test_initializing_is_not_unhealthy(self):
        self.assertEqual(minecraft_probe.classify(FIELDS, [], 200),
                         ("starting", "awaiting_paper_ready"))

    def test_intentionally_stopped_is_not_failure(self):
        self.assertEqual(minecraft_probe.classify(
            dict(FIELDS, ActiveState="inactive"), None, 200),
            ("stopped", "service_inactive"))

    def test_failed_unit_is_unhealthy(self):
        self.assertEqual(minecraft_probe.classify(
            dict(FIELDS, ActiveState="failed"), None, 200),
            ("unhealthy", "service_failed"))

    def test_journal_query_is_bound_to_exact_invocation(self):
        expected = "_SYSTEMD_INVOCATION_ID=" + INVOCATION
        with mock.patch.object(minecraft_probe, "command", side_effect=[
            "ActiveState=active\nInvocationID=" + INVOCATION + "\nActiveEnterTimestampMonotonic=100000000\n",
            "Done (8.1s)!\n",
        ]) as command, mock.patch("builtins.open", mock.mock_open(read_data="200.0 10.0")):
            reading = minecraft_probe.inspect()
        self.assertEqual(reading["status"], "healthy")
        self.assertIn(expected, command.call_args_list[1].args)


if __name__ == "__main__":
    unittest.main()
