import importlib.util
import json
import pathlib
import subprocess
import unittest

spec = importlib.util.spec_from_file_location("wait_for_ci", pathlib.Path(__file__).parents[1] / "pipeline" / "wait-for-ci.py")
ci = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ci)
COMMIT = "0123456789abcdef0123456789abcdef01234567"
OLDER = "89abcdef0123456789abcdef0123456789abcdef"
NEWER = "fedcba9876543210fedcba9876543210fedcba98"


class Clock:
    def __init__(self):
        self.now = 0.0
        self.slept = []

    def __call__(self):
        return self.now

    def sleep(self, seconds):
        self.slept.append(seconds)
        self.now += seconds


class Table:
    """The results table: one item per commit, with items appearing after a number of reads."""

    def __init__(self, records, appear_after=0, errors=()):
        self.records, self.appear_after, self.errors, self.reads = records, appear_after, list(errors), []

    def __call__(self, commit):
        self.reads.append(commit)
        if self.errors:
            raise ci.ReadError(self.errors.pop(0))
        if len(self.reads) <= self.appear_after:
            return None
        return self.records.get(commit)


def record(conclusion, commit=COMMIT):
    return {"commit": commit, "conclusion": conclusion, "runUrl": f"https://github.com/run/{commit[:4]}"}


class WaitForCiTest(unittest.TestCase):
    def wait(self, table, commit=COMMIT):
        clock = Clock()
        ci.wait_for_ci(commit, table, clock=clock, sleep=clock.sleep)
        return clock

    def test_passes_on_a_recorded_success_for_the_commit(self):
        table = Table({COMMIT: record("success")}, appear_after=2)
        clock = self.wait(table)
        self.assertEqual([COMMIT] * 3, table.reads)
        self.assertEqual([ci.INTERVAL, ci.INTERVAL], clock.slept)

    def test_fails_at_once_on_a_recorded_failure(self):
        for conclusion in ("failure", "cancelled", None):
            table = Table({COMMIT: record(conclusion)})
            with self.subTest(conclusion=conclusion), self.assertRaisesRegex(SystemExit, "GitHub CI recorded"):
                self.wait(table)
            self.assertEqual(1, len(table.reads))

    def test_an_older_commit_finds_its_own_result_not_the_newest(self):
        # The nightly release ships an older staging-verified run after newer merges recorded theirs.
        table = Table({OLDER: record("success", OLDER), NEWER: record("failure", NEWER)})
        self.wait(table, OLDER)
        self.assertEqual([OLDER], table.reads)
        with self.assertRaisesRegex(SystemExit, "recorded failure"):
            self.wait(Table({OLDER: record("failure", OLDER), NEWER: record("success", NEWER)}), OLDER)

    def test_a_success_recorded_only_for_another_commit_never_passes(self):
        with self.assertRaisesRegex(SystemExit, "No GitHub CI result was recorded"):
            self.wait(Table({NEWER: record("success", NEWER)}))

    def test_nothing_recorded_within_the_timeout_fails(self):
        clock = Clock()
        with self.assertRaisesRegex(SystemExit, f"within {ci.TIMEOUT // 60} minutes"):
            ci.wait_for_ci(COMMIT, Table({}), clock=clock, sleep=clock.sleep)
        self.assertGreater(clock.now, ci.TIMEOUT - ci.INTERVAL)

    def test_a_passing_read_error_is_read_again_and_a_permission_error_fails(self):
        self.wait(Table({COMMIT: record("success")}, errors=["ThrottlingException", "Could not connect"]))
        for error in ("AccessDeniedException: not authorized", "ResourceNotFoundException: no table"):
            with self.subTest(error=error), self.assertRaisesRegex(SystemExit, "Cannot read the CI results table"):
                self.wait(Table({COMMIT: record("success")}, errors=[error]))


    def test_an_error_that_lasts_to_the_timeout_is_named(self):
        table = Table({}, errors=["Unable to locate credentials"] * 1000)
        with self.assertRaisesRegex(SystemExit, "Could not read the CI result .*Unable to locate credentials"):
            self.wait(table)


class ReadRecordTest(unittest.TestCase):
    def test_reads_one_item_by_exact_commit_consistently(self):
        seen = {}

        def run(command, capture_output, text):
            seen["command"] = command
            item = {"commit": {"S": COMMIT}, "conclusion": {"S": "success"}, "runAttempt": {"N": "2"}}
            return subprocess.CompletedProcess(command, 0, json.dumps({"Item": item}), "")

        self.assertEqual({"commit": COMMIT, "conclusion": "success", "runAttempt": "2"},
                         ci.read_record(COMMIT, "sendrec-ci-results", run=run))
        command = seen["command"]
        self.assertEqual(["aws", "dynamodb", "get-item"], command[:3])
        self.assertIn("--consistent-read", command)
        self.assertEqual("sendrec-ci-results", command[command.index("--table-name") + 1])
        self.assertEqual({"commit": {"S": COMMIT}}, json.loads(command[command.index("--key") + 1]))

    def test_no_item_is_none_and_a_failed_call_raises(self):
        self.assertIsNone(ci.read_record(COMMIT, "t", run=lambda c, **k: subprocess.CompletedProcess(c, 0, "{}", "")))
        with self.assertRaisesRegex(ci.ReadError, "AccessDenied"):
            ci.read_record(COMMIT, "t", run=lambda c, **k: subprocess.CompletedProcess(c, 254, "", "AccessDeniedException"))
        with self.assertRaisesRegex(ci.ReadError, "unreadable answer"):
            ci.read_record(COMMIT, "t", run=lambda c, **k: subprocess.CompletedProcess(c, 0, "{cut", ""))


if __name__ == "__main__":
    unittest.main()
