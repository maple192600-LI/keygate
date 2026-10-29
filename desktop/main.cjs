'use strict';
const { app, BrowserWindow, dialog, shell } = require('electron');
const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');
const { randomBytes } = require('node:crypto');
const { startRuntime, waitForHealth, systemExe } = require('./runtime.cjs');

const productName = '小云雀客户授权中心';
app.setName(productName);
process.stderr.write('MANAGER_STAGE name-set\n');
const serviceHostMode = process.argv.includes('--service-host');
const startupMode = process.argv.includes('--startup');
const stopRequested = process.argv.includes('--snail-stop');
const configuredDataRoot = process.env.SNAIL_MANAGER_DATA_ROOT || process.env.SNAIL_MANAGER_DEV_DATA;
const dataRoot = configuredDataRoot || (app.isPackaged ? path.join(app.getPath('appData'), 'SnailLicenseCenter') : undefined);
if (!dataRoot || !path.isAbsolute(dataRoot)) throw new Error('开发运行必须指定本任务隔离资料目录。');
app.setPath('userData', path.join(dataRoot, serviceHostMode ? 'service-host-profile' : 'window-profile'));
process.stderr.write('MANAGER_STAGE profile-set\n');

const sessionFile = path.join(dataRoot, 'runtime-session.json');
let window, runtime, starting = false, closing = false, shutdownComplete = false;
let windowRequested = !startupMode;
const primary = serviceHostMode || app.requestSingleInstanceLock();
process.stderr.write(`MANAGER_STAGE instance-lock ${primary}\n`);
if (!primary) app.quit();

function readSession() {
  let stat;
  try { stat = fs.lstatSync(sessionFile); }
  catch (error) { if (error.code === 'ENOENT') return null; throw error; }
  if (!stat.isFile() || stat.isSymbolicLink() || stat.size > 4096) throw new Error('后台服务状态文件无法安全读取，现有资料已保留。');
  const value = JSON.parse(fs.readFileSync(sessionFile, 'utf8'));
  if (value.formatVersion !== 1 || !['running', 'stopped'].includes(value.state)
      || !Number.isSafeInteger(value.pid) || value.pid < 1)
    throw new Error('后台服务状态文件格式无效，现有资料已保留。');
  if (value.state === 'running'
      && (!/^http:\/\/127\.0\.0\.1:\d{1,5}$/.test(value.address)
        || !/^[a-f0-9]{64}$/.test(value.setupToken)))
    throw new Error('后台服务状态文件不完整，现有资料已保留。');
  return value;
}

function processExists(pid) {
  try { process.kill(pid, 0); return true; }
  catch (error) { if (error.code === 'ESRCH') return false; if (error.code === 'EPERM') return true; throw error; }
}

function writeSession(value) {
  const temporary = `${sessionFile}.${process.pid}.${randomBytes(8).toString('hex')}.tmp`;
  const fd = fs.openSync(temporary, 'wx', 0o600);
  try { fs.writeFileSync(fd, JSON.stringify(value)); fs.fsyncSync(fd); }
  finally { fs.closeSync(fd); }
  fs.renameSync(temporary, sessionFile);
}

async function connectToService() {
  const session = readSession();
  if (!session || session.state !== 'running') return null;
  try {
    await waitForHealth(session.address, { exitCode: null, signalCode: null }, session.setupToken, 5000);
    return session;
  } catch (error) {
    if (processExists(session.pid)) throw new Error('后台服务进程仍在运行，但健康检查未通过；现有资料已保留。', { cause: error });
    return null;
  }
}

async function startServiceHost() {
  const args = app.isPackaged ? ['--service-host'] : [app.getAppPath(), '--service-host'];
  const commandArgs = args.map(arg => `"${arg}"`).join(' ');
  const script = `$ErrorActionPreference='Stop'; Start-Process -FilePath '${process.execPath.replaceAll("'", "''")}' -ArgumentList '${commandArgs.replaceAll("'", "''")}' -WindowStyle Hidden | Out-Null`;
  // Windows owns the long-lived host so Electron's native shutdown does not wait on its child handle.
  try {
    execFileSync(systemExe('WindowsPowerShell/v1.0/powershell.exe'), [
      '-NoProfile', '-NonInteractive', '-EncodedCommand', Buffer.from(script, 'utf16le').toString('base64'),
    ], {
      windowsHide: true,
      stdio: 'ignore',
      env: { ...process.env, SNAIL_MANAGER_DATA_ROOT: dataRoot },
      timeout: 10000,
    });
  } catch (cause) {
    throw new Error('后台服务进程未能启动，请检查服务日志；现有资料已保留。', { cause });
  }
  const deadline = Date.now() + 30000;
  while (Date.now() < deadline) {
    const session = readSession();
    if (session?.state === 'running') {
      try {
        await waitForHealth(session.address, { exitCode: null, signalCode: null }, session.setupToken, 3000);
        return session;
      } catch (error) {
        if (processExists(session.pid)) throw new Error('后台服务未能通过本机身份检查，现有资料已保留。', { cause: error });
      }
    }
    await new Promise(resolve => setTimeout(resolve, 200));
  }
  throw new Error('后台服务启动超时，请检查服务日志；现有资料已保留。');
}

async function ensureServiceHost() {
  return await connectToService() || await startServiceHost();
}

