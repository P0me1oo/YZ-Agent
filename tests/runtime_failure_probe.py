#!/usr/bin/env python3
"""使用实际 Node 进程复查失败状态和关闭报告；所有实例数据在退出时删除。"""
import argparse
import http.server
import json
import os
from pathlib import Path
import secrets
import signal
import socket
import socketserver
import struct
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


def free_port():
    with socket.socket() as conn:
        conn.bind(("127.0.0.1", 0))
        return conn.getsockname()[1]


def wait_for(check, timeout=12):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        try:
            if result := check():
                return result
        except (OSError, ValueError, urllib.error.URLError):
            pass
        time.sleep(0.1)
    raise RuntimeError("场景等待超时")


def receive(conn, count):
    out = b""
    while len(out) < count:
        chunk = conn.recv(count - len(out))
        if not chunk:
            raise OSError("连接提前关闭")
        out += chunk
    return out


class Echo(socketserver.BaseRequestHandler):
    def handle(self):
        try:
            while data := self.request.recv(65536):
                self.request.sendall(data)
        except OSError:
            pass


class EchoServer(socketserver.ThreadingTCPServer):
    daemon_threads = True


class Panel:
    def __init__(self):
        self.lock = threading.RLock()
        self.token = secrets.token_urlsafe(24)
        self.identities = [str(uuid.uuid4()), str(uuid.uuid4())]
        self.node_id = secrets.randbelow(1000000) + 1000000
        self.users = [0]
        self.config_revision = 1
        self.user_revision = 1
        self.config = {
            "protocol": "socks", "listen_ip": "127.0.0.1", "server_port": free_port(),
            "kernel_type": "singbox", "kernel_log_level": "fatal",
            "custom_routes": [{"ip_cidr": ["127.0.0.1/32"], "outbound": "direct"}],
        }
        self.requests = {}
        self.reports = []
        self.block_report = False
        self.fail_report = False
        self.report_started = threading.Event()
        self.report_release = threading.Event()
        self.report_release.set()

    def serve(self, handler):
        path = urllib.parse.urlsplit(handler.path)
        if handler.command == "GET":
            payload = {k: v[0] for k, v in urllib.parse.parse_qs(path.query).items()}
        else:
            payload = json.loads(handler.rfile.read(int(handler.headers.get("Content-Length", "0"))) or b"{}")
        with self.lock:
            if payload.get("token") != self.token or int(payload.get("node_id", 0)) != self.node_id:
                handler.send_error(403)
                return
            self.requests[path.path] = self.requests.get(path.path, 0) + 1
            etag = None
            block = False
            fail = False
            if path.path.endswith("/handshake"):
                body = {"websocket": {"enabled": False}, "settings": {"push_interval": 5, "pull_interval": 1}}
            elif path.path.endswith("/config"):
                body = dict(self.config)
                etag = f'"config-{self.config_revision}"'
            elif path.path.endswith("/user"):
                body = {"users": [{"id": idx + 1, "uuid": self.identities[idx]} for idx in self.users]}
                etag = f'"users-{self.user_revision}"'
            elif path.path.endswith("/report"):
                self.reports.append(payload)
                block = self.block_report
                self.block_report = False
                fail = self.fail_report
                self.fail_report = False
                self.report_started.set()
                body = {"data": True}
            else:
                handler.send_error(404)
                return
        if block:
            self.report_release.wait(35)
        try:
            if etag and handler.headers.get("If-None-Match") == etag:
                handler.send_response(304)
                handler.end_headers()
                return
            data = json.dumps(body).encode()
            handler.send_response(500 if fail else 200)
            handler.send_header("Content-Type", "application/json")
            handler.send_header("Content-Length", str(len(data)))
            if etag:
                handler.send_header("ETag", etag)
            handler.end_headers()
            handler.wfile.write(data)
        except (OSError, BrokenPipeError):
            pass

    def change_config(self, **changes):
        with self.lock:
            self.config = {**self.config, **changes}
            self.config_revision += 1

    def change_users(self, users):
        with self.lock:
            self.users = users
            self.user_revision += 1


