#!/usr/bin/env python3
"""Explicit, fail-closed real-VM test harness. No instance lifecycle mutations.

Only a disposable, independently marked Vultr staging VM is eligible. The
probe uses Vultr's link-local identity endpoint and the local Paper probe;
all observations are redacted. A restore is to a separate scratch directory.
"""
import argparse
import base64
import hashlib
import json
import os
import re
import shlex
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path

PROD_NAMES = {"prod", "production", "vanilla_no_grief", "minecraft-vanilla"}
UUID = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")
ALIAS = re.compile(r"^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$")
SERVER = re.compile(r"^recovery-test-[a-z0-9-]{1,48}$")
INVOCATION = re.compile(r"^[0-9a-f]{32}$")
SHA256 = re.compile(r"^[0-9a-f]{64}$")
REMOTE = re.compile(r"^stagingwasabi:[a-z0-9-]*staging[a-z0-9-]*/recovery-staging/recovery-test-[a-z0-9-]+/[a-z0-9-]+$")
MARKER = "/etc/gsc-recovery-staging.json"
PURPOSE = "minecraft-recovery-isolated-test"

# This script is sent to the *already existing* staging VM over authenticated
# SSH. The metadata JWT itself never leaves that VM, and no shell-derived
# values from the response are executed.
REMOTE_OBSERVE = r'''
import base64, json, os, stat, subprocess, time, urllib.request
p = "/etc/gsc-recovery-staging.json"
s = os.lstat(p)
if not stat.S_ISREG(s.st_mode) or s.st_uid != 0 or (s.st_mode & 0o022):
    raise SystemExit("staging marker ownership or mode is unsafe")
with open(p, encoding="utf-8") as stream:
    marker = json.load(stream)
if marker.get("environment") != "staging" or marker.get("purpose") != "minecraft-recovery-isolated-test":
    raise SystemExit("not an explicitly marked disposable staging VM")
if marker.get("unit") != "minecraft-vanilla.service":
    raise SystemExit("staging unit is not pinned")
request = urllib.request.Request("http://169.254.169.254/v1/identity/token")
with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request, timeout=3) as reply:
    token = reply.read(8192).decode("ascii")
parts = token.split(".")
if len(parts) != 3:
    raise SystemExit("invalid Vultr instance identity")
payload = json.loads(base64.urlsafe_b64decode(parts[1] + "=" * (-len(parts[1]) % 4)))
if payload.get("iss") != "https://api.vultr.com/v2/oidc/identity/instance":
    raise SystemExit("unexpected Vultr metadata issuer")
if payload.get("resource_type") != "instance" or payload.get("exp", 0) <= time.time():
    raise SystemExit("Vultr metadata is not a fresh instance token")
instance_id = str(payload.get("sub", "")).removeprefix("instance:")
if marker.get("instance_id") != instance_id:
    raise SystemExit("staging marker does not match Vultr instance")
r = subprocess.run(["python3", "/usr/local/libexec/gsc/minecraft_probe.py"],
                   capture_output=True, text=True, timeout=12, check=False)
if r.returncode or len(r.stdout) > 4096:
    raise SystemExit("Paper readiness probe failed")
probe = json.loads(r.stdout)
print(json.dumps({"environment":"staging", "purpose":marker["purpose"],
 "server_id":marker.get("server_id"), "instance_id":instance_id,
 "unit":marker["unit"], "probe":probe,
 "observed_at":int(time.time())}, separators=(",", ":")))
'''

