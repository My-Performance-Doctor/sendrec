import datetime
import importlib.util
import pathlib
import sys
import types
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location("backup_probe", pathlib.Path(__file__).parents[1] / "backup-probe.py")
sys.modules.setdefault("boto3", types.SimpleNamespace())
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)


class BackupProbeTest(unittest.TestCase):
    def test_retention_and_recovery_lag_fail_closed(self):
        now = datetime.datetime.now(datetime.timezone.utc)
        self.assertEqual(60, probe.backup_age({"BackupRetentionPeriod": 35, "LatestRestorableTime": now - datetime.timedelta(seconds=60)}, now))
        self.assertEqual(86400, probe.backup_age({"BackupRetentionPeriod": 7, "LatestRestorableTime": now}, now))
        self.assertEqual(86400, probe.backup_age({"BackupRetentionPeriod": 35}, now))

    def test_application_alarm_gate_follows_live_ecs_desired_count(self):
        with patch.dict(probe.os.environ, {"CLUSTER_ARN":"synthetic-cluster","SERVICE_ARN":"synthetic-service"}):
            for count in (0,2):
                client=Mock()
                client.describe_services.return_value={"services":[{"desiredCount":count}]}
                self.assertEqual(count,probe.service_desired_count(client))
                client.describe_services.assert_called_once_with(cluster="synthetic-cluster",services=["synthetic-service"])
            for response in ({"services":[]},{"services":[{"desiredCount":0}],"failures":[{}]},{"services":[{"desiredCount":-1}]}):
                client=Mock()
                client.describe_services.return_value=response
                self.assertEqual(1,probe.service_desired_count(client))
            client=Mock()
            client.describe_services.side_effect=RuntimeError("private diagnostic")
            self.assertEqual(1,probe.service_desired_count(client))
