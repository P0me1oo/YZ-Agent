#!/usr/bin/env python3
"""只观察本次明确指定的测试进程，不读取命令行、环境变量或配置内容。"""
import argparse
import json
import os
from pathlib import Path
import signal
import time


def process_sample(pid):
    text = Path(f"/proc/{pid}/stat").read_text(encoding="ascii")
    values = text[text.rfind(")") + 2:].split()
    return {
        "start_ticks": int(values[19]),
        "user_ticks": int(values[11]),
        "system_ticks": int(values[12]),
        "rss_bytes": int(values[21]) * os.sysconf("SC_PAGE_SIZE"),
        "threads": int(values[17]),
    }


def host_sample():
    values = list(map(int, Path("/proc/stat").read_text(encoding="ascii").splitlines()[0].split()[1:]))
    memory = {line.split(":", 1)[0]: int(line.split()[1]) * 1024
              for line in Path("/proc/meminfo").read_text(encoding="ascii").splitlines()
              if line.startswith(("MemAvailable:", "SwapFree:"))}
    vm = dict(line.split() for line in Path("/proc/vmstat").read_text(encoding="ascii").splitlines()
              if line.startswith(("pswpin ", "pswpout ")))
    return {"cpu_ticks": sum(values[:8]), "idle_ticks": values[3] + values[4], "steal_ticks": values[7],
            "available_bytes": memory["MemAvailable"], "swap_free_bytes": memory["SwapFree"],
            "swap_in_pages": int(vm["pswpin"]), "swap_out_pages": int(vm["pswpout"])}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--pid", type=int, required=True)
    parser.add_argument("--exe", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--seconds", type=float, default=120)
    parser.add_argument("--interval", type=float, default=0.2)
    args = parser.parse_args()
    if args.pid <= 1 or not 0.05 <= args.interval <= 5 or not 1 <= args.seconds <= 600:
        parser.error("采样范围无效")
    expected = Path(args.exe).resolve(strict=True)
    actual = Path(f"/proc/{args.pid}/exe").resolve(strict=True)
    if actual != expected:
        parser.error("进程可执行文件与本次测试程序不符")
    initial = process_sample(args.pid)
    stopped = False

    def stop(signum, frame):
        nonlocal stopped
        stopped = True

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    deadline = time.monotonic() + args.seconds
    descriptor = os.open(args.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8") as output:
        output.write(json.dumps({"event": "start", "pid": args.pid, "clock_ticks": os.sysconf("SC_CLK_TCK"), **initial}) + "\n")
        while not stopped and time.monotonic() < deadline:
            try:
                current = process_sample(args.pid)
            except FileNotFoundError:
                break
            if current["start_ticks"] != initial["start_ticks"]:
                raise RuntimeError("测试进程已结束，PID 被复用")
            output.write(json.dumps({"event": "sample", "monotonic": time.monotonic(), "unix_time": time.time(), **current, **host_sample()}) + "\n")
            output.flush()
            time.sleep(args.interval)
        output.write(json.dumps({"event": "finished"}) + "\n")


if __name__ == "__main__":
    main()
