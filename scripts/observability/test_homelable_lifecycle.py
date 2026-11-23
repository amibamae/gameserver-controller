"""Offline staging simulations for managed-only Homelable lifecycle sync."""
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import homelable_lifecycle as lifecycle

OP = "a" * 32


def node(state="active"):
    return {"operation_id": OP, "name": "vanilla @ staging (aaaaaaaa)",
            "type": "vm", "state": state}


class LifecycleTests(unittest.TestCase):
    def test_disabled_dry_run_makes_no_requests(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "snapshot.json"
            path.write_text(json.dumps({"design_id": "canvas", "nodes": [node()]}))
            design, nodes = lifecycle.read_snapshot(path)
        with patch.object(lifecycle.api, "http_json") as http:
            result = lifecycle.reconcile("https://homelable.example", design, nodes, "", False)
        self.assertEqual(len(result), 1)
        self.assertTrue(result[0].startswith("DRY RUN"))
        http.assert_not_called()

    def test_new_active_node_is_created_and_later_marked_retired(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "snapshot.json"
            path.write_text(json.dumps({"design_id": "canvas", "nodes": [node()]}))
            design, nodes = lifecycle.read_snapshot(path)
        with patch.object(lifecycle.api, "http_json",
                          side_effect=[[], {"id": "created-1"}]) as http:
            actions = lifecycle.reconcile("https://homelable.example", design, nodes, "jwt", True)
        self.assertEqual(len(http.call_args_list), 2)
        self.assertEqual(http.call_args_list[1].args[3], "POST")
        self.assertEqual(actions[0].split(":")[0], "created")
        nodes[0]["state"] = "retired"
        existing = [{"id": "created-1", "label": nodes[0]["label"],
                     "notes": nodes[0]["notes"], "status": "online"}]
        with patch.object(lifecycle.api, "http_json",
                          side_effect=[existing, {}]) as http:
            lifecycle.reconcile("https://homelable.example", design, nodes, "jwt", True)
        self.assertEqual(http.call_args_list[1].args[3], "PATCH")
        self.assertEqual(http.call_args_list[1].args[4], {"status": "offline"})

    def test_manual_node_with_same_label_is_never_touched(self):
        nodes = [{"label": "GSC / vanilla", "notes": lifecycle.OWNERSHIP_PREFIX + OP,
                  "type": "vm", "design_id": "canvas", "state": "retired"}]
        existing = [{"id": "manual-1", "label": nodes[0]["label"],
                     "notes": "Created manually", "status": "online"}]
        with patch.object(lifecycle.api, "http_json", return_value=existing) as http:
            with self.assertRaisesRegex(ValueError, "unmanaged or ambiguous"):
                lifecycle.reconcile("https://homelable.example", "canvas", nodes, "jwt", True)
        self.assertEqual(http.call_count, 1)

    def test_deleted_instance_is_not_recreated(self):
        nodes = [{"label": "GSC / retired", "notes": lifecycle.OWNERSHIP_PREFIX + OP,
                  "type": "vm", "design_id": "canvas", "state": "retired"}]
        with patch.object(lifecycle.api, "http_json", return_value=[]) as http:
            actions = lifecycle.reconcile("https://homelable.example", "canvas", nodes, "jwt", True)
        self.assertEqual(http.call_count, 1)
        self.assertEqual(actions, ["retired and absent: GSC / retired"])

    def test_ambiguous_owned_nodes_block_all_writes(self):
        nodes = [{"label": "GSC / vanilla", "notes": lifecycle.OWNERSHIP_PREFIX + OP,
                  "type": "vm", "design_id": "canvas", "state": "active"}]
        existing = [{"id": "one", "label": "GSC / vanilla", "notes": nodes[0]["notes"]},
                    {"id": "two", "label": "GSC / vanilla", "notes": nodes[0]["notes"]}]
        with patch.object(lifecycle.api, "http_json", return_value=existing) as http:
            with self.assertRaises(ValueError):
                lifecycle.reconcile("https://homelable.example", "canvas", nodes, "jwt", True)
        self.assertEqual(http.call_count, 1)

    def test_missing_vm_does_not_infer_retirement(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "snapshot.json"
            path.write_text(json.dumps({"design_id": "canvas", "nodes": []}))
            _, nodes = lifecycle.read_snapshot(path)
        self.assertEqual(nodes, [])


if __name__ == "__main__":
    unittest.main()
