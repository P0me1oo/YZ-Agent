#!/usr/bin/env python3
"""用真实控制终端验证管道安装的 HTTP 确认，不操作服务、网络或系统目录。"""

import errno
import os
from pathlib import Path
import pty
import select
import signal
import subprocess
import tempfile
import time


ROOT = Path(__file__).resolve().parents[1]
PROMPT = "确认继续使用 HTTP 面板？[y/N]：".encode()
MARKER = b"INSTALL_CONTINUED"
library = (ROOT / "install.sh").read_text().replace('\nmain "$@"\n', "\n")
# 在第一次系统路径检查处停止；其前面不应有网络请求或系统修改。
harness = library + """
trap - EXIT ERR INT TERM
check_root() { :; }
select_install_root() { printf 'INSTALL_CONTINUED\\n'; exit 0; }
main "$@"
"""


def args(url="http://panel.example.invalid", extra=()):
    return ["--panel", url, "--token", "confirmation-test-token", "--machine-id", "5", *extra]


def run_terminal(script, answer, *, url="http://panel.example.invalid", extra=()):
    # /dev/tty 指向此控制终端，标准输入则保持为安装脚本管道。
    script_in, script_out = os.pipe()
    pid, terminal = pty.fork()
    if pid == 0:
        os.close(script_out)
        os.dup2(script_in, 0)
        os.close(script_in)
        os.execvp("bash", ["bash", "-s", "--", *args(url, extra)])
    os.close(script_in)
    data = bytearray()
    sent = False
    completed = False
    try:
        with os.fdopen(script_out, "wb") as output:
            output.write(script.encode())
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if select.select([terminal], [], [], 0.1)[0]:
                try:
                    part = os.read(terminal, 65536)
                except OSError as exc:
                    if exc.errno != errno.EIO:
                        raise
                    part = b""
                if not part:
                    _, status = os.waitpid(pid, 0)
                    completed = True
                    return os.waitstatus_to_exitcode(status), bytes(data)
                data.extend(part)
                if PROMPT in data and not sent:
                    assert MARKER not in data, "确认前进入了系统操作"
                    os.write(terminal, answer)
                    sent = True
        raise AssertionError("确认过程超时")
    finally:
        os.close(terminal)
        if not completed:
            try:
                os.kill(pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            os.waitpid(pid, 0)


with tempfile.TemporaryDirectory(prefix="yz-agent-http-confirm-") as directory:
    path = Path(directory) / "installer.sh"
    path.write_text(harness)

    for name, answer, accepted in [
        ("yes", b"y\n", True),
        ("uppercase", b"YES\n", True),
        ("default", b"\n", False),
        ("no", b"n\n", False),
        ("invalid", b"continue\n", False),
        ("eof", b"\x04", False),
    ]:
        code, output = run_terminal(harness, answer, extra=("--yes",))
        assert (code == 0) == accepted, (name, code, output.decode())
        assert (MARKER in output) == accepted, name
        assert "认证令牌、配置和文件加密密钥可能被截获或篡改".encode() in output, name
        assert b"confirmation-test-token" not in output, name
        print(f"PASS terminal/{name}")

    code, output = run_terminal(
        harness, b"yes\n", url="  HTTP://panel.example.invalid/private?token=url-secret"
    )
    assert code == 0 and MARKER in output and PROMPT in output
    assert b"url-secret" not in output
    print("PASS uppercase-and-whitespace")

    # 没有控制终端时，stdin 的 y 和 --yes 均不能绕过人工确认。
    for extra in ((), ("--yes",)):
        result = subprocess.run(
            ["bash", str(path), *args(extra=extra)],
            input=b"y\n",
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            start_new_session=True,
            timeout=10,
        )
        assert result.returncode != 0 and MARKER not in result.stdout
        assert "需要在交互终端手动确认".encode() in result.stdout
    print("PASS no-terminal")

    # HTTPS 不需要 HTTP 风险确认；安装入口不接触证书校验逻辑。
    for url in ("https://panel.example.invalid", "HTTPS://panel.example.invalid"):
        result = subprocess.run(
            ["bash", str(path), *args(url)],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            start_new_session=True,
            timeout=10,
        )
        assert result.returncode == 0 and MARKER in result.stdout
        assert PROMPT not in result.stdout
    print("PASS https-no-http-prompt")

    # 升级沿用已有配置，不引入开机或远程升级时的交互等待。
    result = subprocess.run(
        ["bash", str(path), "upgrade"],
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        start_new_session=True,
        timeout=10,
    )
    assert result.returncode == 0 and MARKER in result.stdout
    assert PROMPT not in result.stdout
    print("PASS upgrade-no-prompt")

print("HTTP 安装手动确认测试通过")
