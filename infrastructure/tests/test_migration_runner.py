import importlib.util
import json
import pathlib
import sys
import types
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location("migration_runner", pathlib.Path(__file__).parents[1] / "migration-runner.py")
sys.modules.setdefault("boto3", types.SimpleNamespace(client=None))
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)

PROPERTIES = {"Cluster": "synthetic-cluster", "TaskDefinition": "synthetic-migration:7", "Subnets": ["subnet-a"],
              "SecurityGroups": ["sg-a"], "Container": "migration"}
EVENT = {"RequestType": "Update", "StackId": "synthetic-stack", "RequestId": "r", "LogicalResourceId": "MigrationRun",
         "ResponseURL": "https://example.test/response", "ResourceProperties": PROPERTIES}


def stopped(code, name="migration", reason="Essential container in task exited"):
    return {"tasks": [{"lastStatus": "STOPPED", "stoppedReason": reason, "containers": [{"name": name, "exitCode": code}]}]}


def ecs_with(*described):
    ecs = Mock()
    ecs.run_task.return_value = {"tasks": [{"taskArn": "synthetic-task"}], "failures": []}
    ecs.describe_tasks.side_effect = list(described)
    return ecs


class RunTest(unittest.TestCase):
    def test_exit_zero_succeeds_after_the_task_stops(self):
        ecs = ecs_with({"tasks": [{"lastStatus": "RUNNING"}]}, stopped(0))
        self.assertEqual("synthetic-task", runner.run(ecs, PROPERTIES, lambda: 900_000, sleep=lambda _: None))
        ecs.run_task.assert_called_once_with(
            cluster="synthetic-cluster", taskDefinition="synthetic-migration:7", launchType="FARGATE", count=1,
            startedBy="sendrec-migration-run", networkConfiguration={"awsvpcConfiguration": {
                "subnets": ["subnet-a"], "securityGroups": ["sg-a"], "assignPublicIp": "DISABLED"}})
        self.assertEqual(2, ecs.describe_tasks.call_count)

    def test_any_other_exit_fails_with_the_code(self):
        for described, message in ((stopped(1), "exit code 1"), (stopped(None), "exit code unknown"),
                                   (stopped(0, name="other"), "exit code unknown")):
            with self.subTest(message=message), self.assertRaisesRegex(RuntimeError, message):
                runner.run(ecs_with(described), PROPERTIES, lambda: 900_000, sleep=lambda _: None)

    def test_a_failed_or_empty_read_is_read_again_and_never_abandons_the_task(self):
        ecs = ecs_with(RuntimeError("ThrottlingException"), {"tasks": []}, stopped(0))
        self.assertEqual("synthetic-task", runner.run(ecs, PROPERTIES, lambda: 900_000, sleep=lambda _: None))
        remaining = iter([900_000, 120_000, 59_000])
        ecs = ecs_with(RuntimeError("ThrottlingException"), RuntimeError("AccessDeniedException"))
        with self.assertRaisesRegex(RuntimeError, "still running; the deploy rolls back.*AccessDeniedException"):
            runner.run(ecs, PROPERTIES, lambda: next(remaining), sleep=lambda _: None)
        ecs.stop_task.assert_not_called()

    def test_a_task_that_does_not_start_fails(self):
        for response in ({"tasks": [], "failures": [{"reason": "RESOURCE:MEMORY"}]}, {"tasks": []},
                         {"tasks": [{"taskArn": "a"}], "failures": [{"reason": "x"}]}):
            ecs = Mock()
            ecs.run_task.return_value = response
            with self.subTest(response=response), self.assertRaisesRegex(RuntimeError, "did not start"):
                runner.run(ecs, PROPERTIES, lambda: 900_000, sleep=lambda _: None)

    def test_a_denied_start_is_retried_while_the_new_grant_propagates(self):
        class Denied(Exception):
            def __init__(self, code):
                super().__init__(code)
                self.response = {"Error": {"Code": code}}

        ecs = ecs_with(stopped(0))
        started = ecs.run_task.return_value
        ecs.run_task.side_effect = [Denied("AccessDeniedException"), Denied("AccessDeniedException"), started]
        self.assertEqual("synthetic-task", runner.run(ecs, PROPERTIES, lambda: 900_000, sleep=lambda _: None))
        self.assertEqual(3, ecs.run_task.call_count)
        for side_effect, calls in (([Denied("ClusterNotFoundException")], 1),
                                   ([Denied("AccessDeniedException")] * runner.DENIED_ATTEMPTS, runner.DENIED_ATTEMPTS)):
            ecs = ecs_with()
            ecs.run_task.side_effect = side_effect
            with self.subTest(calls=calls), self.assertRaises(Denied):
                runner.run(ecs, PROPERTIES, lambda: 900_000, sleep=lambda _: None)
            self.assertEqual(calls, ecs.run_task.call_count)

    def test_a_task_still_running_near_the_limit_fails_the_deploy_and_is_left_to_finish(self):
        remaining = iter([900_000, 120_000, 59_000])
        ecs = ecs_with({"tasks": [{"lastStatus": "RUNNING"}]}, {"tasks": [{"lastStatus": "RUNNING"}]})
        with self.assertRaisesRegex(RuntimeError, "synthetic-task still running; the deploy rolls back"):
            runner.run(ecs, PROPERTIES, lambda: next(remaining), sleep=lambda _: None)
        # Stopping it midway would leave the schema dirty.
        ecs.stop_task.assert_not_called()


