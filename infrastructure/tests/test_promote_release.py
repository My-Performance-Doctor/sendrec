import importlib.util
import pathlib
import tempfile
import hashlib
import json
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('promote', pathlib.Path(__file__).parents[1] / 'promote-release.py')
promote = importlib.util.module_from_spec(spec)
spec.loader.exec_module(promote)


class PromotionTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.template = {'Resources': {}}
        template = pathlib.Path(self.directory.name) / 'stack.json'
        template.write_text(json.dumps(self.template))
        config = {'templatePath': str(template), 'stack': 'SyntheticStage', 'desiredCount': 1, 'cluster': 'stage-cluster', 'service': 'stage-service',
                  'migrationTaskDefinition': 'stage-migration:1', 'applicationTaskDefinition': 'stage-app:2',
                  'subnets': ['synthetic-private'], 'securityGroups': ['synthetic-private'],
                  'readinessUrl': 'https://synthetic.example/api/ready'}
        self.manifest = {'revision': 'a' * 40, 'image': '111111111111.dkr.ecr.ap-southeast-2.amazonaws.com/sendrec@sha256:' + 'b' * 64,
                         'infrastructureSha256': hashlib.sha256(json.dumps([self.template,self.template],sort_keys=True,separators=(',',':')).encode()).hexdigest(), 'staging': config, 'production': {**config, 'desiredCount': 2}}
        for key in ('cluster', 'service', 'migrationTaskDefinition', 'applicationTaskDefinition'):
            self.manifest['production'][key] = self.manifest['production'][key].replace('stage', 'prod')
        self.calls = []
        self.current = 'old-app:1'
        self.count = 1
        self.migration_exit = 0

    def aws(self, *args):
        self.calls.append(args)
        if args[1] == 'get-template':
            return {'TemplateBody':self.template}
        if args[1] == 'describe-repositories':
            return {'repositories': [{'imageTagMutability':'IMMUTABLE'}]}
        if args[1] == 'describe-images':
            return {'imageDetails': [{'imageTags':[self.manifest['revision']]}]}
        if args[1] == 'describe-task-definition':
            mode = 'only' if 'migration' in args[-1] else 'skip'
            return {'taskDefinition': {'containerDefinitions': [{'image': self.manifest['image'], 'environment': [{'name': 'MIGRATIONS_MODE', 'value': mode}]}]}}
        if args[1] == 'describe-services':
            return {'services': [{'taskDefinition': self.current, 'desiredCount': self.count, 'runningCount': self.count}]}
        if args[1] == 'run-task':
            return {'tasks': [{'taskArn': 'synthetic-migration'}]}
        if args[1] == 'describe-tasks':
            return {'tasks': [{'containers': [{'exitCode': self.migration_exit}]}]}
        if args[1] == 'update-service':
            self.current = args[args.index('--task-definition')+1]
            self.count = int(args[args.index('--desired-count')+1])
        return {}

    def test_one_migration_and_immutable_promotion(self):
        with patch.object(promote, 'aws', self.aws):
            evidence = promote.promote(self.manifest, 'staging', probe=lambda _: True)
        self.assertTrue(evidence['verified'])
        migration_calls = [c for c in self.calls if c[1] == 'run-task']
        self.assertEqual(1, len(migration_calls))
        self.assertEqual('1', migration_calls[0][migration_calls[0].index('--count')+1])
        self.assertEqual('stage-app:2', self.current)

    def test_failed_migration_leaves_service_untouched(self):
        self.migration_exit = 1
        with patch.object(promote, 'aws', self.aws), self.assertRaisesRegex(RuntimeError, 'Migration failed'):
            promote.promote(self.manifest, 'staging', probe=lambda _: True)
        self.assertFalse(any(c[1]=='update-service' for c in self.calls))

    def test_bad_readiness_restores_previous_image_without_database_reversal(self):
        probes = iter([False, True])
        with patch.object(promote, 'aws', self.aws), self.assertRaisesRegex(RuntimeError, 'prior application restored'):
            promote.promote(self.manifest, 'staging', probe=lambda _: next(probes))
        self.assertEqual('old-app:1', self.current)
        self.assertEqual(1, self.count)
        self.assertEqual(1, len([c for c in self.calls if c[1]=='run-task']))

    def test_production_requires_matching_staging_and_distinct_resources(self):
        with self.assertRaises(ValueError):
            promote.validate(self.manifest, 'production')
        evidence = {k: self.manifest[k] for k in ('image','revision','infrastructureSha256')}
        evidence.update(target='staging',verified=True)
        promote.validate(self.manifest, 'production', evidence)
        evidence['image'] += '0'
        with self.assertRaises(ValueError):
            promote.validate(self.manifest, 'production', evidence)
        self.manifest['image'] = self.manifest['image'].split('@')[0]+':latest'
        with self.assertRaises(ValueError):
            promote.validate(self.manifest, 'staging')

    def test_reviewed_template_change_blocks_before_migration(self):
        pathlib.Path(self.manifest['staging']['templatePath']).write_text('{"Resources":{"Unexpected":{}}}')
        with patch.object(promote, 'aws', self.aws), self.assertRaisesRegex(ValueError, 'templates differ'):
            promote.promote(self.manifest, 'staging', probe=lambda _: True)
        self.assertFalse(any(c[1]=='run-task' for c in self.calls))
