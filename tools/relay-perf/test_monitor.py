"""仅使用内存模拟 /proc，不在本机发起网络测试。"""
import unittest
from unittest.mock import patch

import monitor


class MonitorTest(unittest.TestCase):
    def test_stat_with_spaces_and_parentheses(self):
        values = ["S"] + ["0"] * 49
        values[11], values[12], values[17], values[19], values[21] = "3", "5", "7", "101", "11"
        with patch.object(monitor.Path, "read_text", return_value="42 (name with ) brackets) " + " ".join(values)), patch.object(monitor.os, "sysconf", return_value=4096, create=True):
            self.assertEqual(monitor.process_sample(42), {"start_ticks": 101, "user_ticks": 3,
                             "system_ticks": 5, "rss_bytes": 45056, "threads": 7})

    def test_host_ticks_exclude_duplicate_guest(self):
        data = {"/proc/stat": "cpu 1 2 3 4 5 6 7 8 9 10\n",
                "/proc/meminfo": "MemAvailable: 100 kB\nSwapFree: 200 kB\n",
                "/proc/vmstat": "pswpin 2\npswpout 3\n"}
        with patch.object(monitor.Path, "read_text", lambda path, **kwargs: data[path.as_posix()]):
            sample = monitor.host_sample()
        self.assertEqual(sample["cpu_ticks"], 36)
        self.assertEqual(sample["idle_ticks"], 9)
        self.assertEqual(sample["steal_ticks"], 8)
        self.assertEqual(sample["available_bytes"], 102400)
        self.assertEqual(sample["swap_out_pages"], 3)


if __name__ == "__main__":
    unittest.main()
