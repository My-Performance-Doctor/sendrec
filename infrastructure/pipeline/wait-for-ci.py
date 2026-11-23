"""WaitForGitHubCI step: waits until GitHub's CI workflow on main passes for the release's commit.

GitHub Actions runs `ci.yml` (the Go, web and end-to-end checks) on every push to main, so the
pipeline deploys staging only once that run has passed, as the staff app's pipeline does. It reads
the runs with a read-only token from $GITHUB_TOKEN and fails on any conclusion but success, or
when no run appears.
"""
import json
import os
import ssl
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

REPOSITORY = "My-Performance-Doctor/sendrec"
WORKFLOW = "ci.yml"
# A run takes about 15 minutes, plus queueing.
TIMEOUT = 60 * 60
# GitHub creates the run within seconds of the push, if it creates one at all.
START_TIMEOUT = 10 * 60
INTERVAL = 30
THROTTLE_WAIT = 60
CA_BUNDLE = "/etc/ssl/certs/ca-certificates.crt"


def latest_run(commit, token, opener=urllib.request.urlopen):
    query = urllib.parse.urlencode({"head_sha": commit, "branch": "main", "event": "push", "per_page": 1})
    request = urllib.request.Request(
        f"https://api.github.com/repos/{REPOSITORY}/actions/workflows/{WORKFLOW}/runs?{query}",
        headers={"Accept": "application/vnd.github+json", "Authorization": f"Bearer {token}",
                 "X-GitHub-Api-Version": "2022-11-28"})
    context = ssl.create_default_context(cafile=CA_BUNDLE if os.path.exists(CA_BUNDLE) else None)
    with opener(request, timeout=30, context=context) as response:
        runs = json.load(response)["workflow_runs"]
    return runs[0] if runs else None


def throttle_wait(error):
    """Seconds GitHub asks us to wait, or None when the error is not throttling."""
    if error.code not in (403, 429):
        return None
    headers = error.headers or {}
    retry_after = headers.get("Retry-After", "")
    if retry_after.isdigit():
        return float(retry_after)
    if headers.get("X-RateLimit-Remaining") == "0":
        reset = headers.get("X-RateLimit-Reset", "")
        return max(0.0, int(reset) - time.time()) if reset.isdigit() else THROTTLE_WAIT
    return THROTTLE_WAIT if error.code == 429 else None


def wait_for_ci(commit, token, read=latest_run, clock=time.monotonic, sleep=time.sleep):
    """Returns once the commit's CI run succeeded; raises SystemExit otherwise."""
    started = clock()
    while True:
        elapsed = clock() - started
        if elapsed > TIMEOUT:
            raise SystemExit(f"GitHub CI did not finish for {commit} within {TIMEOUT // 60} minutes")
        try:
            run = read(commit, token)
        except urllib.error.HTTPError as error:
            wait = throttle_wait(error)
            if wait is None:
                raise SystemExit(f"GitHub answered {error.code} reading CI runs; check the token")
            sleep(min(wait, 300))
            continue
        if run is None:
            if elapsed > START_TIMEOUT:
                raise SystemExit(f"GitHub started no {WORKFLOW} run on main for {commit}")
        elif run.get("status") == "completed":
            if run.get("conclusion") == "success":
                print(f"GitHub CI passed for {commit}: {run.get('html_url')}")
                return
            raise SystemExit(f"GitHub CI ended {run.get('conclusion')} for {commit}: {run.get('html_url')}")
        sleep(INTERVAL)


def main():
    with open(os.environ["RELEASE_FILE"]) as stream:
        commit = json.load(stream)["revision"]
    wait_for_ci(commit, os.environ["GITHUB_TOKEN"])


if __name__ == "__main__":
    sys.exit(main())
