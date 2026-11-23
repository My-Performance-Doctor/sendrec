"""Custom resource: runs the release's migration task once, before the service changes.

Called when the migration task definition revision changes; succeeds only on exit code 0.
Does nothing on delete or during a rollback, when older migrations would meet a newer schema.
"""
import json
import time
import urllib.request

import boto3

ROLLING_BACK = ("UPDATE_ROLLBACK_IN_PROGRESS", "ROLLBACK_IN_PROGRESS")
PHYSICAL_ID = "migration-run"
POLL_SECONDS = 10
DENIED_ATTEMPTS = 7
# Give up a minute before the Lambda limit, so the failure reaches CloudFormation.
MARGIN_MS = 60_000


def respond(event, status, reason):
    body = json.dumps({
        "Status": status, "Reason": reason[:400], "PhysicalResourceId": PHYSICAL_ID,
        "StackId": event["StackId"], "RequestId": event["RequestId"],
        "LogicalResourceId": event["LogicalResourceId"],
    }).encode()
    request = urllib.request.Request(event["ResponseURL"], data=body, method="PUT",
                                     headers={"Content-Type": ""})
    with urllib.request.urlopen(request, timeout=30) as response:
        response.read()


def rolling_back(cloudformation, stack_id):
    stacks = cloudformation.describe_stacks(StackName=stack_id)["Stacks"]
    return stacks[0]["StackStatus"] in ROLLING_BACK


def start(ecs, p, sleep):
    # A denial starts nothing; it waits for IAM to grant the new revision.
    for attempt in range(1, DENIED_ATTEMPTS + 1):
        try:
            return ecs.run_task(
                cluster=p["Cluster"], taskDefinition=p["TaskDefinition"], launchType="FARGATE",
                count=1, startedBy="sendrec-migration-run",
                networkConfiguration={"awsvpcConfiguration": {
                    "subnets": p["Subnets"], "securityGroups": p["SecurityGroups"],
                    "assignPublicIp": "DISABLED"}})
        except Exception as error:
            code = getattr(error, "response", {}).get("Error", {}).get("Code")
            if code != "AccessDeniedException" or attempt == DENIED_ATTEMPTS:
                raise
            sleep(POLL_SECONDS)


def run(ecs, properties, remaining_ms, sleep=None):
    sleep = sleep or time.sleep
    cluster = properties["Cluster"]
    started = start(ecs, properties, sleep)
    tasks = started.get("tasks", [])
    if started.get("failures") or len(tasks) != 1:
        raise RuntimeError("The migration task did not start")
    task = tasks[0]["taskArn"]
    while remaining_ms() > MARGIN_MS:
        sleep(POLL_SECONDS)
        try:  # A failed read is read again; the task is stopped if time runs out.
            found = ecs.describe_tasks(cluster=cluster, tasks=[task]).get("tasks", [])
        except Exception:
            continue
        if len(found) != 1 or found[0].get("lastStatus") != "STOPPED":
            continue
        codes = [c.get("exitCode") for c in found[0].get("containers", [])
                 if c.get("name") == properties["Container"]]
        if codes == [0]:
            return task
        code = codes[0] if len(codes) == 1 and codes[0] is not None else "unknown"
        raise RuntimeError(f"The migration task stopped with exit code {code}: "
                           f"{found[0].get('stoppedReason', 'no reason given')}")
    ecs.stop_task(cluster=cluster, task=task, reason="Migration run out of time")
    raise RuntimeError("The migration task did not finish in time and was stopped")


def handler(event, context):
    try:
        if event["RequestType"] == "Delete":
            respond(event, "SUCCESS", "Nothing to run on delete")
            return
        if rolling_back(boto3.client("cloudformation"), event["StackId"]):
            respond(event, "SUCCESS", "Nothing to run during a rollback")
            return
        task = run(boto3.client("ecs"), event["ResourceProperties"],
                   context.get_remaining_time_in_millis)
        respond(event, "SUCCESS", "Migrations applied by " + task)
    except Exception as error:  # Every failure must reach CloudFormation.
        respond(event, "FAILED", str(error) or type(error).__name__)
