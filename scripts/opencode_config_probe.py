"""
Asks a real OpenCode binary which config documents it loaded for a config given
in OPENCODE_CONFIG_CONTENT, and classifies the answer. Test support for
scripts/test_triage_run.py.

The question is `opencode api --standalone GET /api/config`: it starts a private
server, prints the config documents it loaded as JSON, and exits. It makes no
model call and opens no session. Each invocation gets its own HOME and working
directory under the directory the caller names, and its environment holds PATH,
HOME and the config alone, so none of the operator's own config, plugins or keys
is read.

probe_config() returns (status, reason, infos). The three statuses are the ones
DIR-002 asks for, so an incomplete check is never read as a pass or a failure:

  CHECKED          the binary ran and listed at least one document that carries
                   the agent's step cap; `infos` holds the `info` object of each.
  COULD_NOT_CHECK  the check could not be completed: no binary, a binary that
                   could not be started, no answer in time, output that is not
                   JSON, or a non-zero exit that an empty config also produces.
  FAILED           the check was completed and the premise is false: the binary
                   ran and the config did not arrive. Either it exited non-zero
                   on the layered config but not on an empty one, or it exited 0
                   with valid JSON that lists no document carrying the step cap.

A non-zero exit is split by one control run (CONTROL_CONFIG, an empty config,
the same binary and the same kind of temporary HOME). A binary that cannot run
at all on this host (a broken install, a sandbox that denies it something it
needs) exits non-zero on the control as well, and that is COULD_NOT_CHECK. A
binary that runs the control and refuses the layered config is FAILED, with its
stderr in the reason.
"""

import json
import os
import signal
import subprocess
import tempfile

CHECKED = "checked"
COULD_NOT_CHECK = "could-not-check"
FAILED = "failed"

# Given in place of the layered config when a run exits non-zero. It names
# nothing, so a binary that exits non-zero on it is unusable on this host.
CONTROL_CONFIG = "{}"

STDERR_LIMIT = 400


def flat(text):
    """`text` on one line, whitespace runs collapsed, cut to STDERR_LIMIT characters."""
    one = " ".join(text.split())
    if len(one) > STDERR_LIMIT:
        return one[:STDERR_LIMIT] + "..."
    return one


def invoke(binary, config_text, workdir, timeout):
    """
    Run `binary api --standalone GET /api/config` with `config_text` as
    OPENCODE_CONFIG_CONTENT. Returns (how, returncode, stdout, stderr), where
    `how` is "exited", "timeout" or "unstartable". On a timeout the process
    group is killed and reaped, the returncode is None and both streams are empty.
    """
    home = tempfile.mkdtemp(prefix="home-", dir=workdir)
    cwd = tempfile.mkdtemp(prefix="cwd-", dir=workdir)
    env = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "HOME": home, "OPENCODE_CONFIG_CONTENT": config_text}
    try:
        proc = subprocess.Popen([binary, "api", "--standalone", "GET", "/api/config"], env=env, cwd=cwd,
                                stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                text=True, start_new_session=True)
    except OSError as exc:
        return "unstartable", None, "", str(exc)
    try:
        out, err = proc.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        os.killpg(proc.pid, signal.SIGKILL)
        proc.communicate()
        return "timeout", None, "", ""
    return "exited", proc.returncode, out, err


def describe(how, returncode, timeout):
    """How an invoke() ended, as a phrase for a reason."""
    if how == "unstartable":
        return "could not be started"
    if how == "timeout":
        return "did not answer within %g seconds" % timeout
    return "exited %d" % returncode


def matching_infos(documents, agent, steps):
    """The `info` of every document that lists `agent` with the step cap `steps`. Anything that is not a list of documents has none."""
    if not isinstance(documents, list):
        return []
    return [d["info"] for d in documents if isinstance(d, dict) and isinstance(d.get("info"), dict)
            and isinstance(d["info"].get("agents"), dict)
            and isinstance(d["info"]["agents"].get(agent), dict)
            and d["info"]["agents"][agent].get("steps") == steps]


def probe_config(binary, config, agent, steps, workdir, timeout=60):
    """
    Classify what `binary` (a path, or None when there is none) reports for
    `config`, a dict. The document that came from `config` is found by the step
    cap `steps` set on `agent`: that is the positive control that the binary read
    OPENCODE_CONFIG_CONTENT at all. Returns (status, reason, infos); `reason` is
    empty for CHECKED, and `infos` is empty unless the status is CHECKED.
    """
    if binary is None:
        return COULD_NOT_CHECK, "no opencode on PATH and OPENCODE_BIN names none", []
    how, rc, out, err = invoke(binary, json.dumps(config), workdir, timeout)
    if how != "exited":
        return COULD_NOT_CHECK, "opencode api --standalone %s%s" % (describe(how, rc, timeout),
                                                                     (": " + flat(err)) if err else ""), []
    if rc != 0:
        chow, crc, _, cerr = invoke(binary, CONTROL_CONFIG, workdir, timeout)
        if chow == "exited" and crc == 0:
            return FAILED, ("opencode api --standalone exited %d on the layered config and 0 on an empty config, "
                            "so it refused the layered config; stderr: %s" % (rc, flat(err))), []
        return COULD_NOT_CHECK, ("opencode api --standalone exited %d, and with an empty config it %s, so it "
                                 "does not run on this host; stderr: %s / %s"
                                 % (rc, describe(chow, crc, timeout), flat(err), flat(cerr))), []
    try:
        documents = json.loads(out)
    except ValueError:
        return COULD_NOT_CHECK, "opencode api --standalone printed something that is not JSON: %r" % (out[:120],), []
    infos = matching_infos(documents, agent, steps)
    if not infos:
        listed = len(documents) if isinstance(documents, list) else type(documents).__name__
        return FAILED, ("opencode api --standalone exited 0 with JSON that lists no document with step cap %r on "
                        "agent %r (documents listed: %s), so the binary did not carry OPENCODE_CONFIG_CONTENT"
                        % (steps, agent, listed)), []
    return CHECKED, "", infos
