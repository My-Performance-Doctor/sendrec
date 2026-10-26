#!/usr/bin/env python3
"""Promote already-reviewed ECS definitions, preserving state on rollback.

No infrastructure creation, secret values, build tags or database reversal here.
AWS access uses the caller's short-lived environment credentials.
"""
import argparse
import json
import hashlib
import os
import pathlib
import re
import subprocess
import time
import urllib.request
import urllib.parse


IMAGE = re.compile(r"[0-9]{12}\.dkr\.ecr\.ap-southeast-2\.amazonaws\.com/[a-z0-9/_-]+@sha256:[a-f0-9]{64}")


class ReleaseFailure(RuntimeError):
    """A release outcome in this script's own fixed words, safe to print."""


def aws(*args):
    result = subprocess.run(["aws", *args, "--output", "json"], check=True, capture_output=True, text=True)
    return json.loads(result.stdout or "{}")


def validate(manifest, target, staged=None):
    if not re.fullmatch(r"[a-f0-9]{40}", manifest["revision"]):
        raise ValueError("A full source revision is required")
    image = manifest["image"]
    if not IMAGE.fullmatch(image):
        raise ValueError("A Sydney ECR image digest is required")
    if not re.fullmatch(r"[a-f0-9]{64}", manifest["infrastructureSha256"]):
        raise ValueError("The reviewed infrastructure digest is required")
    config = manifest[target]
    target_image(manifest, target)
    if config.get("desiredCount") != (2 if target == "production" else 1):
        raise ValueError("Only one staging or two approved production tasks are supported")
    parsed = urllib.parse.urlparse(config["readinessUrl"])
    if parsed.scheme != "https" or parsed.username or parsed.password or parsed.query or parsed.fragment or parsed.path != "/api/ready":
        raise ValueError("Readiness requires an exact HTTPS /api/ready URL")
    if target == "production":
        if staged is None or not staged.get("verified") or staged.get("target") != "staging":
            raise ValueError("Verified staging evidence is required")
        for field in ("image", "revision", "infrastructureSha256"):
            if staged.get(field) != manifest[field]:
                raise ValueError("Staging evidence belongs to a different release")
        for key in ("cluster", "service", "migrationTaskDefinition", "applicationTaskDefinition"):
            if config[key] == manifest["staging"][key]:
                raise ValueError("Production and staging resources must differ")
    return config


def target_image(manifest, target):
    """The image this environment's task definitions name.

    Production runs in its own account, so it pulls a copy from its own registry. The copy may
    name another account and repository but must carry the reviewed digest. Staging runs the
    top-level image itself, because that is the image its evidence vouches for.
    """
    if target == "staging" and manifest["staging"].get("image", manifest["image"]) != manifest["image"]:
        raise ValueError("Staging must run the top-level reviewed image")
    image = manifest[target].get("image", manifest["image"])
    if not IMAGE.fullmatch(image):
        raise ValueError("A Sydney ECR image digest is required")
    if image.split("@", 1)[1] != manifest["image"].split("@", 1)[1]:
        raise ValueError("Every environment must run the reviewed image digest")
    return image


def check_infrastructure(manifest, target):
    templates = []
    for environment in ("staging", "production"):
        templates.append(json.loads(pathlib.Path(manifest[environment]["templatePath"]).read_text()))
    serialized = json.dumps(templates, sort_keys=True, separators=(",", ":")).encode()
    if hashlib.sha256(serialized).hexdigest() != manifest["infrastructureSha256"]:
        raise ValueError("Reviewed infrastructure templates differ from the manifest digest")
    live = aws("cloudformation", "get-template", "--stack-name", manifest[target]["stack"], "--template-stage", "Original")["TemplateBody"]
    if isinstance(live, str):
        live = json.loads(live)
    if live != templates[0 if target == "staging" else 1]:
        raise ValueError("Live stack differs from the reviewed template")


def check_definitions(config, image):
    for key, mode in (("applicationTaskDefinition", "skip"), ("migrationTaskDefinition", "only")):
        definition = aws("ecs", "describe-task-definition", "--task-definition", config[key])["taskDefinition"]
        containers = definition["containerDefinitions"]
        if len(containers) != 1 or containers[0]["image"] != image:
            raise ValueError("Task definition must contain the reviewed immutable image only")
        environment = {item["name"]: item["value"] for item in containers[0].get("environment", [])}
        if environment.get("MIGRATIONS_MODE") != mode:
            raise ValueError("Task migration mode differs from the release contract")


def readiness(url):
    try:
        with urllib.request.urlopen(url, timeout=5) as response:
            result = json.load(response)
            return response.status == 200 and result.get("status") == "ready" and result.get("checks") == {"database": True, "storage": True, "workers": True}
    except Exception:
        return False


