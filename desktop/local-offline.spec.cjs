'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const os = require('node:os');
const { createRequire } = require('node:module');
const { spawnSync } = require('node:child_process');
const { randomUUID } = require('node:crypto');
const { waitForHealth } = require('./runtime.cjs');

const clientRoot = process.env.SNAIL_CLIENT_ROOT;
if (!clientRoot) throw new Error('SNAIL_CLIENT_ROOT must select the isolated client checkout for Electron test dependencies.');
const requireClient = createRequire(path.join(clientRoot, 'desktop-client/package.json'));
const { _electron, expect } = requireClient('playwright/test');
const executablePath = requireClient('electron');
const mainPath = path.join(__dirname, 'main.cjs');

// Old UI assertions for signed-file download/delivery and device-request files
// map to Go/API tests: TestLocalOfflineIssuanceHTTP, TestLocalOfflineCustomersAndRequests,
// TestLocalOfflineElectronBridge, TestClientSyncReturnsSignedTransferAndReplacementActivationKeepsTerm,
// TestLocalOfflineRestoreSwitchAndRuntimeFailurePreservesSource, and TestOfflineBusinessBundlePreserves*.
// Current Electron assertions cover code issue/recovery, owner copy/detail, import ownership, backup,
// and service/window lifecycle.
function createTaskRoot(prefix) {
  return fs.realpathSync.native(fs.mkdtempSync(path.join(os.tmpdir(), prefix)));
}

function requirePostgresRoot() {
  const root = process.env.SNAIL_MANAGER_TEST_POSTGRES;
  if (!root) throw new Error('SNAIL_MANAGER_TEST_POSTGRES must select the verified task PostgreSQL runtime.');
  return root;
}

function managerEnv(root, dataRoot, postgresRoot) {
  const env = {
    ...process.env,
    APPDATA: path.join(root, 'appdata'),
    LOCALAPPDATA: path.join(root, 'localappdata'),
    TEMP: path.join(root, 'tmp'),
    TMP: path.join(root, 'tmp'),
    SNAIL_MANAGER_DEV_DATA: dataRoot,
    SNAIL_MANAGER_DEV_POSTGRES: postgresRoot,
  };
  delete env.ELECTRON_RUN_AS_NODE;
  delete env.SNAIL_MANAGER_DATA_ROOT;
  return env;
}

function readSession(dataRoot) {
  return JSON.parse(fs.readFileSync(path.join(dataRoot, 'runtime-session.json'), 'utf8'));
}

function processIsAlive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    if (error.code === 'ESRCH') return false;
    if (error.code === 'EPERM') return true;
    throw error;
  }
}

async function waitForExit(child, timeout, label) {
  if (child.exitCode !== null && child.exitCode !== undefined) {
    assert.equal(child.exitCode, 0, label + ' exit code');
    return;
  }
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(label + ' did not exit')), timeout);
    child.once('exit', code => {
      clearTimeout(timer);
      if (code === 0) resolve();
      else reject(new Error(label + ' exit code ' + code));
    });
  });
}

function postgresStatus(postgresRoot, dataRoot) {
  return spawnSync(path.join(postgresRoot, 'bin', 'pg_ctl.exe'), ['status', '-D', path.join(dataRoot, 'postgres')], {
    windowsHide: true,
    encoding: 'utf8',
  });
}

function stopOwnedHost(root, dataRoot, postgresRoot) {
  const sessionPath = path.join(dataRoot, 'runtime-session.json');
  if (!fs.existsSync(sessionPath)) return;
  const session = readSession(dataRoot);
  if (session.state !== 'running' || !processIsAlive(session.pid)) return;
  const stopped = spawnSync(executablePath, [mainPath, '--snail-stop'], {
    env: managerEnv(root, dataRoot, postgresRoot),
    windowsHide: true,
    encoding: 'utf8',
    timeout: 30000,
  });
  assert.equal(stopped.status, 0, stopped.stdout + stopped.stderr);
}

async function adminRequest(page, requestPath, method = 'GET', body) {
  return page.evaluate(async ({ requestPath, method, body }) => {
    const response = await fetch('/api/v1' + requestPath, {
      method,
      credentials: 'same-origin',
      ...(body === undefined ? {} : {
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      }),
    });
    const text = await response.text();
    return {
      status: response.status,
      cacheControl: response.headers.get('cache-control'),
      value: text ? JSON.parse(text) : null,
    };
  }, { requestPath, method, body });
}