# A real Wasabi restore is *never* written into Paper's running directory.
# It runs on the remote isolated instance with its separately scoped rclone
# credentials already configured. No object gets deleted or overwritten.
REMOTE_RESTORE = r'''
import base64, hashlib, json, os, pathlib, re, stat, subprocess, sys, tempfile, time, urllib.request
p = "/etc/gsc-recovery-staging.json"
s = os.lstat(p)
if not stat.S_ISREG(s.st_mode) or s.st_uid != 0 or (s.st_mode & 0o022):
    raise SystemExit("invalid staging marker permissions")
with open(p, encoding="utf-8") as f:
    marker = json.load(f)
if marker.get("environment") != "staging" or marker.get("purpose") != "minecraft-recovery-isolated-test":
    raise SystemExit("restore only permitted in isolated staging")
args = json.loads(base64.urlsafe_b64decode(sys.argv[1]))
if marker.get("instance_id") != args["instance_id"] or marker.get("server_id") != args["server_id"]:
    raise SystemExit("wrong target VM or server")
# Verify the instance independently on this second connection too. The first
# read-only preflight cannot authorize a later connection to a different VM.
with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(
        "http://169.254.169.254/v1/identity/token", timeout=3) as reply:
    token = reply.read(8192).decode("ascii")
parts = token.split(".")
if len(parts) != 3:
    raise SystemExit("invalid Vultr metadata response")
claim = json.loads(base64.urlsafe_b64decode(parts[1] + "=" * (-len(parts[1]) % 4)))
if claim.get("iss") != "https://api.vultr.com/v2/oidc/identity/instance":
    raise SystemExit("Vultr metadata issuer mismatch")
if claim.get("sub") != "instance:" + args["instance_id"]:
    raise SystemExit("Vultr instance changed since preflight")
if claim.get("resource_type") != "instance" or claim.get("exp", 0) <= time.time():
    raise SystemExit("Vultr instance metadata is not current")
if marker.get("restore_root") != "/srv/gsc-recovery-restore":
    raise SystemExit("remote scratch root is not the required isolated path")
root = pathlib.Path("/srv/gsc-recovery-restore")
if not root.is_dir() or root.is_symlink() or root.resolve() != root:
    raise SystemExit("scratch restore root is absent or unsafe")
source = args["source"]
prefix = marker.get("remote_prefix", "")
if not prefix.endswith("/") or not source.startswith(prefix):
    raise SystemExit("remote backup is outside approved staging-only scope")
if not re.fullmatch(r"stagingwasabi:[a-z0-9-]*staging[a-z0-9-]*/recovery-staging/recovery-test-[a-z0-9-]+/[a-z0-9-]+", source):
    raise SystemExit("remote backup source is not an isolated test prefix")
# Refuse an unbounded download or one that could fill the staging disk.
inventory = subprocess.run(["rclone", "size", "--json", source],
                           capture_output=True, text=True, timeout=90, check=False)
if inventory.returncode or len(inventory.stdout) > 16384:
    raise SystemExit("could not independently bound staging backup size")
limits = json.loads(inventory.stdout)
total = limits.get("bytes")
count = limits.get("count")
if not isinstance(total, int) or not isinstance(count, int):
    raise SystemExit("unverifiable staging backup byte and file count")
if total <= 0 or total > 8 * 1024**3 or count <= 0 or count > 50000:
    raise SystemExit("staging backup exceeds the safe restore limits")
disk = os.statvfs(root)
if disk.f_bavail * disk.f_frsize < total * 2 + 512 * 1024**2:
    raise SystemExit("insufficient isolated scratch capacity for backup copy")
destination = pathlib.Path(tempfile.mkdtemp(prefix="gsc-restore-", dir=root))
result = subprocess.run(["rclone", "copy", "--checksum", source, str(destination)],
                        capture_output=True, text=True, timeout=900, check=False)
if result.returncode:
    raise SystemExit("staging Wasabi restore failed")
marker_file = destination / "recovery-test-marker.txt"
if not marker_file.is_file() or marker_file.is_symlink():
    raise SystemExit("restored world marker not found")
digest = hashlib.sha256(marker_file.read_bytes()).hexdigest()
if digest != args["marker_sha256"]:
    raise SystemExit("restored world marker hash mismatch")
level_data = list(destination.rglob("level.dat"))
if not level_data or not any(path.is_file() and not path.is_symlink() for path in level_data):
    raise SystemExit("no Minecraft world level.dat in restored backup")
print(json.dumps({"result":"verified_restore_files", "instance_id":args["instance_id"],
                  "source":source, "restore_directory":str(destination),
                  "marker_sha256":digest, "world_files_present":True}, separators=(",",":")))
'''


