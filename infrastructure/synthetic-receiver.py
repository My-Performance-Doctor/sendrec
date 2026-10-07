"""Verify synthetic webhook signatures. Never log or forward request bodies."""
import base64
import hashlib
import hmac
import json
import os
import urllib.error
import urllib.request

import boto3


def handler(event, context):
    # Only an IAM-authorized Lambda invocation can omit requestContext. A public
    # function URL request always carries it and cannot select this probe path.
    if 'requestContext' not in event and event.get('probe') is True:
        try:
            with urllib.request.urlopen(os.environ['TARGET_URL'] + '/api/health', timeout=15) as response:
                return {'status': response.status}
        except urllib.error.HTTPError as error:
            return {'status': error.code}
    if event.get('requestContext', {}).get('http', {}).get('method') != 'POST':
        return {'statusCode': 405, 'body': 'POST required'}
    raw = event.get('body', '')
    body = base64.b64decode(raw) if event.get('isBase64Encoded') else raw.encode()
    secret = boto3.client('secretsmanager').get_secret_value(SecretId=os.environ['SECRET_ARN'])['SecretString']
    expected = 'sha256=' + hmac.new(secret.encode(), body, hashlib.sha256).hexdigest()
    supplied = event.get('headers', {}).get('x-webhook-signature', '')
    valid = hmac.compare_digest(expected, supplied)
    # Logs contain only a result flag, never signatures, secrets or content.
    print(json.dumps({'signature_verified': valid}))
    return {'statusCode': 204 if valid else 403, 'body': ''}