async function saveDownload(app, page, file, buttonName) {
  const completed = app.evaluate(({ session }, target) => new Promise(resolve => {
    const timer = setTimeout(() => resolve({ state: 'timeout', bytes: 0 }), 30000);
    session.defaultSession.once('will-download', (_event, item) => {
      item.setSavePath(target);
      item.once('done', (_doneEvent, state) => {
        clearTimeout(timer);
        resolve({ state, bytes: item.getReceivedBytes() });
      });
    });
  }), file);
  await page.getByRole('button', { name: buttonName, exact: true }).click();
  const result = await completed;
  assert.equal(result.state, 'completed');
  assert.ok(result.bytes > 0);
  await expect.poll(() => fs.existsSync(file)).toBe(true);
  return fs.readFileSync(file);
}

function assertEncryptedFile(file, secrets) {
  const bytes = fs.readFileSync(file);
  assert.ok(bytes.length > 0);
  assert.throws(() => JSON.parse(bytes.toString('utf8')));
  for (const secret of secrets) assert.equal(bytes.includes(Buffer.from(secret)), false);
  return bytes;
}

async function setupOwner(app, page, username, password, siteName, contact) {
  await page.getByLabel('管理用户名', { exact: true }).fill(username);
  await page.getByLabel('密码', { exact: true }).fill(password);
  await page.getByLabel('确认密码', { exact: true }).fill(password);
  await page.getByLabel('商家名称', { exact: true }).fill(siteName);
  await page.getByLabel('联系方式', { exact: true }).fill(contact);
  await page.getByRole('button', { name: '完成设置', exact: true }).click();
  await expect(page.getByRole('heading', { name: '保存账号恢复凭据', exact: true })).toBeVisible();
  const recoveryCredential = await page.locator('code').innerText();
  assert.ok(/^SNAIL-RECOVERY-[A-Fa-f0-9]{64}$/.test(recoveryCredential), 'recovery credential format');
  await page.bringToFront();
  await page.getByRole('button', { name: '复制恢复凭据', exact: true }).click();
  await expect(page.getByRole('alert')).toHaveCount(0);
  const copiedCredential = await app.evaluate(({ clipboard }) => clipboard.readText());
  assert.ok(copiedCredential === recoveryCredential, 'recovery credential clipboard readback');
  await page.getByRole('button', { name: '已妥善保存，继续登录', exact: true }).click();
  return recoveryCredential;
}

async function login(page, username, password) {
  await page.getByLabel('管理用户名', { exact: true }).fill(username);
  await page.getByLabel('密码', { exact: true }).fill(password);
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await expect(page.getByRole('heading', { name: '客户', exact: true })).toBeVisible();
}

async function createCustomer(page, email) {
  await page.getByRole('button', { name: '新建客户', exact: true }).click();
  const form = page.getByRole('dialog', { name: '新建客户', exact: true });
  await form.getByLabel('客户名称', { exact: true }).fill('同名合成客户');
  await form.getByLabel('联系人', { exact: true }).fill('合成联系人');
  await form.getByLabel('邮箱', { exact: true }).fill(email);
  await form.getByLabel('备注', { exact: true }).fill(email.startsWith('first') ? '甲方合同' : '乙方合同');
  await form.getByRole('button', { name: '保存客户', exact: true }).click();
  await expect(form).toHaveCount(0);
}

async function showLicenseDetails(page, licenseNumber) {
  await page.getByRole('row').filter({ hasText: licenseNumber })
    .getByRole('button', { name: '查看详情', exact: true }).click();
  return page.getByRole('dialog', { name: '授权详情', exact: true });
}

async function closeWindowAndWait(app) {
  const child = app.process();
  const managerPID = child.pid;
  let stderr = '';
  const collectStderr = chunk => { stderr += chunk.toString(); };
  child.stderr?.on('data', collectStderr);
  await app.evaluate(({ app }) => {
    for (const eventName of ['window-all-closed', 'before-quit', 'will-quit']) {
      app.once(eventName, () => process.stderr.write('T03_MANAGER_EVENT ' + eventName + '\n'));
    }
  });
  const exited = waitForExit(child, 30000, '管理窗口进程 ' + managerPID);
  await app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0].close());
  try {
    await exited;
  } catch (error) {
    console.log('T03_MANAGER_EXIT_EVIDENCE', JSON.stringify({ managerPID, stderr }));
    throw error;
  }
  child.stderr?.off('data', collectStderr);
  const events = [...stderr.matchAll(/T03_MANAGER_EVENT (window-all-closed|before-quit|will-quit)/g)].map(match => match[1]);
  console.log('T03_MANAGER_EXIT_EVIDENCE', JSON.stringify({ managerPID, events }));
  assert.deepEqual(events, ['window-all-closed', 'before-quit', 'will-quit']);
  assert.equal(processIsAlive(managerPID), false);
  return managerPID;
}

