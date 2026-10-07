const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync('bootstrap-access.js', 'utf8');
function evaluate(ip, viewer) {
  const context = {event: {viewer: {ip: viewer}, request: {uri: '/api/auth/register', method: 'POST'}}};
  vm.createContext(context);
  vm.runInContext(source.replace('__BOOTSTRAP_IP__', ip) + '; result = handler(event);', context);
  return context;
}
const allowed = evaluate('192.0.2.10', '192.0.2.10');
assert.equal(allowed.result, allowed.event.request);
assert.equal(evaluate('192.0.2.10', '192.0.2.11').result.statusCode, 403);
assert.equal(evaluate('192.0.2.10', '::ffff:192.0.2.10').result.statusCode, 403);
const normal = evaluate('', '192.0.2.11');
assert.equal(normal.result, normal.event.request);
console.log('bootstrap access: allowed control, IPv4/IPv6 denials and normal access passed');
