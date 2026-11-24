"""Offline fault-injection coverage for the optional durable controller collector."""
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import MagicMock, Mock, call, patch

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
    def test_first_collection_reads_retained_history_without_a_rolling_window(self):
        process = Mock(stdout=io.BytesIO(json.dumps(event_record()).encode() + b"\n"))
        process.poll.return_value = 0
        process.wait.return_value = 0
        with patch.object(collector.subprocess, "Popen", return_value=process) as start:
            events, cursor = collector.read_journal("gameserver-controller.service", "")
        self.assertIn("--since=@0", start.call_args.args[0])
        self.assertEqual(len(events), 1)
        self.assertEqual(cursor, "s=1;i=1")

    def test_journal_reader_resumes_from_saved_cursor(self):
        process = Mock(stdout=io.BytesIO())
        process.poll.return_value = 0
        process.wait.return_value = 0
        with patch.object(collector.subprocess, "Popen", return_value=process) as start:
            self.assertEqual(collector.read_journal("gameserver-controller.service", "s=1;i=1"),
                             ([], None))
        self.assertIn("--after-cursor=s=1;i=1", start.call_args.args[0])
        self.assertFalse(any(arg.startswith("--since=") for arg in start.call_args.args[0]))

    def test_journal_batch_is_bounded_and_resumes_at_last_processed_record(self):
        records = [json.dumps(event_record(f"s=1;i={index}")).encode() + b"\n"
                   for index in range(collector.MAX_RECORDS + 1)]
        process = Mock(stdout=io.BytesIO(b"".join(records)))
        process.poll.return_value = None
        process.wait.return_value = -15
        with patch.object(collector.subprocess, "Popen", return_value=process):
            events, cursor = collector.read_journal("gameserver-controller.service", "")
        self.assertEqual(len(events), collector.MAX_RECORDS)
        self.assertEqual(cursor, f"s=1;i={collector.MAX_RECORDS - 1}")
        self.assertEqual(process.stdout.readline(), records[-1])
        process.terminate.assert_called_once_with()

    def test_journal_failure_does_not_deliver_or_advance_saved_cursor(self):
        process = Mock(stdout=io.BytesIO(json.dumps(event_record()).encode() + b"\n"))
        process.poll.return_value = 1
        process.wait.return_value = 1
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "cursor"
            path.write_text("s=1;i=0\n", encoding="utf-8")
            with patch.object(collector.subprocess, "Popen", return_value=process):
                with self.assertRaises(ValueError) as failure:
                    collector.collect_once(collector.Destination("https://logs.example", "private"), path,
                                           "gameserver-controller.service", collector.read_journal,
                                           lambda *_: self.fail("network must not be called"))
            self.assertEqual(str(failure.exception), "journal reader failed; cursor retained")
            self.assertEqual(path.read_text(encoding="utf-8"), "s=1;i=0\n")

    def test_oversized_journal_record_is_drained_before_reading_next_record(self):
        oversized = b"x" * (collector.MAX_LINE + 10) + b"\n"
        valid = json.dumps(event_record("s=1;i=2")).encode() + b"\n"
        process = Mock(stdout=io.BytesIO(oversized + valid))
        process.poll.return_value = 0
        process.wait.return_value = 0
        with patch.object(collector.subprocess, "Popen", return_value=process):
            events, cursor = collector.read_journal("gameserver-controller.service", "")
        self.assertEqual(len(events), 1)
        self.assertEqual(cursor, "s=1;i=2")

    def test_journal_read_error_still_terminates_and_waits_for_reader(self):
        process = Mock()
        process.stdout.readline.side_effect = OSError("journal pipe unavailable")
        process.poll.return_value = None
        process.wait.return_value = -15
        with patch.object(collector.subprocess, "Popen", return_value=process):
            with self.assertRaises(OSError) as failure:
                collector.read_journal("gameserver-controller.service", "")
        self.assertEqual(str(failure.exception), "journal pipe unavailable")
        process.terminate.assert_called_once_with()
        process.wait.assert_called_once_with(timeout=5)

    def test_journal_reader_preserves_filtered_tail_cursor_and_decode_output(self):
        records = [event_record(), event_record("s=1;i=2", "player.private"),
                   {"__CURSOR": "s=1;i=4", "MESSAGE": "private raw log"},
                   {"__CURSOR": "bad cursor", "MESSAGE": "invalid cursor"}]
        lines = [json.dumps(record).encode() + b"\n" for record in records]
        lines.insert(1, b"not json\n")
        process = Mock(stdout=io.BytesIO(b"".join(lines)))
        process.poll.return_value = 0
        process.wait.return_value = 0
        expected = collector.decode_records(lines)
        with patch.object(collector.subprocess, "Popen", return_value=process):
            self.assertEqual(collector.read_journal("gameserver-controller.service", ""), expected)
        self.assertEqual(len(expected[0]), 1)
        self.assertEqual(expected[1], "s=1;i=4")

    def test_failed_cursor_replace_keeps_previous_position_for_replay(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "cursor"
            path.write_text("s=1;i=0\n", encoding="utf-8")
            reader = Mock(return_value=([{"event_type": "safe"}], "s=1;i=1"))
            sender = Mock()
            with patch.object(collector.os, "replace", side_effect=OSError("disk unavailable")):
                with self.assertRaises(OSError) as failure:
                    collector.collect_once(collector.Destination("https://logs.example", "private"), path,
                                           "gameserver-controller.service", reader, sender)
            self.assertEqual(str(failure.exception), "disk unavailable")
            reader.assert_called_once_with("gameserver-controller.service", "s=1;i=0")
            sender.assert_called_once()
            self.assertEqual(collector.read_cursor(path), "s=1;i=0")
            self.assertEqual(list(path.parent.iterdir()), [path])

    def test_journal_timeout_kills_reader_and_does_not_return_partial_batch(self):
        process = Mock(stdout=io.BytesIO())
        process.poll.return_value = None
        process.wait.side_effect = [collector.subprocess.TimeoutExpired("journalctl", 5), 0]
        with patch.object(collector.subprocess, "Popen", return_value=process):
            with self.assertRaises(ValueError) as failure:
                collector.read_journal("gameserver-controller.service", "s=1;i=0")
        self.assertEqual(str(failure.exception), "journal reader timed out")
        process.terminate.assert_called_once_with()
        process.kill.assert_called_once_with()

    def test_cursor_persistence_syncs_file_before_replace_and_directory_after(self):
        output = MagicMock()
        output.__enter__.return_value = output
        output.fileno.return_value = 101
        operations = Mock()
        operations.attach_mock(output.write, "write")
        operations.attach_mock(output.flush, "flush")
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "cursor"
            with (patch.object(collector.tempfile, "mkstemp", return_value=(101, "pending")),
                  patch.object(collector.os, "fdopen", return_value=output),
                  patch.object(collector.os, "O_DIRECTORY", 0x10000, create=True),
                  patch.object(collector.os, "fsync") as sync,
                  patch.object(collector.os, "replace") as replace,
                  patch.object(collector.os, "open", return_value=102) as open_directory,
                  patch.object(collector.os, "close") as close_directory,
                  patch.object(collector.os.path, "exists", return_value=False)):
                operations.attach_mock(sync, "fsync")
                operations.attach_mock(replace, "replace")
                operations.attach_mock(open_directory, "open")
                operations.attach_mock(close_directory, "close")
                collector.save_cursor(path, "s=1;i=1")
            self.assertEqual(operations.mock_calls, [
                call.write("s=1;i=1\n"), call.flush(), call.fsync(101),
                call.replace("pending", path),
                call.open(path.parent, collector.os.O_RDONLY | 0x10000),
                call.fsync(102), call.close(102),
            ])

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
                collector.collect_once(collector.Destination("https://logs.example", "private"), path,
                                       "gameserver-controller.service", reader, fail)
            self.assertFalse(path.exists())
            self.assertEqual(collector.collect_once(
                collector.Destination("https://logs.example", "private"), path, "gameserver-controller.service",
                reader, lambda *_: None), 1)
            self.assertEqual(path.read_text().strip(), "s=1;i=3")
            self.assertEqual([x[1] for x in calls], ["", "s=1;i=3"][:1] + [""])

    def test_success_without_allowed_events_advances_cursor(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "cursor"
            collector.collect_once(collector.Destination("https://logs.example", "private"), path, "gameserver-controller.service",
                                   lambda unit, cursor: ([], "s=1;i=4"), lambda *_: None)
            self.assertEqual(path.read_text().strip(), "s=1;i=4")

    def test_invalid_cursor_requires_manual_recovery(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "cursor"
            path.write_text("malicious cursor with spaces", encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "invalid saved journal cursor"):
                collector.collect_once(collector.Destination("https://logs.example", "private"), path, "gameserver-controller.service",
                                       lambda *_: self.fail("journal must not be read"),
                                       lambda *_: self.fail("network must not be called"))

    def test_malformed_journal_entries_never_export(self):
        lines = [b"not json", b'{"__CURSOR":"bad cursor","MESSAGE":"hello"}',
                 json.dumps(event_record(event_type="player.private")).encode()]
        events, cursor = collector.decode_records(lines)
        self.assertEqual(events, [])
        self.assertEqual(cursor, "s=1;i=1")

    def test_invalid_event_fields_are_dropped_without_blocking_later_records(self):
        for field in ("event_type", "severity"):
            for value in ([], {}, None, 7, True, "not-allowlisted"):
                with self.subTest(field=field, value=value):
                    malformed = event_record()
                    message = json.loads(malformed["MESSAGE"])
                    message[field] = value
                    malformed["MESSAGE"] = json.dumps(message)
                    self.assertEqual(collector.decode_records([json.dumps(malformed).encode()]),
                                     ([], "s=1;i=1"))
                    events, cursor = collector.decode_records([
                        json.dumps(malformed).encode(),
                        json.dumps(event_record("s=1;i=2")).encode(),
                    ])
                    self.assertEqual(len(events), 1)
                    self.assertEqual(events[0]["event_type"], "integration.proxy.recovered")
                    self.assertEqual(cursor, "s=1;i=2")


if __name__ == "__main__":
    unittest.main()
