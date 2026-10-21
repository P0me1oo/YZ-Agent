"""只用内存数据校验 CPU 时间边界和争用计算。"""
import unittest

from summarize import at, interval


class SummaryTest(unittest.TestCase):
    def test_interpolated_request_boundaries(self):
        samples = [{"event": "start", "clock_ticks": 100}]
        for timestamp, user, system, host, idle, steal in ((0, 0, 0, 100, 20, 0),
                                                          (1, 20, 10, 200, 50, 10),
                                                          (2, 40, 20, 300, 80, 20)):
            samples.append({"event": "sample", "unix_time": timestamp, "user_ticks": user,
                            "system_ticks": system, "cpu_ticks": host, "idle_ticks": idle,
                            "steal_ticks": steal, "rss_bytes": 4096,
                            "swap_in_pages": 0, "swap_out_pages": 0})
        result = interval(samples, .5, 1.5)
        self.assertAlmostEqual(result["cpu_seconds"], .3)
        self.assertAlmostEqual(result["cpu_percent"], 30)
        self.assertAlmostEqual(result["host_busy_percent"], 60)
        self.assertAlmostEqual(result["host_steal_percent"], 10)
        self.assertEqual(result["max_rss_bytes"], 4096)

    def test_missing_coverage_is_rejected(self):
        samples = [{"unix_time": 1, "user_ticks": 0}, {"unix_time": 2, "user_ticks": 1}]
        with self.assertRaises(ValueError):
            at(samples, .5, "user_ticks")


if __name__ == "__main__":
    unittest.main()
