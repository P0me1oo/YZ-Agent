#!/usr/bin/env python3
"""在独立网络命名空间中验证安装器、面板下发、代理收发和重载恢复。"""

import argparse
import hashlib
import http.server
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import socketserver
import struct
import subprocess
import tempfile
import threading
import time
import urllib.parse
import urllib.request
import uuid


PAYLOAD = b"sing-box-install-probe\n" * 1024


def command(*args, env=None, check=True):
    result = subprocess.run(args, env=env, text=True, capture_output=True, timeout=90)
    if check and result.returncode:
        # 子进程输出可能含临时配置，失败时仅报告命令名和退出码。
        raise RuntimeError(f"{Path(args[0]).name} 返回 {result.returncode}")
    return result


def wait_for(check, label, timeout=30):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            result = check()
            if result:
                return result
        except (OSError, ValueError):
            pass
        time.sleep(0.2)
    raise RuntimeError(f"等待超时：{label}")


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def receive(conn, length):
    result = bytearray()
    while len(result) < length:
        data = conn.recv(length - len(result))
        if not data:
            raise OSError("连接提前关闭")
        result.extend(data)
    return bytes(result)


def connect_proxy(port, identity, destination):
    conn = socket.create_connection(("127.0.0.1", port), timeout=2)
    try:
        conn.sendall(b"\x05\x01\x02")
        if receive(conn, 2) != b"\x05\x02":
            raise OSError("SOCKS5 认证方式不符")
        credential = identity.encode()
        conn.sendall(b"\x01" + bytes([len(credential)]) + credential + bytes([len(credential)]) + credential)
        if receive(conn, 2) != b"\x01\x00":
            raise OSError("SOCKS5 认证被拒绝")
        conn.sendall(b"\x05\x01\x00\x01\x7f\x00\x00\x01" + struct.pack("!H", destination))
        header = receive(conn, 4)
        if header[:2] != b"\x05\x00":
            raise OSError("SOCKS5 目标被拒绝")
        if header[3] == 1:
            receive(conn, 6)
        elif header[3] == 4:
            receive(conn, 18)
        else:
            receive(conn, receive(conn, 1)[0] + 2)
        return conn
    except Exception:
        conn.close()
        raise


def exchange(conn):
    conn.sendall(PAYLOAD)
    if receive(conn, len(PAYLOAD)) != PAYLOAD:
        raise RuntimeError("代理返回内容不一致")


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
        self.token = secrets.token_urlsafe(32)
        self.node_id = secrets.randbelow(1000000) + 1000000
        self.identities = [str(uuid.uuid4()), str(uuid.uuid4())]
        self.port = free_port()
        self.user_revision = 1
        self.config_revision = 1
        self.users = [1]
        self.route = "direct"
        self.requests = {}
        self.report_count = 0
        self.traffic = {}

    def serve(self, handler):
        path = urllib.parse.urlsplit(handler.path)
        if handler.command == "GET":
            payload = {key: value[0] for key, value in urllib.parse.parse_qs(path.query).items()}
        else:
            payload = json.loads(handler.rfile.read(int(handler.headers.get("Content-Length", "0"))) or b"{}")
        with self.lock:
            if payload.get("token") != self.token or int(payload.get("node_id", 0)) != self.node_id:
                handler.send_error(403)
                return
            self.requests[path.path] = self.requests.get(path.path, 0) + 1
            etag = None
            if path.path.endswith("/handshake"):
                body = {"websocket": {"enabled": False}, "settings": {"push_interval": 5, "pull_interval": 1}}
            elif path.path.endswith("/config"):
                etag = f'"config-{self.config_revision}"'
                route = {"ip_cidr": ["127.0.0.1/32"], "outbound": "direct"}
                if self.route == "reject":
                    route = {"ip_cidr": ["127.0.0.1/32"], "action": "reject"}
                elif self.route == "invalid":
                    route = {"rule_set": "missing-install-test-set", "outbound": "direct"}
                body = {
                    "protocol": "socks", "listen_ip": "127.0.0.1", "server_port": self.port,
                    "kernel_type": "singbox", "kernel_log_level": "fatal", "custom_routes": [route],
                    "base_config": {"push_interval": 5, "pull_interval": 1},
                }
            elif path.path.endswith("/user"):
                etag = f'"users-{self.user_revision}"'
                body = {"users": [{"id": user, "uuid": self.identities[user - 1]} for user in self.users]}
            elif path.path.endswith("/report"):
                self.report_count += 1
                for user, counts in payload.get("traffic", {}).items():
                    total = self.traffic.setdefault(user, [0, 0])
                    total[0] += counts[0]
                    total[1] += counts[1]
                body = {"data": True}
            else:
                handler.send_error(404)
                return
            if etag and handler.headers.get("If-None-Match") == etag:
                handler.send_response(304)
                handler.end_headers()
                return
            handler.send_response(200)
            handler.send_header("Content-Type", "application/json")
            if etag:
                handler.send_header("ETag", etag)
            data = json.dumps(body).encode()
            handler.send_header("Content-Length", str(len(data)))
            handler.end_headers()
            handler.wfile.write(data)

    def set_users(self, users):
        with self.lock:
            self.users = users
            self.user_revision += 1

    def set_route(self, route):
        with self.lock:
            self.route = route
            self.config_revision += 1


