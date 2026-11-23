import importlib.util
import io
import json
import pathlib
import unittest
import urllib.error

spec = importlib.util.spec_from_file_location("wait_for_ci", pathlib.Path(__file__).parents[1] / "pipeline" / "wait-for-ci.py")
ci = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ci)
COMMIT = "0123456789abcdef0123456789abcdef01234567"


class Clock:
    def __init__(self):
        self.now = 0.0
        self.slept = []

    def __call__(self):
        return self.now

    def sleep(self, seconds):
        self.slept.append(seconds)
        self.now += seconds


def reads(*answers):
    answers = list(answers)

    def read(commit, token):
        assert (commit, token) == (COMMIT, "token")
        answer = answers.pop(0) if len(answers) > 1 else answers[0]
        if isinstance(answer, Exception):
            raise answer
        return answer
    return read


def http_error(code, headers=None):
    return urllib.error.HTTPError("https://api.github.com", code, "error", headers or {}, io.BytesIO(b""))


class WaitForCiTest(unittest.TestCase):
    def wait(self, *answers):
        clock = Clock()
        ci.wait_for_ci(COMMIT, "token", read=reads(*answers), clock=clock, sleep=clock.sleep)
        return clock

    def test_passes_once_the_run_succeeds(self):
        clock = self.wait(None, {"status": "in_progress"}, {"status": "completed", "conclusion": "success", "html_url": "u"})
        self.assertEqual([ci.INTERVAL, ci.INTERVAL], clock.slept)

    def test_any_other_conclusion_fails(self):
        for conclusion in ("failure", "cancelled", "timed_out", "skipped", None):
            with self.subTest(conclusion=conclusion), self.assertRaisesRegex(SystemExit, "GitHub CI ended"):
                self.wait({"status": "completed", "conclusion": conclusion})

    def test_no_run_within_ten_minutes_fails(self):
        with self.assertRaisesRegex(SystemExit, "started no ci.yml run"):
            self.wait(None)

    def test_a_run_that_never_finishes_fails_at_the_timeout(self):
        with self.assertRaisesRegex(SystemExit, "did not finish"):
            self.wait({"status": "queued"})

    def test_throttling_waits_and_a_bad_token_fails(self):
        clock = self.wait(http_error(429, {"Retry-After": "7"}), {"status": "completed", "conclusion": "success"})
        self.assertEqual([7.0], clock.slept)
        with self.assertRaisesRegex(SystemExit, "answered 401"):
            self.wait(http_error(401))
        with self.assertRaisesRegex(SystemExit, "answered 404"):
            self.wait(http_error(404))

    def test_server_errors_and_network_failures_are_read_again(self):
        clock = self.wait(http_error(502), urllib.error.URLError("reset"), TimeoutError("slow"),
                          {"status": "completed", "conclusion": "success"})
        self.assertEqual([ci.INTERVAL] * 3, clock.slept)
        with self.assertRaisesRegex(SystemExit, "did not finish"):
            self.wait(http_error(503))

    def test_the_request_names_the_commit_on_main_and_sends_the_token(self):
        seen = {}

        class Response(io.BytesIO):
            def __enter__(self):
                return self

            def __exit__(self, *args):
                return False

        def opener(request, timeout, context):
            seen["url"], seen["auth"] = request.full_url, request.get_header("Authorization")
            return Response(json.dumps({"workflow_runs": [{"id": 1}]}).encode())

        self.assertEqual({"id": 1}, ci.latest_run(COMMIT, "token", opener=opener))
        self.assertIn("/repos/My-Performance-Doctor/sendrec/actions/workflows/ci.yml/runs?", seen["url"])
        self.assertIn(f"head_sha={COMMIT}", seen["url"])
        self.assertIn("branch=main", seen["url"])
        self.assertIn("event=push", seen["url"])
        self.assertEqual("Bearer token", seen["auth"])


if __name__ == "__main__":
    unittest.main()
