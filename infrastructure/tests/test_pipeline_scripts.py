"""The pipeline's shell steps, run against stub aws, docker, crane and curl commands."""
import json
import os
import pathlib
import subprocess
import tempfile
import unittest

SCRIPTS = pathlib.Path(__file__).parents[1] / "pipeline"
REVISION = "0123456789abcdef0123456789abcdef01234567"
DIGEST = "sha256:" + "a" * 64
OTHER = "sha256:" + "b" * 64
STAGING = "537421187871.dkr.ecr.ap-southeast-2.amazonaws.com"
PRODUCTION = "111122223333.dkr.ecr.ap-southeast-2.amazonaws.com"

# Each stub logs its arguments to calls.log and answers from files the test writes.
AWS = """#!/bin/sh
echo "aws $*" >> "$STUBS/calls.log"
case "$*" in
  *get-login-password*) echo token ;;
  *describe-images*)
    if [ -f "$STUBS/pushed" ]; then cat "$STUBS/pushed"; exit 0; fi
    if [ -f "$STUBS/existing" ]; then cat "$STUBS/existing"; exit 0; fi
    cat "$STUBS/describe-error" >&2; exit 254 ;;
esac
"""
DOCKER = """#!/bin/sh
echo "docker $*" >> "$STUBS/calls.log"
case "$1" in
  login) cat > /dev/null ;;
  push) cp "$STUBS/push-digest" "$STUBS/pushed" ;;
esac
"""
CRANE = """#!/bin/sh
echo "crane $*" >> "$STUBS/calls.log"
case "$1" in
  auth) cat > /dev/null ;;
  digest)
    if [ -f "$STUBS/copied" ]; then cat "$STUBS/copied"; exit 0; fi
    if [ -f "$STUBS/production-tag" ]; then cat "$STUBS/production-tag"; exit 0; fi
    echo "MANIFEST_UNKNOWN" >&2; exit 1 ;;
  copy) cp "$STUBS/copy-digest" "$STUBS/copied" ;;
esac
"""
CURL = """#!/bin/sh
echo "curl $*" >> "$STUBS/calls.log"
n=$(cat "$STUBS/count" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" > "$STUBS/count"
answer="$STUBS/health.$n"
[ -f "$answer" ] || answer="$STUBS/health"
[ -f "$answer" ] || exit 22
cat "$answer"
"""


class Stubs:
    def __init__(self):
        self.dir = tempfile.TemporaryDirectory()
        self.path = pathlib.Path(self.dir.name)
        bin_dir = self.path / "bin"
        bin_dir.mkdir()
        for name, body in (("aws", AWS), ("docker", DOCKER), ("crane", CRANE), ("curl", CURL)):
            (bin_dir / name).write_text(body)
            (bin_dir / name).chmod(0o755)
        self.env = {**os.environ, "PATH": f"{bin_dir}:{os.environ['PATH']}", "STUBS": str(self.path)}

    def write(self, name, text):
        (self.path / name).write_text(text)

    def calls(self):
        log = self.path / "calls.log"
        return log.read_text().splitlines() if log.exists() else []

    def run(self, script, **env):
        return subprocess.run(["sh", str(SCRIPTS / script)], env={**self.env, **env}, capture_output=True,
                              text=True, cwd=self.path)

    def release(self, **overrides):
        release = {"revision": REVISION, "version": REVISION[:8], "digest": DIGEST,
                   "stagingImage": f"{STAGING}/sendrec@{DIGEST}", "productionImage": f"{PRODUCTION}/sendrec@{DIGEST}",
                   **overrides}
        self.write("release.json", json.dumps(release))
        return str(self.path / "release.json")