test('Windows 登录启动期间的普通打开请求会在服务就绪后显示管理窗口', { timeout: 120000 }, async t => {
  const root = createTaskRoot('snail-keygate-startup-');
  const dataRoot = path.join(root, 'data');
  const postgresRoot = requirePostgresRoot();
  for (const directory of ['appdata', 'localappdata', 'tmp']) fs.mkdirSync(path.join(root, directory));
  let app;
  t.after(async () => {
    if (app) await app.close();
    stopOwnedHost(root, dataRoot, postgresRoot);
  });

  app = await _electron.launch({
    executablePath,
    args: [mainPath, '--startup'],
    env: managerEnv(root, dataRoot, postgresRoot),
  });

  const runtimeConfig = path.join(dataRoot, 'runtime.json');
  const sessionFile = path.join(dataRoot, 'runtime-session.json');
  await expect.poll(() => fs.existsSync(runtimeConfig) && !fs.existsSync(sessionFile), {
    timeout: 60000,
    intervals: [10, 20, 50, 100, 200],
  }).toBe(true);

  const normalOpen = spawnSync(executablePath, [mainPath], {
    env: managerEnv(root, dataRoot, postgresRoot),
    windowsHide: true,
    encoding: 'utf8',
    timeout: 15000,
  });
  assert.equal(normalOpen.status, 0, normalOpen.stdout + normalOpen.stderr);

  const page = await app.firstWindow({ timeout: 60000 });
  await expect(page.getByRole('heading', { name: '首次设置', exact: true })).toBeVisible();
  const session = readSession(dataRoot);
  assert.equal(session.state, 'running');
  const managerPID = app.process().pid;
  assert.notEqual(session.pid, managerPID);
  assert.equal(processIsAlive(managerPID), true);
  await waitForHealth(session.address, { exitCode: null, signalCode: null }, session.setupToken, 5000);
  assert.equal(postgresStatus(postgresRoot, dataRoot).status, 0);
  console.log('T03_STARTUP_PROCESSES', JSON.stringify({ managerPID, serviceHostPID: session.pid }));
  console.log('T03_STARTUP_EVIDENCE', root);
});

