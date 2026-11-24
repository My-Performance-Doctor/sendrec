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


class ReadError(Exception):
    pass


def read_record(commit, table, run=subprocess.run):
    """The item recorded for commit, or None when there is none yet."""
    key = json.dumps({"commit": {"S": commit}})
    result = run(["aws", "dynamodb", "get-item", "--region", REGION, "--table-name", table, "--key", key,
                  "--consistent-read", "--output", "json"], capture_output=True, text=True)
    if result.returncode != 0:
        raise ReadError(result.stderr.strip() or f"aws exited {result.returncode}")
    item = json.loads(result.stdout or "{}").get("Item")
    if item is None:
        return None
    return {name: next(iter(value.values())) for name, value in item.items()}


def wait_for_ci(commit, read, clock=time.monotonic, sleep=time.sleep):
    """Returns once commit's recorded result is success; raises SystemExit otherwise."""
    started = clock()
    while True:
        try:
            record = read(commit)
        except ReadError as error:
            message = str(error)
            if "AccessDenied" in message or "ResourceNotFound" in message:
                raise SystemExit(f"Cannot read the CI results table: {message}")
            print(f"Reading the CI result failed, reading again: {message}", file=sys.stderr)
            record = None
        if record is not None:
            conclusion = record.get("conclusion")
            run = record.get("runUrl", "no run recorded")
            if conclusion == "success":
                print(f"GitHub CI passed for {commit}: {run}")
                return
            raise SystemExit(f"GitHub CI recorded {conclusion} for {commit}: {run}")
        if clock() - started > TIMEOUT:
            raise SystemExit(f"No GitHub CI result was recorded for {commit} within {TIMEOUT // 60} minutes."
                             " Rerun ci.yml for that commit on main, then retry this stage.")
        sleep(INTERVAL)


def main():
    with open(os.environ["RELEASE_FILE"]) as stream:
        commit = json.load(stream)["revision"]
    table = os.environ["CI_RESULTS_TABLE"]
    wait_for_ci(commit, lambda c: read_record(c, table))


if __name__ == "__main__":
    sys.exit(main())