class BuildImageTest(unittest.TestCase):
    def setUp(self):
        self.stubs = Stubs()
        self.addCleanup(self.stubs.dir.cleanup)

    def build(self, revision=REVISION):
        return self.stubs.run("build-image.sh", REVISION=revision, STAGING_REGISTRY=STAGING)

    def test_a_new_commit_is_built_once_with_its_version_and_label_then_pushed(self):
        self.stubs.write("describe-error", "An error occurred (ImageNotFoundException) when calling DescribeImages")
        self.stubs.write("push-digest", DIGEST)
        result = self.build()
        self.assertEqual(0, result.returncode, result.stderr)
        self.assertEqual(DIGEST, result.stdout.strip())
        calls = self.stubs.calls()
        [build] = [c for c in calls if c.startswith("docker build")]
        self.assertIn("--platform linux/amd64", build)
        self.assertIn(f"--build-arg VERSION={REVISION[:8]}", build)
        self.assertIn(f"--label org.opencontainers.image.revision={REVISION}", build)
        self.assertIn(f"-t {STAGING}/sendrec:{REVISION} .", build)
        self.assertIn(f"docker push {STAGING}/sendrec:{REVISION}", calls)
        self.assertIn("--registry-id 537421187871 --repository-name sendrec", calls[0])

    def test_an_image_already_pushed_is_reused_and_nothing_is_built(self):
        self.stubs.write("existing", DIGEST)
        result = self.build()
        self.assertEqual(0, result.returncode, result.stderr)
        self.assertEqual(DIGEST, result.stdout.strip())
        self.assertFalse([c for c in self.stubs.calls() if c.startswith("docker")])

    def test_any_other_read_error_fails_without_building(self):
        self.stubs.write("describe-error", "An error occurred (AccessDeniedException)")
        result = self.build()
        self.assertNotEqual(0, result.returncode)
        self.assertIn("AccessDeniedException", result.stderr)
        self.assertFalse([c for c in self.stubs.calls() if c.startswith("docker")])

    def test_a_short_or_unusual_revision_is_refused(self):
        for revision in ("0123456", REVISION.upper(), REVISION + "0", "main"):
            with self.subTest(revision=revision):
                self.assertNotEqual(0, self.build(revision).returncode)
        self.assertEqual([], self.stubs.calls())

    def test_a_missing_digest_fails(self):
        self.stubs.write("existing", "None")
        self.assertNotEqual(0, self.build().returncode)


class PromoteImageTest(unittest.TestCase):
    def setUp(self):
        self.stubs = Stubs()
        self.addCleanup(self.stubs.dir.cleanup)

    def test_the_staging_digest_is_copied_to_production_and_checked(self):
        self.stubs.write("copy-digest", DIGEST)
        result = self.stubs.run("promote-image.sh", RELEASE_FILE=self.stubs.release())
        self.assertEqual(0, result.returncode, result.stderr)
        calls = self.stubs.calls()
        self.assertIn(f"crane auth login {STAGING} -u AWS --password-stdin", calls)
        self.assertIn(f"crane auth login {PRODUCTION} -u AWS --password-stdin", calls)
        self.assertIn(f"crane copy {STAGING}/sendrec@{DIGEST} {PRODUCTION}/sendrec:{REVISION}", calls)
        self.assertEqual(f"crane digest {PRODUCTION}/sendrec:{REVISION}", calls[-1])

    def test_a_copy_with_another_digest_fails(self):
        self.stubs.write("copy-digest", OTHER)
        self.assertNotEqual(0, self.stubs.run("promote-image.sh", RELEASE_FILE=self.stubs.release()).returncode)

    def test_a_tag_already_holding_the_digest_is_left_alone(self):
        self.stubs.write("production-tag", DIGEST)
        result = self.stubs.run("promote-image.sh", RELEASE_FILE=self.stubs.release())
        self.assertEqual(0, result.returncode, result.stderr)
        self.assertFalse([c for c in self.stubs.calls() if c.startswith("crane copy")])

    def test_a_tag_naming_another_image_fails_without_copying(self):
        self.stubs.write("production-tag", OTHER)
        result = self.stubs.run("promote-image.sh", RELEASE_FILE=self.stubs.release())
        self.assertNotEqual(0, result.returncode)
        self.assertIn("already names", result.stderr)
        self.assertFalse([c for c in self.stubs.calls() if c.startswith("crane copy")])

    def test_a_release_file_whose_images_differ_is_refused(self):
        for overrides in ({"productionImage": f"{PRODUCTION}/sendrec@{OTHER}"}, {"digest": "latest"},
                          {"revision": "main"}):
            with self.subTest(overrides=overrides):
                result = self.stubs.run("promote-image.sh", RELEASE_FILE=self.stubs.release(**overrides))
                self.assertNotEqual(0, result.returncode)
        self.assertEqual([], self.stubs.calls())


