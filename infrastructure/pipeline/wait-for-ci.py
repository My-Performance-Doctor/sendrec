"""WaitForGitHubCI step: waits for GitHub CI's recorded result for the release's commit.

ci.yml's last job, on every push to main, signs in to the deployment account through GitHub OIDC
and puts one item in the CI results table: the commit and whether every check passed (design D11).
This step reads that item for the exact commit being released, with read-only access and no GitHub
token. It passes only on a recorded success, fails at once on a recorded failure, and fails when
nothing is recorded within the timeout.
"""
import json
import os
import subprocess
import sys
import time

REGION = "ap-southeast-2"
# A ci.yml run takes about 15 minutes, plus queueing, and its record is written at the end.
TIMEOUT = 60 * 60
INTERVAL = 30
# One read; a hung call is read again instead of holding the step past its own timeout.
READ_TIMEOUT = 60
# Errors no amount of reading again will fix.
PERMANENT_ERRORS = ("AccessDenied", "ResourceNotFound", "ValidationException")


class ReadError(Exception):
    pass


def read_record(commit, table, run=subprocess.run):
    """The item recorded for commit, or None when there is none yet."""
    key = json.dumps({"commit": {"S": commit}})
    try:
        result = run(["aws", "dynamodb", "get-item", "--region", REGION, "--table-name", table, "--key", key,
                      "--consistent-read", "--output", "json"], capture_output=True, text=True, timeout=READ_TIMEOUT)
    except subprocess.TimeoutExpired as error:
        raise ReadError(f"aws did not answer within {READ_TIMEOUT} seconds") from error
    except OSError as error:
        raise ReadError(f"could not run aws: {error}") from error
    if result.returncode != 0:
        raise ReadError(result.stderr.strip() or f"aws exited {result.returncode}")
    try:
        item = json.loads(result.stdout or "{}").get("Item")
    except (ValueError, AttributeError) as error:
        raise ReadError(f"unreadable answer from aws: {error}") from error
    if item is None:
        return None
    return {name: next(iter(value.values())) for name, value in item.items()}


def wait_for_ci(commit, read, clock=time.monotonic, sleep=time.sleep):
    """Returns once commit's recorded result is success; raises SystemExit otherwise."""
    started = clock()
    last_error = None
    while True:
        try:
            record = read(commit)
            last_error = None
        except ReadError as error:
            message = str(error)
            if any(permanent in message for permanent in PERMANENT_ERRORS):
                raise SystemExit(f"Cannot read the CI results table: {message}")
            print(f"Reading the CI result failed, reading again: {message}", file=sys.stderr)
            record, last_error = None, message
        if record is not None:
            conclusion = record.get("conclusion")
            run = record.get("runUrl", "no run recorded")
            if conclusion == "success":
                print(f"GitHub CI passed for {commit}: {run}")
                return
            raise SystemExit(f"GitHub CI recorded {conclusion} for {commit}: {run}")
        if clock() - started > TIMEOUT:
            if last_error is not None:
                raise SystemExit(f"Could not read the CI result for {commit} within {TIMEOUT // 60} minutes:"
                                 f" {last_error}")
            raise SystemExit(f"No GitHub CI result was recorded for {commit} within {TIMEOUT // 60} minutes."
                             " Rerun that commit's push run of ci.yml (a manual dispatch records nothing),"
                             " then retry this stage. A run older than 30 days, or from a commit without"
                             " the record-result job, cannot record: see infrastructure/README.md,"
                             " \"CI result record\".")
        sleep(INTERVAL)


def main():
    with open(os.environ["RELEASE_FILE"]) as stream:
        commit = json.load(stream)["revision"]
    table = os.environ["CI_RESULTS_TABLE"]
    wait_for_ci(commit, lambda c: read_record(c, table))


if __name__ == "__main__":
    sys.exit(main())
