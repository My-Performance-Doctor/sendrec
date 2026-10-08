"""Read RDS recovery metadata and emit a numeric metric without identifiers."""
import datetime
import json
import os

import boto3


def backup_age(instance, now):
    latest = instance.get("LatestRestorableTime")
    if not latest or instance.get("BackupRetentionPeriod", 0) < 35:
        return 86400
    return max(0, int((now - latest).total_seconds()))


def service_desired_count(ecs):
    try:
        result = ecs.describe_services(
            cluster=os.environ["CLUSTER_ARN"], services=[os.environ["SERVICE_ARN"]]
        )
        if result.get("failures") or len(result.get("services", [])) != 1:
            return 1
        count = result["services"][0]["desiredCount"]
        if not isinstance(count, int) or isinstance(count, bool) or count < 0:
            return 1
        return count
    except Exception:
        # Unknown activity must fail closed, not silence a stopped application.
        return 1


def handler(event, context):
    try:
        instances = boto3.client("rds").describe_db_instances(
            DBInstanceIdentifier=os.environ["DATABASE_IDENTIFIER"]
        )["DBInstances"]
        age = backup_age(instances[0], datetime.datetime.now(datetime.timezone.utc))
    except Exception:
        # SDK errors may include resource or request metadata. Emit no error text.
        age = 86400
    try:
        desired = service_desired_count(boto3.client("ecs"))
    except Exception:
        desired = 1
    result = {"BackupAgeSeconds": age, "ServiceDesiredCount": desired}
    print(json.dumps(result))
    return result
