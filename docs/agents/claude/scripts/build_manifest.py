"""Build a staging-only release manifest from live AWS state. Prints references, never credentials.

Usage: AWS_PROFILE=mpd-staging python3 -I build_manifest.py <revision> <image> <reviewed-template.json> <manifest-out.json>
"""
import hashlib
import json
import subprocess
import sys

revision, image, template_path, out = sys.argv[1:5]


def aws(*args):
    return json.loads(subprocess.check_output(["aws", *args, "--region", "ap-southeast-2", "--output", "json"]))


resources = aws("cloudformation", "describe-stack-resources", "--stack-name", "SendRecStaging")["StackResources"]
by_type = {}
for r in resources:
    by_type.setdefault(r["ResourceType"], []).append(r)
definitions = {r["LogicalResourceId"]: r["PhysicalResourceId"] for r in by_type["AWS::ECS::TaskDefinition"]}
application = next(v for k, v in definitions.items() if k.startswith("TaskDefinition"))
migration = next(v for k, v in definitions.items() if k.startswith("MigrationTaskDefinition"))
service_arn = by_type["AWS::ECS::Service"][0]["PhysicalResourceId"]
cluster_arn = by_type["AWS::ECS::Cluster"][0]["PhysicalResourceId"]
if not cluster_arn.startswith("arn:"):
    cluster_arn = aws("ecs", "describe-clusters", "--clusters", cluster_arn)["clusters"][0]["clusterArn"]
service = aws("ecs", "describe-services", "--cluster", cluster_arn, "--services", service_arn)["services"][0]
network = service["networkConfiguration"]["awsvpcConfiguration"]
template = json.load(open(template_path))
digest = hashlib.sha256(json.dumps([template], sort_keys=True, separators=(",", ":")).encode()).hexdigest()
manifest = {
    "revision": revision,
    "image": image,
    "infrastructureSha256": digest,
    "staging": {
        "stack": "SendRecStaging",
        "templatePath": template_path,
        "cluster": cluster_arn,
        "service": service["serviceArn"],
        "applicationTaskDefinition": application,
        "migrationTaskDefinition": migration,
        "subnets": network["subnets"],
        "securityGroups": network["securityGroups"],
        "readinessUrl": "https://d2oopdll9i8yvf.cloudfront.net/api/ready",
        "desiredCount": 1,
    },
    "production": {},
}
json.dump(manifest, open(out, "w"), indent=2)
print("application:", application.rsplit("/", 1)[1], "migration:", migration.rsplit("/", 1)[1],
      "service on:", service["taskDefinition"].rsplit("/", 1)[1], "template sha256:", digest)