class Instance:
    def __init__(self, binary, initial=None, panel_factory=Panel, modify_config=None):
        self.temp = tempfile.TemporaryDirectory(prefix="yznode-runtime-test-", dir="/var/tmp")
        self.root = Path(self.temp.name)
        self.panel = panel_factory()
        if initial:
            initial(self.panel)
        panel = self.panel

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                panel.serve(self)
            def do_POST(self):
                panel.serve(self)
            def log_message(self, *args):
                pass

        self.httpd = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.echo = EchoServer(("127.0.0.1", 0), Echo)
        for server in (self.httpd, self.echo):
            threading.Thread(target=server.serve_forever, daemon=True).start()
        self.health_port = free_port()
        config = {
            "panel": {"url": f"http://127.0.0.1:{self.httpd.server_port}", "token": panel.token, "node_id": panel.node_id},
            "kernel": {"type": "singbox", "log_level": "fatal", "config_dir": str(self.root / "kernel")},
            "cert": {"cert_mode": "none", "cert_dir": str(self.root / "cert")},
            "node": {"push_interval": 5, "pull_interval": 1, "track_interval": 1, "device_report_interval": 60},
            "ws": {"discovery_interval": 60},
            "health_port": self.health_port,
            "time_sync": {"enabled": False},
            "log": {"level": "error"},
        }
        if modify_config:
            modify_config(config, self)
        path = self.root / "config.json"
        path.write_text(json.dumps(config), encoding="utf-8")
        path.chmod(0o600)
        self.lines = []
        self.process = subprocess.Popen([str(binary), "-c", str(path)], cwd=self.root,
                                        stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
                                        env=dict(os.environ, GOMAXPROCS="2"))
        def drain():
            for line in self.process.stdout:
                self.lines.append(line)
        self.reader = threading.Thread(target=drain, daemon=True)
        self.reader.start()

    def health(self):
        try:
            with urllib.request.urlopen(f"http://127.0.0.1:{self.health_port}/healthz", timeout=1) as response:
                return response.status
        except urllib.error.HTTPError as error:
            return error.code
        except (OSError, urllib.error.URLError):
            return 0

    def listening(self):
        try:
            with socket.create_connection(("127.0.0.1", self.panel.config["server_port"]), timeout=0.2):
                return True
        except OSError:
            return False

    def exchange(self):
        identity = self.panel.identities[self.panel.users[0]].encode()
        with socket.create_connection(("127.0.0.1", self.panel.config["server_port"]), timeout=2) as conn:
            conn.sendall(b"\x05\x01\x02")
            if receive(conn, 2) != b"\x05\x02":
                raise OSError("认证协商失败")
            conn.sendall(b"\x01" + bytes([len(identity)]) + identity + bytes([len(identity)]) + identity)
            if receive(conn, 2) != b"\x01\x00":
                raise OSError("认证失败")
            conn.sendall(b"\x05\x01\x00\x01\x7f\x00\x00\x01" + struct.pack("!H", self.echo.server_address[1]))
            header = receive(conn, 4)
            if header[:2] != b"\x05\x00":
                raise OSError("代理目标连接失败")
            if header[3] == 1:
                receive(conn, 6)
            elif header[3] == 4:
                receive(conn, 18)
            else:
                receive(conn, receive(conn, 1)[0] + 2)
            payload = b"node-review-traffic\n" * 1024
            conn.sendall(payload)
            if receive(conn, len(payload)) != payload:
                raise OSError("流量内容不一致")
        return True

    def logs_contain(self, text):
        return any(text in line for line in self.lines)

    def close(self):
        self.panel.report_release.set()
        if self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=35)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=5)
        self.reader.join(timeout=2)
        self.process.stdout.close()
        for server in (self.httpd, self.echo):
            server.shutdown()
            server.server_close()
        self.lines.clear()
        self.temp.cleanup()


def first_start_failure(binary):
    with socket.socket() as occupied:
        occupied.bind(("127.0.0.1", 0))
        occupied.listen()
        port = occupied.getsockname()[1]
        instance = Instance(binary, lambda panel: panel.change_config(server_port=port))
        try:
            wait_for(lambda: instance.health() == 503 and instance.logs_contain("启动内核失败"))
            time.sleep(3)
            count = sum("启动内核失败，内核已停止" in line for line in instance.lines)
            alive = instance.process.poll() is None
            occupied.close()
            time.sleep(2)
            remained_failed = instance.health() == 503 and not instance.listening()
            instance.panel.change_config(server_port=free_port())
            wait_for(lambda: instance.health() == 200)
            transferred = wait_for(instance.exchange)
            return {"passed": alive and remained_failed and count == 1 and transferred,
                    "process_survived": alive, "same_snapshot_start_attempts": count,
                    "same_snapshot_remained_failed": remained_failed, "corrected_config_transfers": transferred}
        finally:
            instance.close()


def certificate_bypass(binary):
    instance = Instance(binary)
    try:
        wait_for(lambda: instance.health() == 200)
        instance.exchange()
        instance.panel.change_config(cert_config={"cert_mode": "file",
            "cert_file": str(instance.root / "missing.crt"), "key_file": str(instance.root / "missing.key")})
        wait_for(lambda: instance.health() == 503)
        before = instance.panel.requests.get("/api/v1/server/UniProxy/user", 0)
        instance.panel.change_users([0, 1])
        wait_for(lambda: instance.panel.requests.get("/api/v1/server/UniProxy/user", 0) >= before + 3)
        health = instance.health()
        listening = instance.listening()
        result = {"passed": health == 503 and not listening, "health_after_user_change": health,
                  "listening_with_invalid_certificate": listening}
        instance.panel.change_config(cert_config={"cert_mode": "none"})
        wait_for(lambda: instance.health() == 200)
        result["corrected_config_transfers"] = wait_for(instance.exchange)
        return result
    finally:
        instance.close()


