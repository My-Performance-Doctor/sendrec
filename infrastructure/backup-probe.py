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


def handler(event, context):
    try:
        instances = boto3.client("rds").describe_db_instances(
            DBInstanceIdentifier=os.environ["DATABASE_IDENTIFIER"]
        )["DBInstances"]
        age = backup_age(instances[0], datetime.datetime.now(datetime.timezone.utc))
    except Exception:
        # SDK errors may include resource or request metadata. Emit no error text.
        age = 86400
    print(json.dumps({"BackupAgeSeconds": age}))
    return {"BackupAgeSeconds": age}
