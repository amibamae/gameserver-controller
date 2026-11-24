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
    def test_unchanged_node_never_patches(self):
        desired = {"label": "GSC / vanilla", "state": "active"}
        with patch.object(lifecycle.api, "http_json") as http:
            action = lifecycle.reconcile_node("https://homelable.example", desired, "jwt",
                                              {"status": "online"})
        self.assertEqual(action, "unchanged: GSC / vanilla")
        http.assert_not_called()

    def test_create_response_requires_node_identity(self):
        desired = {"label": "GSC / vanilla", "state": "active", "type": "vm",
                   "design_id": "canvas", "notes": lifecycle.OWNERSHIP_PREFIX + OP}
        for response in (None, {}, {"id": 123}):
            with self.subTest(response=response), patch.object(lifecycle.api, "http_json", return_value=response):
                with self.assertRaises(ValueError) as caught:
                    lifecycle.reconcile_node("https://homelable.example", desired, "jwt", None)
                self.assertEqual(str(caught.exception), "Homelable did not return the node ID")

    def test_snapshot_validation_errors(self):
        valid = {"design_id": "canvas", "nodes": [node()]}
        cases = [([], "invalid lifecycle snapshot"),
                 (dict(valid, design_id="bad/id"), "invalid design ID"),
                 (dict(valid, nodes={}), "invalid lifecycle inventory size"),
                 (dict(valid, nodes=[{}]), "invalid managed node fields"),
                 (dict(valid, nodes=[dict(node(), operation_id="bad")]),
                  "duplicate or invalid operation identity"),
                 (dict(valid, nodes=[node(), node()]), "duplicate or invalid operation identity")]
        for name in (None, "", "x" * 81, "bad\nname"):
            cases.append((dict(valid, nodes=[dict(node(), name=name)]), "invalid lifecycle node name"))
        for value, error in cases:
            with self.subTest(value=value), tempfile.TemporaryDirectory() as tmp:
                path = Path(tmp) / "snapshot.json"
                path.write_text(json.dumps(value))
                with self.assertRaises(ValueError) as caught:
                    lifecycle.read_snapshot(path)
                self.assertEqual(str(caught.exception), error)

    def test_existing_index_validation_errors(self):
        for existing, error in (({}, "Homelable node list is invalid"),
                                ([None], "invalid existing Homelable node")):
            with self.subTest(existing=existing), self.assertRaises(ValueError) as caught:
                lifecycle.preflight(existing, [])
            self.assertEqual(str(caught.exception), error)

    def test_duplicate_desired_labels_block_all_writes(self):
        first = {"label": "GSC / same", "notes": lifecycle.OWNERSHIP_PREFIX + OP,
                 "type": "vm", "design_id": "canvas", "state": "active"}
        second = dict(first, notes=lifecycle.OWNERSHIP_PREFIX + "b" * 32)
        with patch.object(lifecycle.api, "http_json", return_value=[]) as http:
            with self.assertRaisesRegex(ValueError, "^duplicate desired node label$"):
                lifecycle.reconcile("https://homelable.example", "canvas", [first, second], "jwt", True)
        self.assertEqual(http.call_count, 1)

    def test_unhashable_state_is_rejected_with_controlled_error(self):
        invalid = dict(node(), state=[])
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "snapshot.json"
            path.write_text(json.dumps({"design_id": "canvas", "nodes": [invalid]}))
            with self.assertRaisesRegex(ValueError, "^unsupported node type or lifecycle state$"):
                lifecycle.read_snapshot(path)

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
