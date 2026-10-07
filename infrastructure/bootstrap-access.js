function handler(event) {
  var bootstrapIp = '__BOOTSTRAP_IP__';
  if (bootstrapIp && event.viewer.ip !== bootstrapIp) {
    return {
      statusCode: 403,
      statusDescription: 'Forbidden',
      headers: {'cache-control': {value: 'no-store'}}
    };
  }
  return event.request;
}
