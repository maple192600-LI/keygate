'use strict';
// 仅验证随附件交付的 TEST_ONLY 固定向量。不是产品解析器、验签服务或商业许可实现。
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const { createHash, createPublicKey, verify } = require('node:crypto');
const root = __dirname;
const vector = JSON.parse(fs.readFileSync(path.join(root, 'TEST_ONLY.vector.json'), 'utf8'));
assert.equal(vector.classification, 'TEST_ONLY');
const raw = fs.readFileSync(path.join(root, vector.licenseFile));
const envelope = JSON.parse(raw.toString('utf8'));
const publicKey = createPublicKey(fs.readFileSync(path.join(root, vector.publicKeyFile)));
assert.equal(publicKey.asymmetricKeyType, 'ed25519');
const keyId = 'lic-' + createHash('sha256')
  .update(Buffer.from('stq-license-key/v1\0'))
  .update(publicKey.export({ type: 'spki', format: 'der' })).digest('hex');
assert.equal(keyId, vector.keyId);
assert.equal(keyId, envelope.keyId);
const payloadBytes = Buffer.from(envelope.payloadB64, 'base64url');
const signature = Buffer.from(envelope.signatureB64, 'base64url');
assert.equal(payloadBytes.toString('base64url'), envelope.payloadB64);
assert.equal(signature.toString('base64url'), envelope.signatureB64);
assert.equal(signature.length, 64);
assert.equal(raw.length, vector.expected.rawBytes);
assert.equal(payloadBytes.length, vector.expected.payloadBytes);
assert.equal(createHash('sha256').update(raw).digest('hex'), vector.expected.licenseSha256);
assert.equal(createHash('sha256').update(payloadBytes).digest('hex'), vector.expected.payloadSha256);
const domain = Buffer.from('stq-license/v2\0');
assert.equal(verify(null, Buffer.concat([domain, payloadBytes]), publicKey, signature), true);
assert.deepEqual(JSON.parse(payloadBytes.toString('utf8')), vector.payload);
assert.equal(payloadBytes.toString('utf8'), JSON.stringify(vector.payload));
assert.equal(raw.toString('utf8'), JSON.stringify(envelope));
const changed = Buffer.from(payloadBytes);
changed[changed.length - 2] ^= 1;
assert.equal(verify(null, Buffer.concat([domain, changed]), publicKey, signature), false);
assert.equal(verify(null, Buffer.concat([Buffer.from('stq-license/v1\0'), payloadBytes]), publicKey, signature), false);
console.log(JSON.stringify({
  result: 'PASS',
  identity: 'TEST_ONLY',
  keyId,
  checks: ['public-key identity', 'original-byte Ed25519 signature', 'canonical base64url',
    'fixed hashes and field order', 'tampered payload rejected', 'wrong signature domain rejected'],
  excludes: ['Go interoperability', 'product behavior', 'Windows installation', 'full strict JSON parser']
}, null, 2));