class VerifyVersionTest(unittest.TestCase):
    def setUp(self):
        self.stubs = Stubs()
        self.addCleanup(self.stubs.dir.cleanup)

    def verify(self, attempts="3"):
        return self.stubs.run("verify-version.sh", BASE_URL="https://example.test", RELEASE_FILE=self.stubs.release(),
                              ATTEMPTS=attempts, DELAY="0")

    def test_passes_once_health_reports_the_release(self):
        self.stubs.write("health.1", json.dumps({"status": "ok", "version": "old00000"}))
        self.stubs.write("health.2", json.dumps({"status": "ok", "version": REVISION[:8]}))
        result = self.verify()
        self.assertEqual(0, result.returncode, result.stderr)
        self.assertEqual(2, len(self.stubs.calls()))
        self.assertIn("https://example.test/api/health", self.stubs.calls()[0])

    def test_fails_when_the_old_build_keeps_answering_or_nothing_does(self):
        self.stubs.write("health", json.dumps({"status": "ok", "version": "old00000"}))
        result = self.verify()
        self.assertNotEqual(0, result.returncode)
        self.assertIn("still serves 'old00000'", result.stderr)
        (self.stubs.path / "health").unlink()
        (self.stubs.path / "count").unlink()
        result = self.verify()
        self.assertNotEqual(0, result.returncode)
        self.assertIn("still serves 'nothing'", result.stderr)



class RepositoryPolicyTest(unittest.TestCase):
    """The one-time repository policies name only the pipeline's two roles, through a placeholder."""

    def policy(self, name):
        text = (SCRIPTS / name).read_text()
        self.assertNotRegex(text.replace("537421187871", ""), r"[0-9]{12}", "no account but staging's is committed")
        return json.loads(text.replace("DEPLOYMENT_ACCOUNT", "999988887777"))["Statement"]

    def roles(self, statements):
        return {s["Sid"]: s["Condition"]["StringEquals"]["aws:PrincipalArn"].rsplit("/", 1)[1]
                for s in statements if "Condition" in s}

    def test_staging_lets_build_push_and_promote_only_read(self):
        statements = self.policy("staging-repository-policy.json")
        self.assertEqual({"PipelineBuildPushesReleaseImages": "sendrec-pipeline-build",
                          "PipelinePromoteReadsReleaseImages": "sendrec-pipeline-promote"}, self.roles(statements))
        promote = next(s for s in statements if s["Sid"] == "PipelinePromoteReadsReleaseImages")
        self.assertFalse([a for a in promote["Action"] if "Upload" in a or "Put" in a])
        for statement in statements:
            self.assertEqual({"AWS": "arn:aws:iam::999988887777:root"}, statement["Principal"])

    def test_production_lets_only_promote_push(self):
        statements = self.policy("production-repository-policy.json")
        self.assertEqual({"PipelinePromoteCopiesReleaseImages": "sendrec-pipeline-promote"}, self.roles(statements))
        self.assertIn("ecr:PutImage", statements[0]["Action"])

    def test_the_base_policy_keeps_the_committed_statement_and_adds_build_pulls(self):
        statements = self.policy("staging-base-repository-policy.json")
        committed = json.loads((SCRIPTS.parent / "sendrec-base-repository-policy.json").read_text())["Statement"]
        self.assertEqual(committed, [s for s in statements if "Condition" not in s])
        self.assertEqual({"PipelineBuildPullsBase": "sendrec-pipeline-build"}, self.roles(statements))

if __name__ == "__main__":
    unittest.main()
