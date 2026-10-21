#!/usr/bin/env python3
"""服务器端隔离进程管理，不连接服务器，不改系统配置。"""
import argparse
import json
import os
from pathlib import Path
import signal
import subprocess
import time

from monitor import host_sample, process_sample

ROOT = Path("/tmp/yz-relay-perf-20261020")
MODES = ("new", "buffered", "realm", "old-ss", "old-vless")
PORTS = set(range(28101, 28109))
MAX_SECONDS = 30 * 60


def root():
    if ROOT.is_symlink() or ROOT.resolve() != ROOT or not ROOT.is_dir():
        raise RuntimeError("隔离目录不存在或不是预定目录")
    return ROOT


def ports_clear():
    for table in ("tcp", "tcp6"):
        path = Path(f"/proc/net/{table}")
        if table == "tcp6" and not path.exists():
            continue
        for line in path.read_text(encoding="ascii").splitlines()[1:]:
            values = line.split()
            if values[3] == "0A" and int(values[1].split(":")[1], 16) in PORTS:
                raise RuntimeError("候选测试端口被占用，拒绝启动")


def budget(amount=0, label=None):
    path = root() / "budget.json"
    state = json.loads(path.read_text(encoding="utf-8")) if path.exists() else {"started": time.time(), "bytes": 0, "labels": []}
    if time.time() - state["started"] > MAX_SECONDS:
        raise RuntimeError("已到总时间上限，停止测试")
    if label:
        if label in state["labels"]:
            raise RuntimeError("同一轮已经执行，拒绝重复消耗流量")
        state["labels"].append(label)
    state["bytes"] += amount
    with path.open("w", encoding="utf-8") as output:
        json.dump(state, output)
    return MAX_SECONDS - (time.time() - state["started"])


def identity(pid, exe):
    actual = Path(f"/proc/{pid}/exe").resolve(strict=True)
    if actual != exe.resolve(strict=True):
        raise RuntimeError("PID 不属于本次测试程序")
    return process_sample(pid)["start_ticks"]


def stop_one(path):
    state = json.loads(path.read_text(encoding="utf-8"))
    pid, exe = state["pid"], Path(state["exe"])
    try:
        if identity(pid, exe) != state["start_ticks"]:
            raise RuntimeError("PID 已复用，拒绝停止")
    except FileNotFoundError:
        path.unlink()
        return
    os.kill(pid, signal.SIGTERM)
    for _ in range(100):
        time.sleep(.1)
        try:
            if identity(pid, exe) != state["start_ticks"]:
                raise RuntimeError("PID 已复用")
        except FileNotFoundError:
            path.unlink()
            return
    # 超时后仍验证同一个 PID，再停止本次测试进程。
    if identity(pid, exe) == state["start_ticks"]:
        os.kill(pid, signal.SIGKILL)
    path.unlink()