class HandlerTest(unittest.TestCase):
    def handle(self, event, status="UPDATE_IN_PROGRESS", ecs=None):
        cloudformation = Mock()
        cloudformation.describe_stacks.return_value = {"Stacks": [{"StackStatus": status}]}
        ecs = ecs or ecs_with(stopped(0))
        clients = {"cloudformation": cloudformation, "ecs": ecs}
        sent = []
        context = Mock()
        context.get_remaining_time_in_millis.return_value = 900_000
        with patch.object(runner.boto3, "client", side_effect=clients.__getitem__, create=True), \
                patch.object(runner, "respond", side_effect=lambda e, s, r: sent.append((s, r))), \
                patch.object(runner.time, "sleep"):
            runner.handler(event, context)
        return sent, ecs

    def test_update_and_create_run_the_migration(self):
        for kind in ("Create", "Update"):
            sent, ecs = self.handle({**EVENT, "RequestType": kind})
            self.assertEqual([("SUCCESS", "Migrations applied by synthetic-task")], sent)
            ecs.run_task.assert_called_once()

    def test_delete_and_rollback_run_nothing(self):
        for event, status in (({**EVENT, "RequestType": "Delete"}, "UPDATE_IN_PROGRESS"),
                              (EVENT, "UPDATE_ROLLBACK_IN_PROGRESS"), (EVENT, "ROLLBACK_IN_PROGRESS")):
            with self.subTest(status=status, kind=event["RequestType"]):
                sent, ecs = self.handle(event, status)
                self.assertEqual("SUCCESS", sent[0][0])
                ecs.run_task.assert_not_called()

    def test_a_failure_is_reported_to_cloudformation(self):
        sent, _ = self.handle(EVENT, ecs=ecs_with(stopped(3)))
        self.assertEqual("FAILED", sent[0][0])
        self.assertIn("exit code 3", sent[0][1])

    def test_the_response_names_one_physical_id_and_trims_the_reason(self):
        captured = {}

        class Response:
            def __enter__(self):
                return self

            def __exit__(self, *args):
                return False

            def read(self):
                return b""

        def urlopen(request, timeout):
            captured["method"], captured["body"] = request.get_method(), json.loads(request.data)
            return Response()

        with patch.object(runner.urllib.request, "urlopen", urlopen):
            runner.respond(EVENT, "FAILED", "x" * 1000)
        self.assertEqual("PUT", captured["method"])
        self.assertEqual({"Status": "FAILED", "Reason": "x" * 400, "PhysicalResourceId": "migration-run",
                          "StackId": "synthetic-stack", "RequestId": "r", "LogicalResourceId": "MigrationRun"},
                         captured["body"])


if __name__ == "__main__":
    unittest.main()
