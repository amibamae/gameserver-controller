"""Offline fault-injection coverage for the optional durable controller collector."""
import json
from pathlib import Path
import tempfile
import unittest

import victorialogs_collector as collector


def event_record(cursor="s=1;i=1", event_type="integration.proxy.recovered"):
    return {
        "__CURSOR": cursor,
        "MESSAGE": json.dumps({
            "message": "integration event", "event_type": event_type,
            "severity": "info", "event_id": "evt-01",
            "environment_id": "staging", "adapter_id": "integration.velocity",
            "occurred_at": "2026-11-23T10:00:00Z",
            "token": "secret", "player_ip": "192.0.2.1",
        }),
    }


class CollectorTests(unittest.TestCase):
    def test_journal_filter_only_exports_safe_fields(self):
        lines = [json.dumps(event_record()).encode(),
                 json.dumps({"__CURSOR": "s=1;i=2", "MESSAGE": "player chat: secret"}).encode()]
        events, cursor = collector.decode_records(lines)
        self.assertEqual(cursor, "s=1;i=2")
        self.assertEqual(len(events), 1)
        self.assertNotIn("token", events[0])
        self.assertNotIn("player_ip", events[0])

    def test_failed_ingestion_keeps_cursor_and_replays(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "cursor"
            calls = []
            def reader(unit, cursor):
                calls.append((unit, cursor))
                return ([{"event_type": "safe"}], "s=1;i=3")
            def fail(*args):
                raise OSError("endpoint offline")
            with self.assertRaises(OSError):
                collector.collect_once("https://logs.example", "private", path,
                                       "gameserver-controller.service", reader, fail)
            self.assertFalse(path.exists())
            self.assertEqual(collector.collect_once(
                "https://logs.example", "private", path, "gameserver-controller.service",
                reader, lambda *_: None), 1)
            self.assertEqual(path.read_text().strip(), "s=1;i=3")
            self.assertEqual([x[1] for x in calls], ["", "s=1;i=3"][:1] + [""])

    def test_success_without_allowed_events_advances_cursor(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "cursor"
            collector.collect_once("https://logs.example", "private", path, "gameserver-controller.service",
                                   lambda unit, cursor: ([], "s=1;i=4"), lambda *_: None)
            self.assertEqual(path.read_text().strip(), "s=1;i=4")

    def test_invalid_cursor_requires_manual_recovery(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "cursor"
            path.write_text("malicious cursor with spaces", encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "invalid saved journal cursor"):
                collector.collect_once("https://logs.example", "private", path, "gameserver-controller.service",
                                       lambda *_: self.fail("journal must not be read"),
                                       lambda *_: self.fail("network must not be called"))

    def test_malformed_journal_entries_never_export(self):
        lines = [b"not json", b'{"__CURSOR":"bad cursor","MESSAGE":"hello"}',
                 json.dumps(event_record(event_type="player.private")).encode()]
        events, cursor = collector.decode_records(lines)
        self.assertEqual(events, [])
        self.assertEqual(cursor, "s=1;i=1")


if __name__ == "__main__":
    unittest.main()
