"""模拟规则操作，不执行系统防火墙命令。"""
from contextlib import nullcontext
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import firewall


class FirewallOwnershipTest(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.base = Path(directory.name)
        patcher = patch.object(firewall, "root", return_value=self.base)
        patcher.start()
        self.addCleanup(patcher.stop)
        if os.name == "nt":
            patcher = patch.object(firewall, "ownership_lock", side_effect=nullcontext)
            patcher.start()
            self.addCleanup(patcher.stop)

    def write_owner(self, generation):
        (self.base / "firewall-owned").write_text(
            json.dumps({"role": "front", "generation": generation}), encoding="utf-8")

    def test_old_watcher_does_not_remove_next_round(self):
        previous, current = "a" * 32, "b" * 32
        self.write_owner(previous)
        clock = [0]

        def next_round(seconds):
            (self.base / "firewall-owned").unlink()
            self.write_owner(current)
            clock[0] = 2

        with patch.object(firewall.time, "monotonic", side_effect=lambda: clock[0]), \
                patch.object(firewall.time, "sleep", side_effect=next_round), \
                patch.object(firewall.subprocess, "run") as command:
            firewall.watch("front", previous, 1)
        self.assertEqual(firewall.owner("front"), current)
        command.assert_not_called()

    def test_deadline_removes_only_matching_generation(self):
        generation = "c" * 32
        self.write_owner(generation)
        with patch.object(firewall, "exists", return_value=True), \
                patch.object(firewall.subprocess, "run") as command:
            self.assertTrue(firewall.remove("front", generation))
            self.assertFalse(firewall.remove("front", generation))
        self.assertFalse((self.base / "firewall-owned").exists())
        self.assertEqual(command.call_count, 1)
        self.assertIn(firewall.COMMENT + "-" + generation, command.call_args.args[0])

    def test_watcher_spawn_failure_rolls_back_current_rule(self):
        with patch.object(firewall, "exists", side_effect=[False, True]), \
                patch.object(firewall.subprocess, "run") as command, \
                patch.object(firewall.subprocess, "Popen", side_effect=OSError("spawn failed")):
            with self.assertRaisesRegex(OSError, "spawn failed"):
                firewall.add("front", 1)
        self.assertFalse((self.base / "firewall-owned").exists())
        self.assertEqual(command.call_count, 2)
        self.assertIn("-I", command.call_args_list[0].args[0])
        self.assertIn("-D", command.call_args_list[1].args[0])

    def test_rule_comments_are_distinct_between_rounds(self):
        self.assertNotEqual(firewall.rule("front", "a" * 32), firewall.rule("front", "b" * 32))


if __name__ == "__main__":
    unittest.main()