class UnsafeStagingTarget(ValueError):
    pass


@dataclass(frozen=True)
class Target:
    environment: str
    server_id: str
    instance_id: str
    ssh_alias: str
    unit: str

    @classmethod
    def load(cls, path):
        document = json.loads(Path(path).read_text(encoding="utf-8"))
        if not isinstance(document, dict) or set(document) != {
            "environment", "server_id", "instance_id", "ssh_alias", "unit"
        }:
            raise UnsafeStagingTarget("target configuration must have exactly five identity fields")
        if any(not isinstance(value, str) for value in document.values()):
            raise UnsafeStagingTarget("staging target fields must all be strings")
        target = cls(**document)
        target.validate()
        return target

    def validate(self):
        if self.environment != "staging" or self.server_id in PROD_NAMES:
            raise UnsafeStagingTarget("production targets are forbidden")
        if not SERVER.fullmatch(self.server_id) or not UUID.fullmatch(self.instance_id):
            raise UnsafeStagingTarget("requires isolated recovery-test server and Vultr UUID")
        if not ALIAS.fullmatch(self.ssh_alias) or self.ssh_alias.lower().startswith(("prod", "vps")):
            raise UnsafeStagingTarget("unsafe SSH host alias")
        if self.unit != "minecraft-vanilla.service":
            raise UnsafeStagingTarget("unit must match the isolated staging Paper unit")


def ssh(target, remote_command, timeout=30):
    """Executes only locally-defined remote Python code; never interpolates host data."""
    target.validate()
    argv = ["ssh", "-T", "-o", "BatchMode=yes",
            "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=10",
            "--", target.ssh_alias, remote_command]
    result = subprocess.run(argv, capture_output=True, text=True,
                            check=False, timeout=timeout)
    if result.returncode or len(result.stdout) > 8192:
        raise UnsafeStagingTarget("isolated staging SSH command failed")
    try:
        response = json.loads(result.stdout)
    except (TypeError, ValueError) as error:
        raise UnsafeStagingTarget("staging command did not return safe JSON") from error
    if not isinstance(response, dict):
        raise UnsafeStagingTarget("staging response must be an object")
    return response


def observe(target):
    result = ssh(target, "python3 -c " + shlex.quote(REMOTE_OBSERVE))
    if any(result.get(field) != expected for field, expected in (
        ("environment", "staging"), ("purpose", PURPOSE),
        ("server_id", target.server_id), ("instance_id", target.instance_id),
        ("unit", target.unit),
    )):
        raise UnsafeStagingTarget("staging VM identity does not match the configured disposable target")
    probe = result.get("probe", {})
    if not isinstance(probe, dict) or probe.get("status") not in {
        "healthy", "unhealthy", "starting", "stopped", "unknown"
    }:
        raise UnsafeStagingTarget("invalid Paper probe result")
    if not isinstance(result.get("observed_at"), int):
        raise UnsafeStagingTarget("staging observation has no timestamp")
    invocation = probe.get("invocation_id", "")
    if invocation and not INVOCATION.fullmatch(invocation):
        raise UnsafeStagingTarget("invalid systemd runtime identity")
    return result


