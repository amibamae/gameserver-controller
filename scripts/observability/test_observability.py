"""Offline tests for the optional VictoriaLogs/Homelable integrations."""
import io
import json
from pathlib import Path
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "bootstrap"))
import homelable_sync as homelable
import victorialogs_bridge as bridge
import victorialogs_configure as installer
import victorialogs_export as exporter


class ControllerLogsTests(unittest.TestCase):
    def test_allowlist_removes_raw_fields(self):
        event = {"message": "integration event", "event_type": "integration.proxy.unreachable",
                 "severity": "critical", "event_id": "evt-0123",
                 "environment_id": "production", "adapter_id": "integration.velocity",
                 "occurred_at": "2026-11-23T10:00:00Z",
                 "summary": "contains player info", "token": "private", "player_ip": "10.0.0.1"}
        result = bridge.sanitized_event(json.dumps(event))
        self.assertEqual(len(result), 9)
        self.assertNotIn("token", result)
        self.assertNotIn("player_ip", result)
        self.assertNotIn("summary", result)

    def test_unknown_events_dropped(self):
        for text in ('{not-json', '{"message":"player chat"}',
                     '{"message":"integration event","event_type":"player.login"}'):
            self.assertIsNone(bridge.sanitized_event(text))

    def test_reject_insecure_urls(self):
        for url in ("http://remote.example/insert/jsonline",
                    "https://name:secret@example/insert/jsonline",
                    "https://example/other", "https://example/insert/jsonline?secret=1"):
            with self.assertRaises(ValueError):
                bridge.endpoint_url(url)
        self.assertIn("_stream_fields=", bridge.endpoint_url("https://logs.example/insert/jsonline"))

    def test_batches_are_copied_before_clearing(self):
        item = json.dumps({"message": "integration event", "event_type": "integration.proxy.recovered",
                           "severity": "info", "event_id": "evt-0123",
                           "environment_id": "production", "adapter_id": "integration.velocity",
                           "occurred_at": "2026-11-23T10:00:00Z"})
        with patch.object(bridge, "deliver") as send:
            count = bridge.run(io.StringIO(item + "\n" + item), "https://logs.example/insert/jsonline", "", 1)
        self.assertEqual(count, 2)
        self.assertEqual(len(send.call_args_list[0].args[0]), 1)
        self.assertEqual(len(send.call_args_list[1].args[0]), 1)


class VMExportTests(unittest.TestCase):
    def test_only_lifecycle_categories_leave_the_vm(self):
        self.assertIsNone(exporter.lifecycle_message("minecraft-vanilla.service", "player whispered password"))
        self.assertEqual(exporter.lifecycle_message("minecraft-vanilla.service", "Done (1.4s)!"), "paper_ready")
        self.assertEqual(exporter.lifecycle_message("minecraft-vanilla.service", "WARN something"), "paper_warning")

    def test_ssh_configurer_rejects_raw_and_bad_tokens(self):
        basic = {"endpoint": "https://logs.example/insert/jsonline", "environment_id": "production",
                 "instance_id": "instance-1", "privacy_mode": "lifecycle", "token": "a" * 32}
        self.assertEqual(installer.read_request(io.BytesIO(json.dumps(basic).encode())), basic)
        for bad in ({**basic, "privacy_mode": "raw"},
                    {**basic, "token": "bad\nsecret"},
                    {**basic, "endpoint": "http://evil.example/insert/jsonline"},
                    {**basic, "player_chat": True}):
            with self.assertRaises(ValueError):
                installer.read_request(io.BytesIO(json.dumps(bad).encode()))


class HomelableTests(unittest.TestCase):
    def test_dry_run_never_calls_api(self):
        nodes = [{"label": "GSC / Proxy", "type": "server", "design_id": "canvas"}]
        with patch.object(homelable, "http_json") as http:
            self.assertEqual(homelable.sync("https://local.example", "canvas", nodes, "", False),
                             ["DRY RUN: ensure GSC / Proxy"])
        http.assert_not_called()

    def test_does_not_modify_existing_nodes(self):
        nodes = [{"label": "GSC / Proxy", "type": "server", "design_id": "canvas"},
                 {"label": "GSC / Backend", "type": "vm", "design_id": "canvas"}]
        with patch.object(homelable, "http_json",
                          side_effect=[[{"id": "node-a", "label": "GSC / Proxy"}], {"id": "node-b"}]) as http:
            result = homelable.sync("https://local.example", "canvas", nodes, "jwt", True)
        self.assertEqual(result, ["existing: GSC / Proxy", "created: GSC / Backend"])
        self.assertEqual(http.call_count, 2)
        self.assertEqual(http.call_args_list[1].kwargs["method"], "POST")

    def test_duplicate_managed_nodes_block_sync(self):
        nodes = [{"label": "GSC / Proxy", "type": "server", "design_id": "canvas"}]
        with patch.object(homelable, "http_json",
                          return_value=[{"label": "GSC / Proxy"}, {"label": "GSC / Proxy"}]):
            with self.assertRaises(ValueError):
                homelable.sync("https://local.example", "canvas", nodes, "jwt", True)


if __name__ == "__main__":
    unittest.main()
