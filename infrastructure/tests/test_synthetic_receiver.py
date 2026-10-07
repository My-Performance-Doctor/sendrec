import contextlib
import hashlib
import hmac
import importlib.util
import io
import json
import os
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch


class ReceiverTests(unittest.TestCase):
    def setUp(self):
        fake = types.SimpleNamespace(client=lambda _: types.SimpleNamespace(
            get_secret_value=lambda **_: {'SecretString': 'synthetic-secret'}))
        spec = importlib.util.spec_from_file_location('receiver', Path(__file__).parents[1] / 'synthetic-receiver.py')
        self.receiver = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, boto3=fake):
            spec.loader.exec_module(self.receiver)

    def event(self, body, signature):
        return {'requestContext': {'http': {'method': 'POST'}}, 'body': body,
                'headers': {'x-webhook-signature': signature}, 'probe': True}

    def test_signed_control_and_tampering_without_content_logs(self):
        body = '{"event":"synthetic.test","data":"synthetic private content"}'
        signature = 'sha256=' + hmac.new(b'synthetic-secret', body.encode(), hashlib.sha256).hexdigest()
        output = io.StringIO()
        with patch.dict(os.environ, SECRET_ARN='synthetic-arn'), contextlib.redirect_stdout(output):
            self.assertEqual(204, self.receiver.handler(self.event(body, signature), None)['statusCode'])
            self.assertEqual(403, self.receiver.handler(self.event(body + ' ', signature), None)['statusCode'])
            self.assertEqual(403, self.receiver.handler(self.event(body, 'sha256=wrong'), None)['statusCode'])
        self.assertNotIn(body, output.getvalue())
        self.assertNotIn(signature, output.getvalue())
        self.assertEqual([True, False, False], [json.loads(line)['signature_verified'] for line in output.getvalue().splitlines()])

    def test_public_request_cannot_select_network_probe(self):
        with patch.object(self.receiver.urllib.request, 'urlopen') as network:
            event = {'requestContext': {'http': {'method': 'GET'}}, 'probe': True}
            self.assertEqual(405, self.receiver.handler(event, None)['statusCode'])
            network.assert_not_called()