def start(role, mode, label):
    base = root()
    remaining = budget()
    ports_clear()
    state_path = base / "service.pid.json"
    if state_path.exists():
        raise RuntimeError("已有本次测试进程，先按清单停止")
    instance = base / ("instance-" + label)
    instance.mkdir(mode=0o700)
    exe = base / ("realm" if role == "front" and mode == "realm" else "yz-relay-perf")
    command = [str(exe), "-c", str(base / "realm.toml")] if exe.name == "realm" else [str(exe), role, "--config", str(base / (role + ".json")), "--dir", str(instance), "--mode", mode]
    # 独立会话、静默私有日志；只有 ready 和非敏感摘要才输出。
    with (instance / "output.jsonl").open("x", encoding="utf-8") as output, (instance / "private-errors.log").open("x", encoding="utf-8") as error:
        process = subprocess.Popen(command, stdin=subprocess.DEVNULL, stdout=output, stderr=error,
                                   start_new_session=True, cwd=base, env={**os.environ, "GOMAXPROCS": "1", "GOMEMLIMIT": "192MiB"})
    state = {"pid": process.pid, "exe": str(exe), "start_ticks": identity(process.pid, exe), "instance": str(instance)}
    with state_path.open("x", encoding="utf-8") as output:
        json.dump(state, output)
    # 到总时长时，仅按已验证身份停止本次子进程，避免控制连接中断后遗留监听。
    subprocess.Popen(["python3", str(base / "run.py"), "watch", "--label", label, "--pid", str(process.pid),
                      "--ticks", str(state["start_ticks"]), "--exe", str(exe), "--seconds", str(remaining)],
                     stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
    for _ in range(100):
        if process.poll() is not None:
            state_path.unlink()
            raise RuntimeError("测试服务启动失败；私有错误日志留在隔离目录，不输出配置")
        if exe.name == "realm":
            ready = any(line.split()[3] == "0A" and int(line.split()[1].split(":")[1], 16) == 28101
                        for line in Path("/proc/net/tcp").read_text(encoding="ascii").splitlines()[1:])
        else:
            ready = '"event":"ready"' in (instance / "output.jsonl").read_text(encoding="utf-8")
        if ready:
            sample_path = base / (label + ".monitor.jsonl")
            monitor = subprocess.Popen(["python3", str(base / "monitor.py"), "--pid", str(process.pid),
                                        "--exe", str(exe), "--output", str(sample_path), "--seconds", "600"],
                                       stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                       start_new_session=True)
            monitor_exe = Path(f"/proc/{monitor.pid}/exe").resolve(strict=True)
            with (base / "monitor.pid.json").open("x", encoding="utf-8") as output:
                json.dump({"pid": monitor.pid, "exe": str(monitor_exe), "start_ticks": identity(monitor.pid, monitor_exe)}, output)
            print(json.dumps({"event": "ready", "role": role, "mode": mode, "pid": process.pid, "exe": str(exe)}))
            return
        time.sleep(.1)
    stop_one(state_path)
    raise RuntimeError("测试服务未按时就绪")


def client(mode, label, direction, amount, concurrency, requests):
    base = root()
    state_path = base / "client.pid.json"
    if state_path.exists():
        raise RuntimeError("已有本次测试客户端，拒绝启动第二轮")
    if not 1 <= amount <= 512 << 20 or not 1 <= concurrency <= 32 or not concurrency <= requests <= 4096 or amount < requests:
        raise RuntimeError("本轮测试参数超出范围")
    remaining = budget(amount, label)
    exe = base / "yz-relay-perf"
    host_before = host_sample()
    started = time.time()
    with (base / (label + ".jsonl")).open("x", encoding="utf-8") as output, (base / (label + ".private-errors.log")).open("x", encoding="utf-8") as error:
        process = subprocess.Popen([str(exe), "client", "--config", str(base / "client.json"), "--mode", mode,
                                    "--direction", direction, "--bytes", str(amount), "--concurrency", str(concurrency),
                                    "--requests", str(requests)], stdout=output, stderr=error, stdin=subprocess.DEVNULL,
                                   cwd=base, env={**os.environ, "GOMAXPROCS": "1", "GOMEMLIMIT": "192MiB"})
        with state_path.open("x", encoding="utf-8") as state_file:
            json.dump({"pid": process.pid, "exe": str(exe), "start_ticks": identity(process.pid, exe)}, state_file)
        try:
            deadline = time.monotonic() + min(130, remaining)
            while True:
                waited, status, usage = os.wait4(process.pid, os.WNOHANG)
                if waited:
                    code = process.returncode = os.waitstatus_to_exitcode(status)
                    break
                if time.monotonic() >= deadline:
                    raise subprocess.TimeoutExpired(process.args, 130)
                time.sleep(.01)
        except subprocess.TimeoutExpired:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
            raise RuntimeError("本轮超时，测试客户端已停止")
        finally:
            state_path.unlink(missing_ok=True)
    host_after = host_sample()
    record = {"event": "client_process", "started_unix": started, "finished_unix": time.time(), "exit_code": code,
              "user_seconds": usage.ru_utime, "system_seconds": usage.ru_stime,
              "max_rss_bytes": usage.ru_maxrss * 1024, "rss_method": "per-process wait4 maximum",
              "host_before": host_before, "host_after": host_after}
    with (base / (label + ".process.json")).open("x", encoding="utf-8") as output:
        json.dump(record, output)
    results = [json.loads(line) for line in (base / (label + ".jsonl")).read_text(encoding="utf-8").splitlines()]
    for result in results:
        if result.get("event") == "result":
            print(json.dumps(result))
    print(json.dumps(record))
    if code:
        raise RuntimeError("本轮失败，不能计入成功吞吐")


if __name__ == "__main__":
    os.umask(0o077)
    parser = argparse.ArgumentParser()
    parser.add_argument("action", choices=("start", "stop", "client", "watch"))
    parser.add_argument("--role", choices=("front", "landing"))
    parser.add_argument("--mode", choices=MODES, default="new")
    parser.add_argument("--label", required=True)
    parser.add_argument("--direction", choices=("upload", "download"), default="download")
    parser.add_argument("--bytes", type=int, default=1 << 20)
    parser.add_argument("--concurrency", type=int, default=1)
    parser.add_argument("--requests", type=int, default=1)
    parser.add_argument("--pid", type=int)
    parser.add_argument("--ticks", type=int)
    parser.add_argument("--exe")
    parser.add_argument("--seconds", type=float)
    args = parser.parse_args()
    if not args.label.replace("-", "").isalnum() or len(args.label) > 64:
        parser.error("结果标签无效")
    if args.action == "start":
        if not args.role:
            parser.error("必须指定角色")
        start(args.role, args.mode, args.label)
    elif args.action == "stop":
        for name in ("monitor.pid.json", "service.pid.json", "client.pid.json"):
            state = root() / name
            if state.exists():
                stop_one(state)
        ports_clear()
        print(json.dumps({"event": "stopped"}))
    elif args.action == "client":
        client(args.mode, args.label, args.direction, args.bytes, args.concurrency, args.requests)
    else:
        # 一秒检查一次进程身份；服务提前结束就立即退出看护。
        deadline = time.monotonic() + args.seconds
        while time.monotonic() < deadline:
            try:
                if identity(args.pid, Path(args.exe)) != args.ticks:
                    break
            except FileNotFoundError:
                break
            time.sleep(1)
        else:
            try:
                if identity(args.pid, Path(args.exe)) == args.ticks:
                    os.kill(args.pid, signal.SIGTERM)
            except FileNotFoundError:
                pass