def file_identity(path):
    file = Path(path)
    if not file.exists():
        return None
    return hashlib.sha256(file.read_bytes()).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--artifacts", type=Path, required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--preserve-service", required=True)
    args = parser.parse_args()
    if os.geteuid() != 0 or os.readlink("/proc/self/ns/net") == os.readlink("/proc/1/ns/net"):
        raise RuntimeError("必须由 root 在 unshare --net 创建的独立网络命名空间中执行")
    artifacts = args.artifacts.resolve(strict=True)
    for file in ("install.sh", "yz-agent-linux-amd64", "singbox_isolated_install.sh"):
        if not (artifacts / file).is_file():
            raise RuntimeError(f"缺少测试产物：{file}")
    command("ip", "link", "set", "lo", "up")
    baseline_pid = command("systemctl", "show", args.preserve_service, "-p", "MainPID", "--value").stdout.strip()
    if baseline_pid in ("", "0"):
        raise RuntimeError("需要保留的原服务未运行，停止安装验收")
    preserved = {path: file_identity(path) for path in ("/usr/local/bin/yz-agent", "/usr/bin/yz-agent", "/usr/local/bin/xboard-node", "/usr/local/bin/xbctl", "/usr/bin/xbctl")}
    root = Path(tempfile.mkdtemp(prefix="yz-agent-singbox-test-", dir="/var/tmp"))
    (root / ".owned-test-instance").touch(mode=0o600)
    service = "yz-agent-singbox-test-" + secrets.token_hex(6)
    panel = Panel()

    class Handler(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            panel.serve(self)

        def do_POST(self):
            panel.serve(self)

        def log_message(self, *unused):
            pass

    httpd = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    echo = EchoServer(("127.0.0.1", 0), Echo)
    for server in (httpd, echo):
        threading.Thread(target=server.serve_forever, daemon=True).start()
    health_port = free_port()
    env = dict(os.environ, YZ_TEST_ROOT=str(root), YZ_TEST_ARTIFACTS=str(artifacts),
               YZ_TEST_SERVICE=service, YZ_TEST_NETNS=f"/proc/{os.getpid()}/ns/net",
               YZ_TEST_TOKEN=panel.token, YZ_TEST_PANEL=f"http://127.0.0.1:{httpd.server_port}",
               YZ_TEST_NODE_ID=str(panel.node_id), YZ_TEST_HEALTH_PORT=str(health_port), YZ_TEST_VERSION=args.version)
    connections = []

    def connect(user):
        conn = connect_proxy(panel.port, panel.identities[user - 1], echo.server_address[1])
        try:
            exchange(conn)
        except Exception:
            conn.close()
            raise
        connections.append(conn)
        return conn

    def healthy():
        with urllib.request.urlopen(f"http://127.0.0.1:{health_port}/healthz", timeout=2) as response:
            return response.status == 200

    def rejected(user):
        try:
            conn = connect_proxy(panel.port, panel.identities[user - 1], echo.server_address[1])
            with conn:
                exchange(conn)
            return False
        except OSError:
            return True

    try:
        for attempt in range(2):
            command("bash", str(artifacts / "singbox_isolated_install.sh"), env=env)
            wait_for(healthy, "安装健康检查")
            installed_pid = command("systemctl", "show", service + ".service", "-p", "MainPID", "--value").stdout.strip()
            if os.readlink(f"/proc/{installed_pid}/ns/net") != os.readlink("/proc/self/ns/net"):
                raise RuntimeError("测试服务没有进入独立网络命名空间")
            metadata = json.loads((root / "instance/install-meta.json").read_text())
            if metadata.get("instance_count") != 1:
                raise RuntimeError("重复安装产生了额外实例")
            with wait_for(lambda: connect(1), "安装后代理收发"):
                pass
            for version_arg in ("-v", "version"):
                if file_identity(root / "bin/yz-agent") != file_identity(artifacts / "yz-agent-linux-amd64"):
                    raise RuntimeError("安装产物校验值不一致")
                version = command(str(root / "bin/yz-agent"), version_arg).stdout
                if args.version not in version or "requested v1.14.0" not in version:
                    raise RuntimeError("安装后的版本标识不一致")
            print(f"安装及重复安装 {attempt + 1}/2：通过", flush=True)

        panel.set_users([1, 2])
        second = wait_for(lambda: connect(2), "面板新增用户")
        panel.set_users([2])
        wait_for(lambda: rejected(1), "面板删除用户")
        exchange(second)
        panel.set_route("reject")
        wait_for(lambda: rejected(2), "面板拒绝路由")
        exchange(second)
        with panel.lock:
            before = panel.requests.get("/api/v1/server/UniProxy/config", 0)
        panel.set_route("invalid")
        wait_for(lambda: panel.requests.get("/api/v1/server/UniProxy/config", 0) >= before + 3, "错误配置重试")
        if not rejected(2):
            raise RuntimeError("错误路由更新破坏了原拒绝规则")
        exchange(second)
        panel.set_route("direct")
        with wait_for(lambda: connect(2), "恢复原路由"):
            pass
        wait_for(lambda: panel.report_count > 0 and all(value > 0 for value in panel.traffic.get("2", [0, 0])), "流量和状态上报")
        print("用户同步、连接保留、路由错误回滚及恢复、流量和状态上报：通过", flush=True)
        for conn in connections:
            conn.close()
        command("systemctl", "restart", service + ".service")
        with wait_for(lambda: connect(2), "systemd 重启恢复"):
            pass
        print("systemd 重启恢复：通过", flush=True)
    finally:
        for conn in connections:
            conn.close()
        command("systemctl", "stop", service + ".service", check=False)
        command("systemctl", "disable", "--runtime", service + ".service", check=False)
        Path(f"/run/systemd/system/{service}.service").unlink(missing_ok=True)
        command("systemctl", "daemon-reload")
        command("systemctl", "reset-failed", service + ".service", check=False)
        httpd.shutdown()
        echo.shutdown()
        httpd.server_close()
        echo.server_close()
        if root.parent != Path("/var/tmp") or not (root / ".owned-test-instance").is_file():
            raise RuntimeError("测试目录校验失败，停止清理")
        shutil.rmtree(root)
        if command("systemctl", "show", args.preserve_service, "-p", "MainPID", "--value").stdout.strip() != baseline_pid:
            raise RuntimeError("原服务进程与测试前不一致")
        if any(file_identity(path) != checksum for path, checksum in preserved.items()):
            raise RuntimeError("原安装文件校验值与测试前不一致")
        print("隔离实例和凭据已清理；原服务进程及安装文件未变：通过", flush=True)


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        # 只输出预定义诊断；网络异常的完整 URL 可能包含临时凭据。
        print(f"安装验收失败：{error if isinstance(error, RuntimeError) else type(error).__name__}", flush=True)
        raise SystemExit(1)
