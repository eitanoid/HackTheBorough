# Bootstrap for one runner job. The Go side starts
#     python3 -s -P -c <this file> <reply fd>
# with the job as one JSON object on stdin:
#     {"code", "input", "limits": {...}, "tmp", "audit"}
# and reads exactly one JSON object from the reply fd:
#     {"ok", "result", "error", "stdout"}
# The script's own prints never touch the reply: they go to a buffer.
import io
import json
import os
import sys
import traceback

_reply_fd = int(sys.argv[1])
_job = json.loads(sys.stdin.buffer.read())
_dumps = json.dumps
_limits = _job["limits"]

# 1. Resource limits, set before anything heavy is imported. Both soft and hard
#    are lowered so the script cannot raise them again.
try:
    import resource
except ImportError:  # Windows: dev only
    resource = None
if resource is not None:
    def _cap(which, value):
        resource.setrlimit(which, (value, value))

    _cap(resource.RLIMIT_CORE, 0)
    if _limits["mem"] > 0:
        _cap(resource.RLIMIT_AS, _limits["mem"])
    _cap(resource.RLIMIT_CPU, _limits["cpu"])
    _cap(resource.RLIMIT_NOFILE, _limits["nofile"])
    _cap(resource.RLIMIT_FSIZE, _limits["fsize"])

# 2. Import what scripts use before the audit hook goes in, so the hook never
#    has to allow whatever pandas does while importing. A failure here is the
#    runner's problem, not the script's: it exits without a reply.
import numpy  # noqa: E402,F401
import pandas  # noqa: E402,F401

# 3. Audit hook. Soft isolation: it stops mistakes and naive misuse, and covers
#    __import__ tricks. A determined script can get around it.
if _job["audit"]:
    _tmp = os.path.realpath(_job["tmp"])
    _pid = str(os.getpid())
    _busy = False

    _BLOCKED_PREFIXES = ("socket.", "ctypes.", "subprocess.", "_posixsubprocess.", "pty.")
    _BLOCKED = {
        "os.system", "os.exec", "os.fork", "os.forkpty", "os.posix_spawn", "os.spawn",
        "os.kill", "os.killpg", "signal.pthread_kill", "os.startfile",
    }
    # Events whose path arguments must stay inside the temp directory.
    _PATH_WRITES = {
        "os.remove": 1, "os.rmdir": 1, "os.mkdir": 1, "os.truncate": 1,
        "os.chmod": 1, "os.chown": 1, "os.utime": 1, "os.mkfifo": 1, "os.mknod": 1,
        "os.rename": 2, "os.link": 2, "os.symlink": 2,
    }
    _WRITE_FLAGS = os.O_WRONLY | os.O_RDWR | os.O_CREAT | os.O_APPEND | os.O_TRUNC
    _SECRET_DIRS = ("/run/secrets", "/var/run/secrets")

    def _as_path(p):
        if isinstance(p, bytes):
            return os.fsdecode(p)
        if isinstance(p, str):
            return p
        if isinstance(p, os.PathLike):
            return os.fspath(p)
        return None  # an fd or None: not a path

    def _inside_tmp(p):
        real = os.path.realpath(p)
        if real == os.devnull:
            return True
        return real == _tmp or real.startswith(_tmp + os.sep)

    def _deny(event, why):
        raise PermissionError("blocked in the script runner (%s): %s" % (event, why))

    def _hook(event, args):
        global _busy
        if event in _BLOCKED or event.startswith(_BLOCKED_PREFIXES):
            _deny(event, "not allowed")
        if event == "open":
            path = _as_path(args[0])
            if path is None:
                return
            if _busy:
                return
            _busy = True
            try:
                real = os.path.realpath(path)
                if real.startswith("/proc/"):
                    first = real[len("/proc/"):].split("/", 1)[0]
                    if first.isdigit() and first != _pid:
                        _deny(event, "other processes are off limits")
                for d in _SECRET_DIRS:
                    if real == d or real.startswith(d + "/"):
                        _deny(event, "secrets are off limits")
                flags = args[2] if len(args) > 2 and isinstance(args[2], int) else 0
                if flags & _WRITE_FLAGS and not _inside_tmp(path):
                    _deny(event, "writes are only allowed in the temp directory")
            finally:
                _busy = False
            return
        n = _PATH_WRITES.get(event)
        if n and not _busy:
            _busy = True
            try:
                for a in args[:n]:
                    p = _as_path(a)
                    if p is not None and not _inside_tmp(p):
                        _deny(event, "writes are only allowed in the temp directory")
            finally:
                _busy = False

    sys.addaudithook(_hook)

# 4. Run the script. Its prints go to a buffer; the first 4 KB are returned.
_out = io.StringIO()
sys.stdout = _out
_ns = {"INPUT": _job["input"], "__name__": "__script__"}
_ok, _result, _error = False, "null", ""
try:
    exec(compile(_job["code"], "<script>", "exec"), _ns)
    if "result" not in _ns:
        raise RuntimeError("the script did not set a global named result")
    _result = _dumps(_ns["result"], allow_nan=False)
    if len(_result) > _limits["maxresult"]:
        raise ValueError("result exceeds %d MB" % (_limits["maxresult"] // (1024 * 1024)))
    _ok = True
except BaseException:
    _result = "null"
    _error = traceback.format_exc()[-8000:]

_body = '{"ok":%s,"result":%s,"error":%s,"stdout":%s}' % (
    "true" if _ok else "false", _result, _dumps(_error), _dumps(_out.getvalue()[:4096]))
with os.fdopen(_reply_fd, "wb") as _w:
    _w.write(_body.encode("ascii"))
os._exit(0)