def verify_recovered(before, after, target):
    """Do not accept TCP reachability or a Done from the old invocation."""
    for observation in (before, after):
        if not isinstance(observation, dict):
            raise UnsafeStagingTarget("recovery evidence must be an observation object")
        for field, value in (("instance_id", target.instance_id),
                             ("server_id", target.server_id),
                             ("unit", target.unit),
                             ("environment", target.environment),
                             ("purpose", PURPOSE)):
            if observation.get(field) != value:
                raise UnsafeStagingTarget("target identity changed during recovery")
    old = before.get("probe")
    new = after.get("probe")
    if not isinstance(old, dict) or not isinstance(new, dict):
        raise UnsafeStagingTarget("recovery evidence has no valid Paper probe")
    if old.get("status") != "healthy" or new.get("status") != "healthy":
        raise UnsafeStagingTarget("Paper was not confirmed ready before and after recovery")
    old_id, new_id = old.get("invocation_id", ""), new.get("invocation_id", "")
    if not INVOCATION.fullmatch(old_id) or not INVOCATION.fullmatch(new_id):
        raise UnsafeStagingTarget("missing Paper runtime identity")
    if old_id == new_id:
        raise UnsafeStagingTarget("no new systemd invocation was observed")
    if after.get("observed_at", 0) <= before.get("observed_at", 0):
        raise UnsafeStagingTarget("stale recovery observation")
    return {"result": "new_invocation_ready", "instance_id":target.instance_id,
            "old_invocation":old_id, "new_invocation":new_id}


def restore(target, source, marker_sha256):
    target.validate()
    if not REMOTE.fullmatch(source) or f"/{target.server_id}/" not in source:
        raise UnsafeStagingTarget("restore source is not the isolated test server's Wasabi prefix")
    if not SHA256.fullmatch(marker_sha256):
        raise UnsafeStagingTarget("expected marker SHA-256 must be 64 lowercase hex chars")
    # A fresh, identity-verified observation is mandatory before any remote copy.
    observe(target)
    args = {"instance_id": target.instance_id, "server_id": target.server_id,
            "source": source, "marker_sha256": marker_sha256}
    payload = base64.urlsafe_b64encode(json.dumps(args).encode("utf-8")).decode("ascii")
    result = ssh(target, "python3 -c " + shlex.quote(REMOTE_RESTORE) + " " +
                 shlex.quote(payload), timeout=960)
    if result.get("result") != "verified_restore_files" or result.get("instance_id") != target.instance_id:
        raise UnsafeStagingTarget("staging remote restore returned no positive integrity evidence")
    if result.get("marker_sha256") != marker_sha256 or result.get("source") != source:
        raise UnsafeStagingTarget("staging restore result does not match exact recovery point")
    return result


def main(argv=None):
    parser = argparse.ArgumentParser(description="Non-destructive live staging recovery verification")
    parser.add_argument("--target", required=True, help="local JSON file naming a disposable staging VM")
    sub = parser.add_subparsers(dest="action", required=True)
    capture = sub.add_parser("observe", help="read metadata and current-invocation Paper health")
    capture.add_argument("--out", help="save redacted observation to a local file")
    ready = sub.add_parser("verify-recovery", help="compare a prior observation with fresh VM readiness")
    ready.add_argument("--before", required=True)
    copy = sub.add_parser("verify-restore", help="copy isolated Wasabi backup into staging scratch")
    copy.add_argument("--source", required=True, help="stagingwasabi:bucket-staging/recovery-staging/server/backup")
    copy.add_argument("--marker-sha256", required=True, help="expected test-world marker SHA-256")
    args = parser.parse_args(argv)
    try:
        target = Target.load(args.target)
        if args.action == "observe":
            result = observe(target)
            if args.out:
                out = Path(args.out)
                fd = os.open(out, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
                with os.fdopen(fd, "w", encoding="utf-8") as stream:
                    json.dump(result, stream, sort_keys=True)
        elif args.action == "verify-recovery":
            before = json.loads(Path(args.before).read_text(encoding="utf-8"))
            result = verify_recovered(before, observe(target), target)
        else:
            result = restore(target, args.source, args.marker_sha256)
        print(json.dumps(result, separators=(",", ":"), sort_keys=True))
        return 0
    except (OSError, ValueError, subprocess.TimeoutExpired) as error:
        # Deliberately do not echo SSH, rclone or token failures; those may contain secrets.
        print("staging validation failed: target, readiness, or backup evidence rejected", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
