#!/usr/bin/env python3
"""在同一个 Node 进程中验证两个节点的故障隔离。"""
import argparse
import json
import os
import socket
from pathlib import Path
import subprocess
import threading
import time
import urllib.parse
from runtime_failure_probe import Instance, Panel, INVALID_OUTBOUNDS, free_port, wait_for


class MultiPanel(Panel):
    def __init__(self, mode, trigger):
        super().__init__()
        self.mode = mode
        self.trigger = trigger
        self.second_id = self.node_id + 1
        self.machine_id = self.node_id + 2
        self.second_release = threading.Event()
        second_kernel = "xray" if mode == "machine" else "singbox"
        second_protocol = "vless" if mode == "machine" else "http"
        second = {
            "protocol": second_protocol, "listen_ip": "127.0.0.1",
            "server_port": self.config["server_port"] if trigger == "port_conflict" else free_port(),
            "kernel_type": second_kernel, "kernel_log_level": "none" if second_kernel == "xray" else "fatal",
        }
        if trigger == "invalid_initial_outbound":
            second["custom_outbounds"] = INVALID_OUTBOUNDS
        self.configs = {self.node_id: self.config, self.second_id: second}
        self.revisions = {self.node_id: 1, self.second_id: 1}

    def serve(self, handler):
        path = urllib.parse.urlsplit(handler.path)
        if handler.command == "GET":
            payload = {key: value[0] for key, value in urllib.parse.parse_qs(path.query).items()}
        else:
            payload = json.loads(handler.rfile.read(int(handler.headers.get("Content-Length", "0"))) or b"{}")
        node_id = int(payload.get("node_id", 0))
        if payload.get("token") != self.token:
            handler.send_error(403)
            return
        if node_id == self.second_id and path.path.endswith("/config"):
            self.second_release.wait(20)
            if self.trigger == "initial_http_failure":
                handler.send_error(503)
                return
        etag = None
        if path.path.endswith("/machine/nodes"):
            body = {"nodes": [
                {"id": node, "type": cfg["protocol"], "name": "isolated-test", "kernel_type": cfg["kernel_type"]}
                for node, cfg in self.configs.items()],
                "base_config": {"push_interval": 10, "pull_interval": 30}}
        elif path.path.endswith("/handshake"):
            body = {"websocket": {"enabled": False}, "settings": {"push_interval": 5, "pull_interval": 1}}
        elif path.path.endswith("/config") and node_id in self.configs:
            body = self.configs[node_id]
            etag = f'"config-{node_id}-{self.revisions[node_id]}"'
        elif path.path.endswith("/user") and node_id in self.configs:
            body = {"users": [{"id": 1, "uuid": self.identities[0]}]}
            etag = f'"users-{node_id}"'
        elif path.path.endswith("/report") or path.path.endswith("/machine/status"):
            body = {"data": True}
        else:
            handler.send_error(404)
            return
        try:
            if etag and handler.headers.get("If-None-Match") == etag:
                handler.send_response(304)
                handler.end_headers()
                return
            data = json.dumps(body).encode()
            handler.send_response(200)
            handler.send_header("Content-Type", "application/json")
            handler.send_header("Content-Length", str(len(data)))
            if etag:
                handler.send_header("ETag", etag)
            handler.end_headers()
            handler.wfile.write(data)
        except OSError:
            pass


def scenario(binary, mode, trigger):
    def configure(config, instance):
        panel = instance.panel
        if mode == "machine":
            config["machine"] = {"machine_id": panel.machine_id, "token": panel.token}
        else:
            config["nodes"] = [{"node_id": panel.node_id, "node_type": "socks"},
                               {"node_id": panel.second_id, "node_type": "http"}]
    instance = Instance(binary, panel_factory=lambda: MultiPanel(mode, trigger), modify_config=configure)
    try:
        wait_for(instance.exchange)
        first_node_worked_before = True
        instance.panel.second_release.set()
        wait_for(lambda: instance.logs_contain("失败") or instance.logs_contain("exited with error") or
                 instance.process.poll() is not None)
        time.sleep(1)
        alive = instance.process.poll() is None
        try:
            first_node_works_after = instance.exchange()
        except OSError:
            first_node_works_after = False
        health_after_failure = instance.health()
        result = {"mode": mode, "trigger": trigger, "healthy_node_worked_before": first_node_worked_before,
                "healthy_node_transfers_after_failure": first_node_works_after, "process_alive": alive,
                "health_status": health_after_failure, "exit_code": instance.process.poll()}
        result["passed"] = alive and first_node_works_after and health_after_failure == 503
        if result["passed"] and trigger != "initial_http_failure":
            panel = instance.panel
            panel.configs[panel.second_id] = {**panel.configs[panel.second_id], "server_port": free_port(), "custom_outbounds": []}
            panel.revisions[panel.second_id] += 1
            wait_for(lambda: instance.health() == 200)
            with socket.create_connection(("127.0.0.1", panel.configs[panel.second_id]["server_port"]), timeout=2):
                result["corrected_node_listening"] = True
            result["healthy_node_transfers_after_recovery"] = instance.exchange()
            result["passed"] = result["healthy_node_transfers_after_recovery"]
        return result
    finally:
        instance.panel.second_release.set()
        instance.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--artifacts", type=Path, required=True)
    args = parser.parse_args()
    if os.readlink("/proc/self/ns/net") == os.readlink("/proc/1/ns/net"):
        raise RuntimeError("需要独立网络空间")
    subprocess.run(["ip", "link", "set", "lo", "up"], check=True, capture_output=True)
    binary = (args.artifacts / "yz-agent-linux-amd64").resolve(strict=True)
    results = []
    for mode in ("machine", "static"):
        for trigger in ("port_conflict", "invalid_initial_outbound", "initial_http_failure"):
            try:
                result = scenario(binary, mode, trigger)
            except Exception as error:
                result = {"mode": mode, "trigger": trigger, "passed": False, "error_type": type(error).__name__}
            results.append(result)
            print(json.dumps(result, ensure_ascii=False), flush=True)
    (args.artifacts / "multi-node-failure-results.json").write_text(
        json.dumps(results, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    raise SystemExit(0 if all(row["passed"] for row in results) else 1)


if __name__ == "__main__":
    main()
