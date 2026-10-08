import datetime
import importlib.util
import pathlib
import sys
import types
import unittest

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
