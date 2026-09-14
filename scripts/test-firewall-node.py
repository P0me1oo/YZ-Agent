#!/usr/bin/env python3
"""在 test-firewall-linux.sh 创建的隔离环境内验证真实 Node 和官方 HY2 客户端。"""

import argparse
import hashlib
import http.server
import json
import os
from pathlib import Path
import secrets
import signal
import socket
import struct
import subprocess
import sys
import tempfile
import threading
import time
import urllib.parse
import urllib.request
import uuid

PAYLOAD = b"yzboard-firewall-hopping-test\n" * 256
SCRIPT = str(Path(__file__).resolve())


def require_isolation():
    if Path("/run/yz-firewall-test").read_text() != "isolated-rootfs\n":
        raise RuntimeError("缺少独立 rootfs 标记")
    socket.if_nametoindex("yzfw0")


def wait_for(description, predicate, timeout=45):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(0.2)
    raise AssertionError("等待超时：" + description)


def stop(process, sig=signal.SIGTERM):
    if process is None or process.poll() is not None:
        return
    process.send_signal(sig)
    try:
        process.wait(timeout=20)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=5)
        if sig != signal.SIGKILL:
            raise AssertionError("测试进程未按时退出")


def serve_echo():
    class Echo(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            self.send_response(200)
            self.send_header("Content-Length", str(len(PAYLOAD)))
            self.end_headers()
            self.wfile.write(PAYLOAD)

        def log_message(self, *args):
            pass

    http.server.ThreadingHTTPServer(("0.0.0.0", 28580), Echo).serve_forever()


def probe_http():
    opener = urllib.request.build_opener(
        urllib.request.ProxyHandler({"http": "http://127.0.0.1:18080"})
    )
    with opener.open("http://192.0.2.2:28580/test", timeout=3) as response:
        if response.read() != PAYLOAD:
            raise AssertionError("响应内容不匹配")


class NodeTest:
    def __init__(self, args, work):
        self.args, self.work = args, work
        self.token, self.credential = secrets.token_hex(24), str(uuid.uuid4())
        self.lock = threading.RLock()
        self.enabled = True
        self.traffic_seen = False
        self.reports = 0
        self.config = {
            "protocol": "hysteria", "version": 2, "server_port": 28443,
            "listen_ip": "::", "port_hopping": "29440-29441,29500",
            "kernel_type": args.kernel, "server_name": "node.example.invalid",
            "up_mbps": 100, "down_mbps": 100,
            "cert_config": {"cert_mode": "self", "domain": "node.example.invalid"},
            "base_config": {"pull_interval": 1, "push_interval": 1},
        }
        self.node = self.client = self.echo = None
        self.panel = None
        self.logs = []
        self.node_path = work / "config.json"
        scope = hashlib.sha256(str(self.node_path).encode()).hexdigest()[:12]
        self.state_path = work / "firewall" / (scope + ".json")

    def start(self, command, label, client_namespace=False):
        if client_namespace:
            command = ["ip", "netns", "exec", "yzfw-client"] + command
        log = (self.work / (label + ".log")).open("ab", buffering=0)
        self.logs.append(log)
        return subprocess.Popen(command, stdout=log, stderr=log, cwd=self.work)

    def start_panel(self):
        test = self

        class Panel(http.server.BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                self.dispatch()

            def do_POST(self):
                self.dispatch()

            def dispatch(self):
                path = urllib.parse.urlsplit(self.path).path
                if self.command == "GET":
                    values = urllib.parse.parse_qs(urllib.parse.urlsplit(self.path).query)
                    auth = values.get("token", [None])[0]
                    data = {}
                else:
                    data = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"{}")
                    auth = data.get("token")
                if auth != test.token:
                    self.send_error(403)
                    return
                with test.lock:
                    if path.endswith("/machine/nodes"):
                        nodes = [{"id": 1, "type": "hysteria", "name": "firewall-test", "kernel_type": test.args.kernel}] if test.enabled else []
                        # 机器发现低于 30 秒会回退到 60 秒，使用实际支持的最短周期。
                        response = {"nodes": nodes, "base_config": {"pull_interval": 30, "push_interval": 10}}
                    elif path.endswith("/handshake"):
                        response = {"websocket": {"enabled": False}}
                    elif path.endswith("/config"):
                        response = dict(test.config)
                    elif path.endswith("/user"):
                        response = {"users": [{"id": 1, "uuid": test.credential, "speed_limit": 0, "device_limit": 0}]}
                    elif path.endswith("/alivelist"):
                        response = {"alive": {}}
                    else:
                        response = {"data": True}
                        if path.endswith("/report"):
                            test.reports += 1
                            test.traffic_seen |= any(sum(value) > 0 for value in data.get("traffic", {}).values())
                encoded = json.dumps(response, sort_keys=True).encode()
                etag = '"' + hashlib.sha256(encoded).hexdigest() + '"'
                if self.command == "GET" and self.headers.get("If-None-Match") == etag:
                    self.send_response(304)
                    self.end_headers()
                    return
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(encoded)))
                self.send_header("ETag", etag)
                self.end_headers()
                self.wfile.write(encoded)

        self.panel = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Panel)
        threading.Thread(target=self.panel.serve_forever, daemon=True).start()
        node_config = {
            "panel": {"url": "http://127.0.0.1:" + str(self.panel.server_port)},
            "machine": {"machine_id": 1, "token": self.token},
            "kernel": {"type": self.args.kernel, "config_dir": str(self.work / "kernel"), "log_level": "debug"},
            "cert": {"cert_dir": str(self.work / "cert")},
            "node": {"pull_interval": 1, "push_interval": 1, "track_interval": 1},
            "time_sync": {"enabled": False}, "log": {"level": "info"},
            "firewall": {"backend": "auto", "redirect_backend": self.args.redirect, "state_dir": str(self.work / "firewall")},
        }
        if self.args.kernel == "xray":
            # Xray 默认拒绝保留地址，仅为隔离环境里的 HTTP 对照服务开放此测试出站。
            node_config["kernel"]["custom_outbound"] = [{
                "tag": "direct", "protocol": "freedom",
                "settings": {"finalRules": [{
                    "action": "allow", "network": "tcp", "ip": ["192.0.2.2/32"], "port": 28580,
                }]},
            }]
        self.node_path.write_text(json.dumps(node_config))

    def start_node(self):
        self.node = self.start(["/work/yz-agent-linux-amd64", "-c", str(self.node_path)], "node")

    def state(self):
        if not self.state_path.exists():
            return {}
        return json.loads(self.state_path.read_text())

    def running(self, first_port):
        state = self.state()
        return self.node.poll() is None and state.get("redirect_backend") == self.args.redirect and any(
            rule["rule"]["ports"]["from"] == first_port for rule in state.get("owned", [])
        )

    def cleared(self):
        state = self.state()
        return state.get("version") == 1 and not state.get("owned") and not state.get("redirect_backend")

    def probe(self):
        return subprocess.run(
            ["ip", "netns", "exec", "yzfw-client", "python3", SCRIPT, "--probe"],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=5,
        ).returncode == 0

    def start_client(self, ports, ipv6=False):
        stop(self.client)
        client_path = self.work / "client.json"
        address = "[2001:db8:1::1]" if ipv6 else "192.0.2.1"
        client_path.write_text(json.dumps({
            "server": address + ":" + ports, "auth": self.credential,
            "tls": {"sni": "node.example.invalid", "insecure": True},
            "transport": {"type": "udp", "udp": {"hopInterval": "5s"}},
            "http": {"listen": "127.0.0.1:18080"},
        }))
        self.client = self.start(["/work/hysteria-linux-amd64", "client", "-c", str(client_path), "--log-level", "warn"], "client", True)
        wait_for("官方 HY2 客户端连接", self.probe, timeout=25)

    def socket_inodes(self):
        result = set()
        for table in ("/proc/net/udp", "/proc/net/udp6"):
            for line in Path(table).read_text().splitlines()[1:]:
                fields = line.split()
                if int(fields[1].split(":")[1], 16) == 28443:
                    result.add(fields[9])
        return result

    def test_hopping(self):
        seen = set()
        finish = threading.Event()
        capture = socket.socket(socket.AF_PACKET, socket.SOCK_RAW, socket.htons(3))
        capture.bind(("yzfw0", 0))
        capture.settimeout(0.2)

        def record_ports():
            while not finish.is_set():
                try:
                    packet, meta = capture.recvfrom(65535)
                except TimeoutError:
                    continue
                if meta[2] == socket.PACKET_OUTGOING or len(packet) < 54:
                    continue
                ether = struct.unpack_from("!H", packet, 12)[0]
                if ether == 0x0800 and packet[23] == 17:
                    offset = 14 + (packet[14] & 15) * 4
                elif ether == 0x86DD and packet[20] == 17:
                    offset = 54
                else:
                    continue
                if len(packet) >= offset + 4:
                    port = struct.unpack_from("!H", packet, offset + 2)[0]
                    if port in (29440, 29441, 29500):
                        seen.add(port)

        thread = threading.Thread(target=record_ports, daemon=True)
        thread.start()
        try:
            self.start_client("29440-29441,29500")
            started = time.monotonic()
            while time.monotonic() - started < 40:
                if not self.probe():
                    raise AssertionError("端口跳跃过程中代理请求失败")
                if time.monotonic() - started >= 16 and len(seen) >= 2:
                    break
                time.sleep(0.5)
            if len(seen) < 2:
                raise AssertionError("未观察到客户端实际更换 UDP 目的端口")
            print("HY2 连续请求和真实跳跃通过，观察到端口：" + ",".join(map(str, sorted(seen))), flush=True)
        finally:
            finish.set()
            thread.join(timeout=2)
            capture.close()
            stop(self.client)

    def run(self):
        self.start_panel()
        self.echo = self.start(["python3", SCRIPT, "--echo"], "echo", True)

        def echo_ready():
            try:
                opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
                with opener.open("http://192.0.2.2:28580/test", timeout=2) as response:
                    return response.read() == PAYLOAD
            except (OSError, urllib.error.URLError):
                return False

        wait_for("客户端 HTTP 对照服务", echo_ready, timeout=10)
        self.start_node()
        wait_for("机器发现、配置下发和防火墙启动", lambda: self.running(29440))
        self.start_client("28443")
        stop(self.client)
        print("机器发现、配置下发和真实监听端口代理请求通过", flush=True)
        self.test_hopping()
        before = self.socket_inodes()
        with self.lock:
            self.config["port_hopping"] = "29600-29601,29700"
        wait_for("跳跃范围重载", lambda: self.running(29600) and not self.running(29440))
        if not before or before != self.socket_inodes():
            raise AssertionError("只更新跳跃范围却重建了实际 UDP 监听")
        for port in (29600, 29601, 29700):
            self.start_client(str(port))
        self.start_client("29700", ipv6=True)
        stop(self.client)
        print("范围重载保留内核监听，新范围及 IPv6 实际代理请求通过", flush=True)

        with self.lock:
            self.enabled = False
        wait_for("面板停用后清理规则", self.cleared)
        wait_for("面板停用后关闭监听", lambda: not self.socket_inodes())
        with self.lock:
            self.enabled = True
        wait_for("面板重新启用", lambda: self.running(29600))
        occupied = socket.socket(socket.AF_INET6, socket.SOCK_DGRAM)
        occupied.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 0)
        occupied.bind(("::", 28445))
        try:
            with self.lock:
                self.config["server_port"] = 28445
            wait_for("监听冲突后清理规则", self.cleared)
            wait_for("监听冲突后关闭旧内核", lambda: not self.socket_inodes())
        finally:
            occupied.close()
        with self.lock:
            self.config["server_port"] = 28443
        wait_for("修正配置后恢复", lambda: self.running(29600))
        self.start_client("29700")
        stop(self.client)
        wait_for("节点状态和流量回传", lambda: self.reports > 0 and self.traffic_seen)
        print("面板停用、重新启用、重载失败、恢复及状态流量回传通过", flush=True)

        stop(self.node)
        if not self.cleared():
            raise AssertionError("正常退出后残留托管规则")
        self.start_node()
        wait_for("再次启动", lambda: self.running(29600))
        stop(self.node, signal.SIGKILL)
        if self.cleared():
            raise AssertionError("异常退出测试没有留下需要恢复的规则")
        with self.lock:
            self.enabled = False
        self.start_node()
        wait_for("进程重启回收遗留规则", self.cleared)
        stop(self.node)
        print("正常退出清理和 SIGKILL 后重启恢复通过", flush=True)

    def close(self):
        for process in (self.client, self.node, self.echo):
            stop(process)
        if self.panel:
            self.panel.shutdown()
            self.panel.server_close()
        for log in self.logs:
            log.close()

    def failure_logs(self):
        for name in ("node.log", "client.log", "echo.log"):
            path = self.work / name
            if path.exists():
                text = path.read_text(errors="replace")[-7000:]
                print(name + ":\n" + text.replace(self.token, "<test-auth>").replace(self.credential, "<test-user>"), file=sys.stderr)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--kernel", choices=("xray", "singbox"))
    parser.add_argument("--backend", choices=("ufw", "firewalld"))
    parser.add_argument("--redirect", choices=("nftables", "iptables"))
    parser.add_argument("--echo", action="store_true")
    parser.add_argument("--probe", action="store_true")
    args = parser.parse_args()
    if args.echo:
        serve_echo()
        return
    if args.probe:
        probe_http()
        return
    require_isolation()
    if not args.kernel or not args.backend or not args.redirect:
        parser.error("必须指定内核和两个防火墙后端")
    os.umask(0o077)
    with tempfile.TemporaryDirectory(prefix="yz-firewall-node-", dir="/run") as directory:
        test = NodeTest(args, Path(directory))
        try:
            test.run()
            print("PASS " + "/".join((args.kernel, args.backend, args.redirect)), flush=True)
        except Exception:
            test.failure_logs()
            raise
        finally:
            test.close()


if __name__ == "__main__":
    main()
