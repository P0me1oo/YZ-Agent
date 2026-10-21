#!/usr/bin/env python3
"""只有单独获准后才执行；只添加/撤销三机之间的本次临时规则。"""
import argparse
from contextlib import contextmanager
import json
import os
from pathlib import Path
import subprocess
import time
import uuid

from run import root

COMMENT = "yz-relay-perf-20261020"


def rule(role, generation):
    comment = COMMENT + "-" + generation
    if role == "front":
        return ["-s", "216.235.243.12/32", "-p", "tcp", "--dport", "28101",
                "-m", "comment", "--comment", comment, "-j", "ACCEPT"]
    return ["-s", "160.236.111.18/32", "-p", "tcp", "-m", "multiport", "--dports", "28102,28103,28104,28105",
            "-m", "comment", "--comment", comment, "-j", "ACCEPT"]


def exists(args):
    return subprocess.run(["iptables", "-w", "5", "-C", "INPUT", *args], stdout=subprocess.DEVNULL,
                          stderr=subprocess.DEVNULL, timeout=10).returncode == 0


@contextmanager
def ownership_lock():
    import fcntl

    descriptor = os.open(root() / "firewall.lock", os.O_RDWR | os.O_CREAT, 0o600)
    with os.fdopen(descriptor, "r+") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        try:
            yield
        finally:
            fcntl.flock(lock, fcntl.LOCK_UN)


def owner(role, generation=None):
    marker = root() / "firewall-owned"
    try:
        state = json.loads(marker.read_text(encoding="utf-8"))
    except FileNotFoundError:
        return None
    if state.get("role") != role:
        raise RuntimeError("临时规则所属角色不符，拒绝接管")
    current = state.get("generation", "")
    if len(current) != 32 or any(c not in "0123456789abcdef" for c in current):
        raise RuntimeError("临时规则归属记录无效")
    return current if generation is None or generation == current else None


def remove_owned(role, generation):
    if owner(role, generation) is None:
        return False
    args = rule(role, generation)
    if exists(args):
        subprocess.run(["iptables", "-w", "5", "-D", "INPUT", *args], check=True, timeout=10)
    (root() / "firewall-owned").unlink()
    return True


def remove(role, generation=None):
    with ownership_lock():
        current = owner(role, generation)
        return remove_owned(role, current) if current is not None else False


def add(role, seconds):
    with ownership_lock():
        marker = root() / "firewall-owned"
        generation = uuid.uuid4().hex
        selected = rule(role, generation)
        if marker.exists() or exists(selected):
            raise RuntimeError("已存在相同临时规则，拒绝重复添加或接管")
        descriptor = os.open(marker, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(descriptor, "w", encoding="utf-8") as output:
            json.dump({"role": role, "generation": generation}, output)
        try:
            subprocess.run(["iptables", "-w", "5", "-I", "INPUT", "1", *selected], check=True, timeout=10)
            subprocess.Popen(["python3", str(Path(__file__).resolve()), "watch", "--role", role,
                              "--seconds", str(seconds), "--generation", generation],
                             stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                             start_new_session=True)
        except Exception:
            # 看护启动失败也须撤销本轮规则；撤销失败时保留归属供人工清理。
            remove_owned(role, generation)
            raise


def watch(role, generation, seconds):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        with ownership_lock():
            if owner(role, generation) is None:
                return
        time.sleep(1)
    remove(role, generation)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("action", choices=("add", "remove", "watch"))
    parser.add_argument("--role", choices=("front", "landing"), required=True)
    parser.add_argument("--seconds", type=int, default=2700)
    parser.add_argument("--generation")
    args = parser.parse_args()
    if not 1 <= args.seconds <= 2700:
        parser.error("临时规则时长超出范围")
    if args.action == "add":
        add(args.role, args.seconds)
        print("temporary_test_rule_added")
    elif args.action == "remove":
        remove(args.role, args.generation)
        print("temporary_test_rule_removed")
    else:
        if not args.generation:
            parser.error("看护必须指定规则所属轮次")
        watch(args.role, args.generation, args.seconds)