INVALID_OUTBOUNDS = [{"tag": "review-invalid", "protocol": "direct", "proxy_tag": "review-missing"}]


def invalid_outbound_update(binary):
    instance = Instance(binary)
    try:
        wait_for(lambda: instance.health() == 200)
        instance.exchange()
        instance.panel.change_config(custom_outbounds=INVALID_OUTBOUNDS)
        wait_for(lambda: instance.logs_contain("normalize") or instance.health() == 503)
        time.sleep(2)
        health, listening = instance.health(), instance.listening()
        instance.panel.change_config(custom_outbounds=[])
        wait_for(lambda: instance.health() == 200)
        recovered = wait_for(instance.exchange)
        return {"passed": health == 503 and not listening and recovered, "health_after_invalid_outbound": health,
                "old_listener_active": listening, "corrected_config_transfers": recovered}
    finally:
        instance.close()


def invalid_outbound_initial(binary):
    instance = Instance(binary, lambda panel: panel.change_config(custom_outbounds=INVALID_OUTBOUNDS))
    try:
        wait_for(lambda: instance.process.poll() is not None or
                 (instance.health() == 503 and instance.logs_contain("失败")))
        time.sleep(1)
        alive, health = instance.process.poll() is None, instance.health()
        instance.panel.change_config(custom_outbounds=[])
        wait_for(lambda: instance.health() == 200)
        recovered = wait_for(instance.exchange)
        return {"passed": alive and health == 503 and recovered, "control_process_alive": alive,
                "health_after_invalid_initial_outbound": health, "corrected_config_transfers": recovered}
    finally:
        instance.close()


def slow_shutdown_report(binary, fail_first=False):
    def configure(panel):
        panel.block_report = True
        panel.fail_report = fail_first
        panel.report_release.clear()
    instance = Instance(binary, configure)
    try:
        wait_for(lambda: instance.health() == 200)
        instance.exchange()
        wait_for(instance.panel.report_started.is_set, timeout=10)
        instance.exchange()
        started = time.monotonic()
        instance.process.send_signal(signal.SIGTERM)
        release = threading.Timer(18, instance.panel.report_release.set)
        release.start()
        try:
            returncode = instance.process.wait(timeout=27)
        finally:
            instance.panel.report_release.set()
            release.cancel()
        elapsed = round(time.monotonic() - started, 3)
        with instance.panel.lock:
            reports = list(instance.panel.reports)
        final_index = 2 if fail_first else 1
        has_final = len(reports) > final_index and any(
            sum(sum(value) for value in report.get("traffic", {}).values()) > 0
            for report in reports[final_index:])
        retry_preserved = not fail_first or (len(reports) >= 3 and reports[0] == reports[1])
        distinct_final = len(reports) > final_index and reports[0]["report_id"] != reports[final_index]["report_id"]
        return {"passed": returncode == 0 and has_final and retry_preserved and distinct_final, "exit_code": returncode,
                "seconds_after_signal": elapsed, "report_batches": len(reports),
                "tail_traffic_reported": has_final, "retry_preserves_id_and_payload": retry_preserved,
                "final_report_has_new_id": distinct_final}
    finally:
        instance.close()


def slow_failed_shutdown_report(binary):
    return slow_shutdown_report(binary, fail_first=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--artifacts", type=Path, required=True)
    parser.add_argument("--only")
    args = parser.parse_args()
    if os.readlink("/proc/self/ns/net") == os.readlink("/proc/1/ns/net"):
        raise RuntimeError("需要独立网络空间")
    subprocess.run(["ip", "link", "set", "lo", "up"], check=True, capture_output=True)
    binary = (args.artifacts / "yz-agent-linux-amd64").resolve(strict=True)
    results = []
    for scenario in (first_start_failure, certificate_bypass, invalid_outbound_update,
                     invalid_outbound_initial, slow_shutdown_report, slow_failed_shutdown_report):
        if args.only and args.only != scenario.__name__:
            continue
        try:
            result = {"scenario": scenario.__name__, **scenario(binary)}
        except Exception as error:
            result = {"scenario": scenario.__name__, "passed": False, "error_type": type(error).__name__}
        results.append(result)
        print(json.dumps(result, ensure_ascii=False), flush=True)
    (args.artifacts / "runtime-failure-results.json").write_text(
        json.dumps(results, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    raise SystemExit(0 if all(row["passed"] for row in results) else 1)


if __name__ == "__main__":
    main()
