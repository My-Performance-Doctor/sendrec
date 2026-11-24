"""ci.yml's record-result job: the only GitHub path into the deployment account (design D11)."""
import pathlib
import re
import unittest

WORKFLOW = (pathlib.Path(__file__).parents[2] / ".github" / "workflows" / "ci.yml").read_text()


def job(name):
    match = re.search(rf"^  {name}:\n(.*?)(?=^  [a-z0-9-]+:\n|\Z)", WORKFLOW, re.S | re.M)
    if not match:
        raise AssertionError(f"no {name} job")
    return match.group(1)


class RecordJobTest(unittest.TestCase):
    def setUp(self):
        self.job = job("record-result")

    def test_runs_only_on_a_push_to_main_of_this_repository_after_every_check(self):
        condition = re.search(r"^    if: (.*)$", self.job, re.M).group(1)
        for part in ("always()", "github.event_name == 'push'", "github.ref == 'refs/heads/main'",
                     "github.repository == 'My-Performance-Doctor/sendrec'"):
            self.assertIn(part, condition)
        self.assertNotIn("||", condition)
        section = WORKFLOW.split("\njobs:\n", 1)[1]
        jobs = [name for name in re.findall(r"^  ([a-z0-9-]+):\n", section, re.M) if name != "record-result"]
        self.assertEqual(f"    needs: [{', '.join(jobs)}]", re.search(r"^    needs: .*$", self.job, re.M).group(0))

    def test_only_this_job_may_request_an_oidc_token(self):
        self.assertIn("permissions:\n      id-token: write\n      contents: read\n", self.job)
        self.assertEqual(1, WORKFLOW.count("id-token: write"))
        self.assertIn("permissions:\n  contents: read\n", WORKFLOW)

    def test_signs_in_with_a_pinned_action_a_masked_role_and_a_masked_account(self):
        uses = re.findall(r"uses: (\S+)", self.job)
        self.assertEqual(1, len(uses))
        self.assertRegex(uses[0], r"^aws-actions/configure-aws-credentials@[0-9a-f]{40}$")
        # A secret, so the role ARN and its account never appear in the public log.
        self.assertIn("ROLE_ARN: ${{ secrets.SENDREC_CI_RECORD_ROLE_ARN }}", self.job)
        self.assertIn("role-to-assume: ${{ env.ROLE_ARN }}", self.job)
        self.assertNotIn("vars.", self.job)
        self.assertIn("mask-aws-account-id: true", self.job)
        self.assertNotRegex(WORKFLOW, r"\b[0-9]{12}\b", "no account id in the workflow")
        self.assertEqual(1, self.job.count("secrets."))

    def test_records_success_only_when_every_check_succeeded_and_failure_otherwise(self):
        self.assertIn('conclusion = "success" if all(result == "success" for result in checks) else "failure"', self.job)
        for name in ("needs.test.result", "needs.docker-build.result", "needs.e2e.result"):
            self.assertIn(name, self.job)
        self.assertIn("aws dynamodb put-item --table-name sendrec-ci-results", self.job)
        self.assertIn('"commit": {"S": os.environ["GITHUB_SHA"]}', self.job)

    def test_scripts_take_github_values_through_the_environment_only(self):
        for script in re.findall(r"run: \|\n((?:          .*\n|\n)*)", self.job):
            self.assertNotIn("${{", script)


if __name__ == "__main__":
    unittest.main()