def promote(manifest, target, staged=None, probe=readiness):
    config = validate(manifest, target, staged)
    check_infrastructure(manifest, target)
    image = target_image(manifest, target)
    registry = image.split(".", 1)[0]
    repository, digest = image.split("/", 1)[1].split("@", 1)
    # Ask the registry the image names, not whichever account the caller's credentials default to.
    policy = aws("ecr", "describe-repositories", "--registry-id", registry, "--repository-names", repository)["repositories"][0]
    if policy.get("imageTagMutability") != "IMMUTABLE":
        raise ValueError("The release repository must enforce immutable tags")
    image_detail = aws("ecr", "describe-images", "--registry-id", registry, "--repository-name", repository,
                       "--image-ids", "imageDigest=" + digest)["imageDetails"][0]
    if manifest["revision"] not in image_detail.get("imageTags", []):
        raise ValueError("The image digest is not bound to the reviewed source revision")
    check_definitions(config, image)
    previous = aws("ecs", "describe-services", "--cluster", config["cluster"], "--services", config["service"])["services"][0]
    old_definition, old_count = previous["taskDefinition"], previous["desiredCount"]
    # A stable token makes a transport retry of this exact migration run safe.
    token = "sendrec-" + target + "-" + manifest["revision"]
    started = aws("ecs", "run-task", "--cluster", config["cluster"], "--task-definition", config["migrationTaskDefinition"],
                  "--launch-type", "FARGATE", "--count", "1", "--client-token", token,
                  "--network-configuration", json.dumps({"awsvpcConfiguration": {
                      "subnets": config["subnets"], "securityGroups": config["securityGroups"], "assignPublicIp": "DISABLED"}}))
    if started.get("failures") or len(started.get("tasks", [])) != 1:
        raise ReleaseFailure("Single migration task failed to start")
    task = started["tasks"][0]["taskArn"]
    try:
        aws("ecs", "wait", "tasks-stopped", "--cluster", config["cluster"], "--tasks", task)
    except subprocess.CalledProcessError:
        # The waiter fails on its ten-minute limit and on any AWS error, so the task's state is unknown.
        raise ReleaseFailure("Migration was not confirmed finished and may still be running; application was not changed") from None
    stopped = aws("ecs", "describe-tasks", "--cluster", config["cluster"], "--tasks", task)["tasks"][0]
    if len(stopped.get("containers", [])) != 1 or stopped["containers"][0].get("exitCode") != 0:
        raise ReleaseFailure("Migration failed; application was not changed")
    try:
        aws("ecs", "update-service", "--cluster", config["cluster"], "--service", config["service"],
            "--task-definition", config["applicationTaskDefinition"], "--desired-count", str(config["desiredCount"]))
        aws("ecs", "wait", "services-stable", "--cluster", config["cluster"], "--services", config["service"])
        live = aws("ecs", "describe-services", "--cluster", config["cluster"], "--services", config["service"])["services"][0]
        if live["taskDefinition"] != config["applicationTaskDefinition"] or live["runningCount"] != config["desiredCount"]:
            raise RuntimeError("ECS did not retain the requested release")
        if not all(probe(config["readinessUrl"]) for _ in range(3)):
            raise RuntimeError("Release readiness failed")
    except Exception:
        try:
            aws("ecs", "update-service", "--cluster", config["cluster"], "--service", config["service"],
                "--task-definition", old_definition, "--desired-count", str(old_count))
            aws("ecs", "wait", "services-stable", "--cluster", config["cluster"], "--services", config["service"])
            rolled_back = aws("ecs", "describe-services", "--cluster", config["cluster"], "--services", config["service"])["services"][0]
            restored = (rolled_back["taskDefinition"] == old_definition and rolled_back["runningCount"] == old_count
                        and (not old_count or probe(config["readinessUrl"])))
        except Exception:
            # A rollback call that fails or times out is an unconfirmed rollback.
            restored = False
        if not restored:
            raise ReleaseFailure("Rollback needs operator recovery; state was retained") from None
        raise ReleaseFailure("Release failed; prior application restored; migration and media retained") from None
    return {"target": target, "verified": True, "image": manifest["image"], "deployedImage": image, "revision": manifest["revision"],
            "infrastructureSha256": manifest["infrastructureSha256"], "verifiedAt": int(time.time())}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--manifest", required=True, type=pathlib.Path)
    parser.add_argument("--target", required=True, choices=("staging", "production"))
    parser.add_argument("--staging-evidence", type=pathlib.Path)
    parser.add_argument("--output", required=True, type=pathlib.Path)
    args = parser.parse_args()
    manifest = json.loads(args.manifest.read_text())
    validate(manifest, args.target, json.loads(args.staging_evidence.read_text()) if args.staging_evidence else None)
    head = os.environ.get("GITHUB_SHA", "")
    if not re.fullmatch(r"[a-f0-9]{40}", head):
        raise ValueError("A trusted workflow revision is required")
    subprocess.run(["git", "merge-base", "--is-ancestor", manifest["revision"], head], check=True, capture_output=True)
    staged = json.loads(args.staging_evidence.read_text()) if args.staging_evidence else None
    result = promote(manifest, args.target, staged)
    args.output.write_text(json.dumps(result, indent=2) + "\n")


def run():
    try:
        main()
    except ReleaseFailure as failure:
        # Say whether the prior application is back, so the operator knows if recovery is theirs.
        raise SystemExit(f"Release did not pass: {failure}.") from None
    except Exception:
        # AWS diagnostics may contain resource identifiers; the runbook covers private investigation.
        raise SystemExit("Release did not pass. Inspect private ECS deployment and migration status; no state was deleted.") from None


if __name__ == "__main__":
    run()
