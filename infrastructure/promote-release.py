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


def aws(*args):
    result = subprocess.run(["aws", *args, "--output", "json"], check=True, capture_output=True, text=True)
    return json.loads(result.stdout or "{}")


def validate(manifest, target, staged=None):
    if not re.fullmatch(r"[a-f0-9]{40}", manifest["revision"]):
        raise ValueError("A full source revision is required")
    image = manifest["image"]
    if not re.fullmatch(r"[0-9]{12}\.dkr\.ecr\.ap-southeast-2\.amazonaws\.com/[a-z0-9/_-]+@sha256:[a-f0-9]{64}", image):
        raise ValueError("A Sydney ECR image digest is required")
    if not re.fullmatch(r"[a-f0-9]{64}", manifest["infrastructureSha256"]):
        raise ValueError("The reviewed infrastructure digest is required")
    config = manifest[target]
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
    image_parts = manifest["image"].split("/", 1)[1].split("@", 1)
    repository, digest = image_parts
    policy = aws("ecr", "describe-repositories", "--repository-names", repository)["repositories"][0]
    if policy.get("imageTagMutability") != "IMMUTABLE":
        raise ValueError("The release repository must enforce immutable tags")
    image_detail = aws("ecr", "describe-images", "--repository-name", repository, "--image-ids", "imageDigest=" + digest)["imageDetails"][0]
    if manifest["revision"] not in image_detail.get("imageTags", []):
        raise ValueError("The image digest is not bound to the reviewed source revision")
    check_definitions(config, manifest["image"])
    previous = aws("ecs", "describe-services", "--cluster", config["cluster"], "--services", config["service"])["services"][0]
    old_definition, old_count = previous["taskDefinition"], previous["desiredCount"]
    # A stable token makes a transport retry of this exact migration run safe.
    token = "sendrec-" + target + "-" + manifest["revision"]
    started = aws("ecs", "run-task", "--cluster", config["cluster"], "--task-definition", config["migrationTaskDefinition"],
                  "--launch-type", "FARGATE", "--count", "1", "--client-token", token,
                  "--network-configuration", json.dumps({"awsvpcConfiguration": {
                      "subnets": config["subnets"], "securityGroups": config["securityGroups"], "assignPublicIp": "DISABLED"}}))
    if started.get("failures") or len(started.get("tasks", [])) != 1:
        raise RuntimeError("Single migration task failed to start")
    task = started["tasks"][0]["taskArn"]
    aws("ecs", "wait", "tasks-stopped", "--cluster", config["cluster"], "--tasks", task)
    stopped = aws("ecs", "describe-tasks", "--cluster", config["cluster"], "--tasks", task)["tasks"][0]
    if len(stopped.get("containers", [])) != 1 or stopped["containers"][0].get("exitCode") != 0:
        raise RuntimeError("Migration failed; application was not changed")
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
        aws("ecs", "update-service", "--cluster", config["cluster"], "--service", config["service"],
            "--task-definition", old_definition, "--desired-count", str(old_count))
        aws("ecs", "wait", "services-stable", "--cluster", config["cluster"], "--services", config["service"])
        rolled_back = aws("ecs", "describe-services", "--cluster", config["cluster"], "--services", config["service"])["services"][0]
        if rolled_back["taskDefinition"] != old_definition or rolled_back["runningCount"] != old_count or (old_count and not probe(config["readinessUrl"])):
            raise RuntimeError("Rollback needs operator recovery; state was retained") from None
        raise RuntimeError("Release failed; prior application restored; migration and media retained") from None
    return {"target": target, "verified": True, "image": manifest["image"], "revision": manifest["revision"],
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


if __name__ == "__main__":
    try:
        main()
    except Exception:
        # AWS diagnostics may contain resource identifiers; the runbook covers private investigation.
        raise SystemExit("Release did not pass. Inspect private ECS deployment and migration status; no state was deleted.")
