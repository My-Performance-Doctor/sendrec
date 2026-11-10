"""Run infrastructure/promote-release.py's promote() for staging only.

The production half of the reviewed template pair needs the private grant-sheet inputs, which this
host does not have. Everything else is the repository script unchanged: immutable repository and
revision-tag binding, task definition image and migration-mode checks, one migration task, service
update, stability, task count, three readiness probes and rollback on failure. The only replaced
check compares the live staging template with the synthesized staging template, exactly.

Usage: python3 -I promote_staging.py <repo-root> <manifest.json> <evidence-out.json>
"""
import hashlib
import importlib.util
import json
import pathlib
import subprocess
import sys

root, manifest_path, out_path = map(pathlib.Path, sys.argv[1:4])
spec = importlib.util.spec_from_file_location("promote_release", root / "infrastructure" / "promote-release.py")
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


def check_staging_template(manifest, target):
    assert target == "staging"
    reviewed = json.loads(pathlib.Path(manifest["staging"]["templatePath"]).read_text())
    serialized = json.dumps([reviewed], sort_keys=True, separators=(",", ":")).encode()
    if hashlib.sha256(serialized).hexdigest() != manifest["infrastructureSha256"]:
        raise ValueError("Reviewed staging template differs from the manifest digest")
    live = release.aws("cloudformation", "get-template", "--stack-name", manifest["staging"]["stack"],
                       "--template-stage", "Original")["TemplateBody"]
    if isinstance(live, str):
        live = json.loads(live)
    if live != reviewed:
        raise ValueError("Live stack differs from the reviewed template")


release.check_infrastructure = check_staging_template
manifest = json.loads(manifest_path.read_text())
subprocess.run(["git", "-C", str(root), "merge-base", "--is-ancestor", manifest["revision"], "origin/main"], check=True)
try:
    result = release.promote(manifest, "staging")
except release.ReleaseFailure as failure:
    raise SystemExit(f"Release did not pass: {failure}.") from None
out_path.write_text(json.dumps(result, indent=2) + "\n")
print(json.dumps(result, indent=2))