test('授权码签发恢复、续期换机、备份移交、账号隔离与服务关闭重开', { timeout: 300000 }, async t => {
  const root = createTaskRoot('snail-keygate-online-');
  const dataRoot = path.join(root, 'data');
  const targetDataRoot = path.join(root, 'target-data');
  const postgresRoot = requirePostgresRoot();
  for (const directory of ['appdata', 'localappdata', 'tmp']) fs.mkdirSync(path.join(root, directory));
  let app;
  let page;
  t.after(async () => {
    if (app) {
      await app.close();
    }
    stopOwnedHost(root, targetDataRoot, postgresRoot);
    stopOwnedHost(root, dataRoot, postgresRoot);
  });

  app = await _electron.launch({
    executablePath,
    args: [mainPath],
    env: managerEnv(root, dataRoot, postgresRoot),
  });
  page = await app.firstWindow({ timeout: 45000 });
  await expect(page.getByRole('heading', { name: '首次设置', exact: true })).toBeVisible();

  const address = new URL(page.url()).origin;
  const outsideSetup = await fetch(address + '/api/v1/local/setup', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Origin: address },
    body: JSON.stringify({
      username: 'outside_owner',
      password: 'Synthetic-outside-password-2026',
      siteName: '不应建立',
      contact: '隔离测试',
    }),
  });
  assert.equal(outsideSetup.status, 403);
  assert.equal((await outsideSetup.json()).error.code, 'LOCAL_WINDOW_REQUIRED');

  const recoveryCredential = await setupOwner(
    app,
    page,
    'source_owner',
    'Synthetic-source-owner-password-2026',
    '隔离合成厂商',
    '合成联系号',
  );
  await page.getByLabel('密码', { exact: true }).fill('Wrong-source-owner-password-2026');
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await expect(page.getByRole('alert')).toHaveText('用户名或密码不正确');
  await page.getByRole('button', { name: '忘记密码', exact: true }).click();
  await page.getByLabel('恢复凭据', { exact: true }).fill(recoveryCredential);
  await page.getByLabel('新密码', { exact: true }).fill('Synthetic-source-recovered-password-2026');
  await page.getByLabel('确认新密码', { exact: true }).fill('Synthetic-source-recovered-password-2026');
  await page.getByRole('button', { name: '重置密码', exact: true }).click();
  await expect(page.getByRole('status')).toHaveText('密码已更新，请使用新密码登录');
  const reusedRecovery = await fetch(address + '/api/v1/local/recovery/reset', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      username: 'source_owner',
      recoveryCredential,
      newPassword: 'Synthetic-source-reused-password-2026',
    }),
  });
  assert.equal(reusedRecovery.status, 401);
  await login(page, 'source_owner', 'Synthetic-source-recovered-password-2026');
  await expect(page.getByRole('link', { name: 'Powered by Keygate' })).toBeVisible();

  await page.getByRole('button', { name: '客户', exact: true }).click();
  await createCustomer(page, 'first@synthetic.invalid');
  await createCustomer(page, 'second@synthetic.invalid');
  const duplicateRows = page.getByRole('row').filter({ hasText: '同名合成客户' });
  await expect(duplicateRows).toHaveCount(2);

  const selectedCustomer = duplicateRows.filter({ hasText: 'second@synthetic.invalid' });
  await selectedCustomer.getByRole('button', { name: '发放授权', exact: true }).click();
  let issue = page.getByRole('dialog');
  await issue.getByLabel('授权期限', { exact: true }).selectOption('days');
  await issue.getByLabel('数量', { exact: true }).fill('7');
  await issue.getByLabel('允许设备数', { exact: true }).fill('1');
  await issue.getByRole('button', { name: '预览授权条件', exact: true }).click();
  await expect(issue.getByText('签发后生成', { exact: true })).toBeVisible();

  let createResult;
  let operationID;
  let initialPosts = 0;
  const loseIssueResponse = async route => {
    if (route.request().method() !== 'POST') return route.continue();
    initialPosts++;
    operationID = route.request().postDataJSON().operationId;
    const committed = await route.fetch();
    const envelope = await committed.json();
    if (committed.ok()) createResult = envelope.data;
    await route.fulfill({
      status: 503,
      contentType: 'application/json',
      body: JSON.stringify({ success: false, error: { code: 'ISSUANCE_RESULT_UNKNOWN', message: '结果待查询' } }),
    });
  };
  await page.route('**/api/v1/admin/offline-licenses', loseIssueResponse);
  await issue.getByRole('button', { name: '确认办理', exact: true }).click();
  await expect(issue.getByRole('alert')).toBeVisible();
  await page.unroute('**/api/v1/admin/offline-licenses', loseIssueResponse);
  assert.equal(initialPosts, 1);
  assert.ok(createResult);
  assert.match(operationID, /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i);

  const licenseID = createResult.licenseId;
  const licenseNumber = 'L-' + licenseID.replaceAll('-', '').toUpperCase();
  const licenseCode = createResult.licenseCode;
  const operationResponse = page.waitForResponse(response =>
    response.request().method() === 'GET' && new URL(response.url()).pathname.endsWith('/issuance-operations/' + operationID),
  );
  const codeResponse = page.waitForResponse(response =>
    response.request().method() === 'GET' && new URL(response.url()).pathname.endsWith('/offline-licenses/' + licenseID + '/code'),
  );
  await issue.getByRole('button', { name: '查询本次结果', exact: true }).click();
  const recoveredOperation = (await (await operationResponse).json()).data;
  const recoveredCode = await codeResponse;
  await expect(issue.getByRole('heading', { name: /授权已办理/ })).toBeVisible();
  assert.equal(recoveredOperation.licenseId, licenseID);
  assert.equal(recoveredOperation.licenseCode, undefined);
  assert.equal(recoveredCode.headers()['cache-control'], 'no-store');
  assert.ok((await recoveredCode.json()).data.licenseCode === licenseCode, 'recovered activation code matches original');
  assert.ok(licenseCode);
  assert.equal(createResult.licenseNumber, licenseNumber);
  await expect(issue.getByText(licenseNumber, { exact: true })).toBeVisible();
  assert.ok((await issue.locator('dd').allInnerTexts()).some(text => text.trim() === licenseCode), 'issued code is visible');
  await page.bringToFront();
  await expect(issue.getByRole('button', { name: '复制授权码', exact: true })).toBeVisible();
  await issue.getByRole('button', { name: '复制授权码', exact: true }).click();
  await expect(issue.getByRole('status')).toHaveText('授权码已复制');
  const issuedClipboard = await app.evaluate(({ clipboard }) => clipboard.readText());
  assert.ok(issuedClipboard === licenseCode, 'issued activation code clipboard readback');
  await issue.getByRole('button', { name: /^(关闭|Close)$/ }).click();

  const codeReply = await adminRequest(page, '/admin/offline-licenses/' + licenseID + '/code');
  assert.equal(codeReply.status, 200);
  assert.equal(codeReply.cacheControl, 'no-store');
  assert.ok(codeReply.value.data.licenseCode === licenseCode, 'owner code endpoint returns original code');
  const ordinaryDetail = await adminRequest(page, '/admin/offline-licenses/' + licenseID);
  assert.equal(ordinaryDetail.status, 200);
  assert.equal(JSON.stringify(ordinaryDetail.value).includes(licenseCode), false);

  const listByLicense = await adminRequest(page, '/admin/offline-licenses?q=' + encodeURIComponent(licenseNumber));
  assert.equal(listByLicense.status, 200);
  assert.ok(listByLicense.value.data.items.some(item => item.id === licenseID));
  assert.equal(JSON.stringify(listByLicense.value).includes(licenseCode), false);
  const fullCustomerID = ordinaryDetail.value.data.license.customerId;
  assert.match(ordinaryDetail.value.data.license.customerNumber, /^C-[A-F0-9]{32}$/);
  const listByCustomer = await adminRequest(
    page,
    '/admin/commercial-customers?q=' + encodeURIComponent(ordinaryDetail.value.data.license.customerNumber),
  );
  assert.equal(listByCustomer.status, 200);
  assert.ok(listByCustomer.value.data.items.some(item => item.id === fullCustomerID));

  await page.getByRole('button', { name: '授权', exact: true }).click();
  await page.getByLabel('查询授权', { exact: true }).fill(licenseNumber);
  await page.getByRole('button', { name: '查询', exact: true }).click();
  await expect(page.getByRole('row').filter({ hasText: licenseNumber })).toHaveCount(1);
  let details = await showLicenseDetails(page, licenseNumber);
  await expect(details.getByText(licenseNumber, { exact: true })).toBeVisible();
  await details.getByRole('button', { name: '显示授权码', exact: true }).click();
  assert.ok((await details.locator('dd').allInnerTexts()).some(text => text.trim() === licenseCode), 'detail code is visible');
  await expect(details.getByRole('button', { name: '复制授权码', exact: true })).toBeVisible();
  await page.bringToFront();
  await details.getByRole('button', { name: '复制授权码', exact: true }).click();
  await expect(details.getByRole('alert')).toHaveCount(0);
  const detailClipboard = await app.evaluate(({ clipboard }) => clipboard.readText());
  assert.ok(detailClipboard === licenseCode, 'detail activation code clipboard readback');

  const firstDevice = {
    type: 'windows-smbios-uuid-sha256/v1',
    digest: 'a'.repeat(64),
  };
  const firstActivation = await adminRequest(page, '/client/activate', 'POST', {
    operationId: randomUUID(),
    licenseCode,
    productId: 'cn.snailfinance.desktop',
    device: firstDevice,
    deviceLabel: '合成设备一',
    appVersion: '0.1.2',
    buildId: 'synthetic-build-20261029',
  });
  assert.equal(firstActivation.status, 200);
  assert.equal(firstActivation.value.data.licenseId, licenseID);
  assert.ok(firstActivation.value.data.rawLicense);

  const deviceState = await adminRequest(page, '/admin/offline-licenses/' + licenseID);
  const firstReportedDevice = deviceState.value.data.license.devices.find(device => device.deviceState === 'current');
  assert.equal(firstReportedDevice.deviceLabel, '合成设备一');
  assert.equal(firstReportedDevice.appVersion, '0.1.2');
  assert.equal(firstReportedDevice.buildId, 'synthetic-build-20261029');
  assert.ok(firstReportedDevice.lastReportedAt);

  await details.getByRole('button', { name: /^(关闭|Close)$/ }).click();
  details = await showLicenseDetails(page, licenseNumber);
  await expect(details.getByText('合成设备一', { exact: true })).toBeVisible();
  await details.getByRole('button', { name: '续期', exact: true }).click();
  let lifecycle = page.getByRole('dialog');
  await lifecycle.getByLabel('数量', { exact: true }).fill('7');
  await lifecycle.getByRole('button', { name: '预览授权条件', exact: true }).click();
  await expect(lifecycle.getByText(licenseNumber, { exact: true })).toBeVisible();
  const renewalResponse = page.waitForResponse(response =>
    response.request().method() === 'POST' && new URL(response.url()).pathname.endsWith('/renew'),
  );
  await lifecycle.getByRole('button', { name: '确认办理', exact: true }).click();
  const renewed = (await (await renewalResponse).json()).data;
  assert.equal(renewed.licenseId, licenseID);
  assert.equal(renewed.licenseNumber, licenseNumber);
  assert.equal(renewed.licenseCode, undefined);
  await lifecycle.getByRole('button', { name: /^(关闭|Close)$/ }).click();
  await details.getByRole('button', { name: /^(关闭|Close)$/ }).click();
  details = await showLicenseDetails(page, licenseNumber);
  const renewedCode = await adminRequest(page, '/admin/offline-licenses/' + licenseID + '/code');
  assert.ok(renewedCode.value.data.licenseCode === licenseCode, 'renewal preserves activation code');

  const expiring = await adminRequest(page, '/admin/offline-licenses?expiring=15d');
  assert.ok(expiring.value.data.items.some(item => item.id === licenseID));

  await details.getByRole('button', { name: '办理换机', exact: true }).click();
  lifecycle = page.getByRole('dialog');
  await lifecycle.getByLabel('换机原因', { exact: true }).fill('隔离换机验收');
  await lifecycle.getByRole('button', { name: '预览换机', exact: true }).click();
  await expect(lifecycle.getByText(licenseNumber, { exact: true })).toBeVisible();
  let transferCommand;
  const transferRequest = page.waitForRequest(request =>
    request.method() === 'POST' && new URL(request.url()).pathname.endsWith('/transfer'),
  );
  const transferResponse = page.waitForResponse(response =>
    response.request().method() === 'POST' && new URL(response.url()).pathname.endsWith('/transfer'),
  );
  await lifecycle.getByRole('button', { name: '确认换机', exact: true }).click();
  transferCommand = (await transferRequest).postDataJSON();
  const transferred = (await (await transferResponse).json()).data;
  assert.equal(transferred.licenseId, licenseID);
  assert.equal(transferred.licenseNumber, licenseNumber);
  assert.equal(transferred.expiresAt, renewed.expiresAt);
  assert.equal(transferCommand.bindingId, firstReportedDevice.bindingId);
  assert.equal(Object.hasOwn(transferCommand, 'deviceCode'), false);
  assert.equal(Object.hasOwn(transferCommand, 'requestId'), false);
  await lifecycle.getByRole('button', { name: /^(关闭|Close)$/ }).click();

  const secondActivation = await adminRequest(page, '/client/activate', 'POST', {
    operationId: randomUUID(),
    licenseCode,
    productId: 'cn.snailfinance.desktop',
    device: { type: firstDevice.type, digest: 'b'.repeat(64) },
    deviceLabel: '合成设备二',
    appVersion: '0.1.3',
    buildId: 'synthetic-build-20261030',
  });
  assert.equal(secondActivation.status, 200);
  assert.equal(secondActivation.value.data.licenseId, licenseID);
  const movedDevices = await adminRequest(page, '/admin/offline-licenses/' + licenseID);
  assert.ok(movedDevices.value.data.license.devices.some(device =>
    device.deviceLabel === '合成设备一' && device.deviceState === 'transferred',
  ));
  assert.ok(movedDevices.value.data.license.devices.some(device =>
    device.deviceLabel === '合成设备二' && device.deviceState === 'current',
  ));

  const bundlePassword = 'Synthetic-transfer-bundle-password-2026';
  const backupPassword = 'Synthetic-full-backup-password-2026';
  const bundleFile = path.join(root, 'source.snailbundle');
  const backupFile = path.join(root, 'source.snailbackup');
  await page.getByRole('button', { name: '设置', exact: true }).click();
  await page.getByRole('tab', { name: '备份与数据', exact: true }).click();
  await page.getByRole('button', { name: '导出管理数据', exact: true }).click();
  let dataDialog = page.getByRole('dialog', { name: '导出管理数据', exact: true });
  await dataDialog.getByLabel('移交包密码', { exact: true }).fill(bundlePassword);
  await dataDialog.getByLabel('确认密码', { exact: true }).fill(bundlePassword);
  await saveDownload(app, dataDialog, bundleFile, '生成加密移交包');
  await expect(dataDialog).toHaveCount(0);
  const bundleBytes = assertEncryptedFile(bundleFile, [licenseCode]);
  const bundleResponse = await page.evaluate(async password => {
    const response = await fetch('/api/v1/admin/business-bundle/export', {
      method: 'POST',
      credentials: 'same-origin',
      cache: 'no-store',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password }),
    });
    const bytes = await response.arrayBuffer();
    return {
      status: response.status,
      cacheControl: response.headers.get('cache-control'),
      byteLength: bytes.byteLength,
    };
  }, bundlePassword);
  assert.equal(bundleResponse.status, 200);
  assert.equal(bundleResponse.cacheControl, 'no-store');
  assert.ok(bundleResponse.byteLength > 0);
  assert.ok(bundleBytes.length > 0);

  await page.getByRole('button', { name: '备份资料', exact: true }).click();
  dataDialog = page.getByRole('dialog', { name: '备份资料', exact: true });
  await dataDialog.getByLabel('备份密码', { exact: true }).fill(backupPassword);
  await dataDialog.getByLabel('确认密码', { exact: true }).fill(backupPassword);
  page.once('dialog', dialog => dialog.accept());
  const backupBytes = await saveDownload(app, dataDialog, backupFile, '生成备份');
  assert.ok(backupBytes.length > 0);
  assert.throws(() => JSON.parse(backupBytes.toString('utf8')));
  assert.equal(backupBytes.includes(Buffer.from(licenseCode)), false);
  await expect(dataDialog).toHaveCount(0);

  await page.getByRole('button', { name: '恢复备份', exact: true }).click();
  dataDialog = page.getByRole('dialog', { name: '恢复备份', exact: true });
  await dataDialog.getByLabel('加密备份文件', { exact: true }).setInputFiles(backupFile);
  await dataDialog.getByLabel('备份密码', { exact: true }).fill('Wrong-synthetic-backup-password');
  await dataDialog.getByRole('button', { name: '验证并预览', exact: true }).click();
  await expect(dataDialog.getByRole('alert')).toBeVisible();
  await expect(dataDialog.getByRole('button', { name: '确认恢复并重新登录', exact: true })).toHaveCount(0);
  await dataDialog.getByLabel('备份密码', { exact: true }).fill(backupPassword);
  await dataDialog.getByRole('button', { name: '验证并预览', exact: true }).click();
  await expect(dataDialog.getByRole('button', { name: '确认恢复并重新登录', exact: true })).toBeVisible();
  await dataDialog.getByLabel('当前管理密码', { exact: true }).fill('Synthetic-source-recovered-password-2026');
  await dataDialog.getByRole('button', { name: '确认恢复并重新登录', exact: true }).click();
  await expect(page.getByRole('button', { name: '登录', exact: true })).toBeVisible({ timeout: 60000 });
  await login(page, 'source_owner', 'Synthetic-source-recovered-password-2026');
  const restoredCode = await adminRequest(page, '/admin/offline-licenses/' + licenseID + '/code');
  assert.equal(restoredCode.status, 200);
  assert.ok(restoredCode.value.data.licenseCode === licenseCode, 'full restore preserves activation code');
  await page.getByRole('button', { name: '客户', exact: true }).click();
  await expect(page.getByRole('row').filter({ hasText: '同名合成客户' })).toHaveCount(2);

  await expect(page.getByRole('row').filter({ hasText: 'first@synthetic.invalid' })).toHaveCount(1);
  await expect(page.getByRole('row').filter({ hasText: 'second@synthetic.invalid' })).toHaveCount(1);
  const sourceSession = readSession(dataRoot);
  const sourceRuntime = JSON.parse(fs.readFileSync(path.join(dataRoot, 'runtime.json'), 'utf8'));
  assert.equal(sourceSession.state, 'running');
  assert.equal(postgresStatus(postgresRoot, dataRoot).status, 0);

  await page.getByRole('button', { name: '退出登录', exact: true }).click();
  await expect(page.getByRole('button', { name: '登录', exact: true })).toBeVisible();
  await closeWindowAndWait(app);
  app = null;
  assert.equal(processIsAlive(sourceSession.pid), true);
  await waitForHealth(sourceSession.address, { exitCode: null, signalCode: null }, sourceSession.setupToken, 5000);
  assert.equal(postgresStatus(postgresRoot, dataRoot).status, 0);

  app = await _electron.launch({
    executablePath,
    args: [mainPath],
    env: managerEnv(root, dataRoot, postgresRoot),
  });
  page = await app.firstWindow({ timeout: 45000 });
  assert.equal(readSession(dataRoot).pid, sourceSession.pid);
  await waitForHealth(sourceSession.address, { exitCode: null, signalCode: null }, sourceSession.setupToken, 5000);
  const reopenedRuntime = JSON.parse(fs.readFileSync(path.join(dataRoot, 'runtime.json'), 'utf8'));
  for (const key of ['databasePassword', 'jwtSecret', 'encryptionSecret']) {
    assert.equal(reopenedRuntime[key], sourceRuntime[key]);
  }
  await login(page, 'source_owner', 'Synthetic-source-recovered-password-2026');
  await page.getByRole('button', { name: '授权', exact: true }).click();
  await page.getByLabel('查询授权', { exact: true }).fill(licenseNumber);
  await page.getByRole('button', { name: '查询', exact: true }).click();
  await expect(page.getByRole('row').filter({ hasText: licenseNumber })).toHaveCount(1);

  await page.getByRole('button', { name: '客户', exact: true }).click();
  await page.getByRole('button', { name: '退出登录', exact: true }).click();
  await expect(page.getByRole('button', { name: '登录', exact: true })).toBeVisible();
  await closeWindowAndWait(app);
  app = null;

  fs.mkdirSync(path.join(root, 'target-appdata'));
  fs.mkdirSync(path.join(root, 'target-localappdata'));
  fs.mkdirSync(path.join(root, 'target-tmp'));
  const targetRoot = {
    appdata: path.join(root, 'target-appdata'),
    localappdata: path.join(root, 'target-localappdata'),
    tmp: path.join(root, 'target-tmp'),
  };
  function targetEnv() {
    const env = {
      ...process.env,
      APPDATA: targetRoot.appdata,
      LOCALAPPDATA: targetRoot.localappdata,
      TEMP: targetRoot.tmp,
      TMP: targetRoot.tmp,
      SNAIL_MANAGER_DEV_DATA: targetDataRoot,
      SNAIL_MANAGER_DEV_POSTGRES: postgresRoot,
    };
    delete env.ELECTRON_RUN_AS_NODE;
    delete env.SNAIL_MANAGER_DATA_ROOT;
    return env;
  }
  app = await _electron.launch({ executablePath, args: [mainPath], env: targetEnv() });
  page = await app.firstWindow({ timeout: 45000 });
  const targetRecovery = await setupOwner(
    app,
    page,
    'target_owner',
    'Synthetic-target-owner-password-2026',
    '目标合成厂商',
    '目标合成联系号',
  );
  assert.ok(targetRecovery !== recoveryCredential, 'separate owner setup receives its own recovery credential');
  await login(page, 'target_owner', 'Synthetic-target-owner-password-2026');
  await page.getByRole('button', { name: '设置', exact: true }).click();
  await page.getByRole('tab', { name: '备份与数据', exact: true }).click();
  await page.getByRole('button', { name: '导入管理数据', exact: true }).click();
  dataDialog = page.getByRole('dialog', { name: '导入管理数据', exact: true });
  await dataDialog.getByLabel('管理数据包', { exact: true }).setInputFiles(bundleFile);
  await dataDialog.getByLabel('移交包密码', { exact: true }).fill('Wrong-synthetic-transfer-password');
  await dataDialog.getByRole('button', { name: '验证并预览', exact: true }).click();
  await expect(dataDialog.getByRole('alert')).toBeVisible();
  await expect(dataDialog.getByRole('button', { name: '确认导入管理数据', exact: true })).toHaveCount(0);
  await dataDialog.getByLabel('移交包密码', { exact: true }).fill(bundlePassword);
  await dataDialog.getByRole('button', { name: '验证并预览', exact: true }).click();
  await expect(dataDialog.getByRole('button', { name: '确认导入管理数据', exact: true })).toBeVisible();
  await dataDialog.getByLabel('当前管理密码', { exact: true }).fill('Synthetic-target-owner-password-2026');
  await dataDialog.getByRole('button', { name: '确认导入管理数据', exact: true }).click();
  await expect(dataDialog).toHaveCount(0);
  await page.getByRole('button', { name: '客户', exact: true }).click();
  await expect(page.getByRole('row').filter({ hasText: '同名合成客户' })).toHaveCount(2);

  await page.getByRole('button', { name: '退出登录', exact: true }).click();
  await expect(page.getByRole('button', { name: '登录', exact: true })).toBeVisible();
  await page.getByLabel('管理用户名', { exact: true }).fill('source_owner');
  await page.getByLabel('密码', { exact: true }).fill('Synthetic-source-recovered-password-2026');
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await expect(page.getByRole('alert')).toHaveText('用户名或密码不正确');
  await page.getByLabel('管理用户名', { exact: true }).fill('target_owner');
  await page.getByLabel('密码', { exact: true }).fill('Synthetic-target-owner-password-2026');
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await expect(page.getByRole('heading', { name: '客户', exact: true })).toBeVisible();

  const imported = await adminRequest(page, '/admin/offline-licenses?q=' + encodeURIComponent(licenseNumber));
  assert.equal(imported.status, 200);
  const importedLicense = imported.value.data.items.find(item => item.licenseNumber === licenseNumber || item.id === licenseID);
  assert.ok(importedLicense);
  const importedCode = await adminRequest(page, '/admin/offline-licenses/' + importedLicense.id + '/code');
  assert.equal(importedCode.status, 200);
  assert.ok(importedCode.value.data.licenseCode === licenseCode, 'business bundle preserves activation code');

  const targetSession = readSession(targetDataRoot);
  await closeWindowAndWait(app);
  app = null;
  assert.equal(processIsAlive(targetSession.pid), true);
  await waitForHealth(targetSession.address, { exitCode: null, signalCode: null }, targetSession.setupToken, 5000);
  assert.equal(postgresStatus(postgresRoot, targetDataRoot).status, 0);
  stopOwnedHost(root, targetDataRoot, postgresRoot);
  stopOwnedHost(root, dataRoot, postgresRoot);
  assert.equal(postgresStatus(postgresRoot, targetDataRoot).status, 3);
  assert.equal(postgresStatus(postgresRoot, dataRoot).status, 3);
  console.log('T03_ONLINE_EVIDENCE', root);
});