async function stopServiceHost() {
  const session = readSession();
  if (!session || session.state !== 'running') return;
  await waitForHealth(session.address, { exitCode: null, signalCode: null }, session.setupToken, 5000);
  const response = await fetch(`${session.address}/internal/shutdown`, {
    method: 'POST',
    headers: { 'X-Snail-Runtime-Token': session.setupToken },
    signal: AbortSignal.timeout(3000),
  });
  if (!response.ok) throw new Error('后台服务拒绝了停止请求，请保留管理端并检查服务状态。');
  const deadline = Date.now() + 20000;
  while (Date.now() < deadline) {
    const current = readSession();
    if (current?.state === 'stopped' || !processExists(session.pid)) return;
    await new Promise(resolve => setTimeout(resolve, 200));
  }
  throw new Error('后台服务尚未完成停止，请保留资料并重试。');
}

function createWindow(session) {
  window = new BrowserWindow({
    width: 1440,
    height: 940,
    minWidth: 1120,
    minHeight: 720,
    title: productName,
    icon: app.isPackaged ? path.join(process.resourcesPath, 'icon.ico') : path.join(__dirname, 'icon.ico'),
    backgroundColor: '#f7f7f8',
    webPreferences: { nodeIntegration: false, contextIsolation: true, sandbox: true, devTools: !app.isPackaged },
  });
  process.stderr.write('MANAGER_STAGE window-created\n');
  window.setMenu(null);
  window.webContents.setWindowOpenHandler(({ url }) => {
    if (url === 'https://keygate.app' || url === 'https://keygate.app/') void shell.openExternal(url);
    return { action: 'deny' };
  });
  window.webContents.on('will-navigate', (event, url) => { if (new URL(url).origin !== session.address) event.preventDefault(); });
  const canWriteClipboard = (contents, permission) => contents === window.webContents
    && permission === 'clipboard-sanitized-write'
    && new URL(contents.getURL()).origin === session.address;
  window.webContents.session.setPermissionCheckHandler((contents, permission, origin) =>
    Boolean(contents && canWriteClipboard(contents, permission) && origin === session.address));
  window.webContents.session.setPermissionRequestHandler((contents, permission, callback) =>
    callback(canWriteClipboard(contents, permission)));
  window.webContents.session.webRequest.onBeforeSendHeaders(
    { urls: [session.address + '/api/v1/local/setup'] },
    (details, callback) => {
      const headers = { ...details.requestHeaders };
      if (details.webContentsId === window.webContents.id && new URL(details.url).origin === session.address)
        headers['X-Snail-Setup-Token'] = session.setupToken;
      callback({ requestHeaders: headers });
    },
  );
  void window.loadURL(session.address);
  process.stderr.write('MANAGER_STAGE window-loaded\n');
}

app.on('second-instance', (_event, argv) => {
  if (argv.includes('--snail-stop')) {
    void stopServiceHost().then(() => app.quit()).catch(error => dialog.showErrorBox('后台服务尚未停止', error.message));
    return;
  }
  if (argv.includes('--startup')) return;
  windowRequested = true;
  if (window) { if (window.isMinimized()) window.restore(); window.focus(); }
});

const startup = app.whenReady().then(async () => {
  process.stderr.write('MANAGER_STAGE app-ready\n');
  if (!primary) return;
  if (serviceHostMode) {
    starting = true;
    const previous = readSession();
    if (previous?.state === 'running' && processExists(previous.pid)) {
      shutdownComplete = true;
      app.quit();
      return;
    }
    runtime = await startRuntime({
      dataRoot,
      postgresRoot: app.isPackaged ? path.join(process.resourcesPath, 'postgresql') : process.env.SNAIL_MANAGER_DEV_POSTGRES,
      serverRoot: app.isPackaged ? path.join(process.resourcesPath, 'server') : path.resolve(__dirname, '..'),
      nativePaths: app.isPackaged,
      serverExecutable: path.join(app.isPackaged ? path.join(process.resourcesPath, 'server') : path.resolve(__dirname, '..'), 'bin', 'snail-license-center.exe'),
    });
    const existing = readSession();
    if (existing?.state === 'running' && processExists(existing.pid)) {
      await runtime.stop();
      runtime = null;
      throw new Error('另一后台服务实例已运行，未覆盖其状态。');
    }
    writeSession({ formatVersion: 1, state: 'running', pid: process.pid, address: runtime.address, setupToken: runtime.setupToken });
    process.stderr.write('MANAGER_STAGE runtime-ready\n');
    await runtime.exited;
    await runtime.stop();
    runtime = null;
    writeSession({ formatVersion: 1, state: 'stopped', pid: process.pid });
    shutdownComplete = true;
    app.quit();
    return;
  }
  if (stopRequested) {
    await stopServiceHost();
    app.quit();
    return;
  }
  const session = await ensureServiceHost();
  if (startupMode && !windowRequested) { app.quit(); return; }
  createWindow(session);
}).finally(() => { starting = false; }).catch(error => {
  process.stderr.write(`MANAGER_START_FAILED ${error.name} ${error.code || 'none'} ${error.cause?.code || 'none'}\n`);
  if (!serviceHostMode && !stopRequested) dialog.showErrorBox('小云雀客户授权中心未能启动', `${error.message}\n现有资料保留。`);
  app.quit();
});

app.on('window-all-closed', () => app.quit());
app.on('before-quit', event => {
  if (serviceHostMode && runtime && !shutdownComplete) {
    event.preventDefault();
    if (closing) return;
    closing = true;
    startup.then(() => runtime?.stop()).then(() => {
      const current = readSession();
      if (current?.pid === process.pid) writeSession({ formatVersion: 1, state: 'stopped', pid: process.pid });
      shutdownComplete = true;
      app.quit();
    }).catch(error => {
      closing = false;
      dialog.showErrorBox('后台服务尚未正常关闭', error.message);
    });
  }
});
