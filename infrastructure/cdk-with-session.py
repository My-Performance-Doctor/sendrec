"""Run CDK with short-lived AWS CLI credentials held only in process memory."""
import json,os,subprocess,sys
creds=json.loads(subprocess.check_output(['aws','configure','export-credentials','--profile',os.environ.get('AWS_PROFILE','mpd-staging'),'--format','process']))
env=os.environ.copy()
env.pop('AWS_PROFILE',None)
env.update(AWS_ACCESS_KEY_ID=creds['AccessKeyId'],AWS_SECRET_ACCESS_KEY=creds['SecretAccessKey'],AWS_SESSION_TOKEN=creds['SessionToken'],AWS_REGION='ap-southeast-2',AWS_DEFAULT_REGION='ap-southeast-2')
sys.exit(subprocess.call(['cdk',*sys.argv[1:]],env=env))
