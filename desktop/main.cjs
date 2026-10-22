'use strict';
const { app, BrowserWindow, dialog, shell } = require('electron');
const path = require('node:path');
const { startRuntime } = require('./runtime.cjs');
app.setName('蜗牛客户与授权中心');
process.stderr.write('MANAGER_STAGE name-set\n');
const dataRoot = app.isPackaged ? path.join(app.getPath('appData'), 'SnailLicenseCenter') : process.env.SNAIL_MANAGER_DEV_DATA;
if (!dataRoot || !path.isAbsolute(dataRoot)) throw new Error('开发运行必须指定本任务隔离资料目录。');
app.setPath('userData', path.join(dataRoot, 'window-profile'));
process.stderr.write('MANAGER_STAGE profile-set\n');
let window, runtime, starting = false, closing = false, shutdownComplete = false;
const stopRequested = process.argv.includes('--snail-stop');
const primary = app.requestSingleInstanceLock();
process.stderr.write(`MANAGER_STAGE instance-lock ${primary}\n`);
if (!primary || stopRequested) app.quit();
app.on('second-instance', (_event, argv) => {
  if (argv.includes('--snail-stop')) { app.quit(); return; }
  if (window) { if (window.isMinimized()) window.restore(); window.focus(); }
});
const startup = app.whenReady().then(async () => {
  process.stderr.write('MANAGER_STAGE app-ready\n');
  if (!primary || stopRequested) return;
  starting = true;
  const serverRoot = app.isPackaged ? path.join(process.resourcesPath, 'server') : path.resolve(__dirname, '..');
  const postgresRoot = app.isPackaged ? path.join(process.resourcesPath, 'postgresql') : process.env.SNAIL_MANAGER_DEV_POSTGRES;
  runtime = await startRuntime({ dataRoot, postgresRoot, serverRoot, serverExecutable: path.join(serverRoot, 'bin', 'snail-license-center.exe') });
  if (closing) return;
  window = new BrowserWindow({ width: 1440, height: 940, minWidth: 1120, minHeight: 720, title: '蜗牛客户与授权中心', backgroundColor: '#f7f7f8',
    webPreferences: { nodeIntegration: false, contextIsolation: true, sandbox: true, devTools: !app.isPackaged } });
  window.setMenu(null);
  window.webContents.setWindowOpenHandler(({ url }) => { if (url === 'https://keygate.app' || url === 'https://keygate.app/') void shell.openExternal(url); return { action: 'deny' }; });
  window.webContents.on('will-navigate', (event, url) => { if (new URL(url).origin !== runtime.address) event.preventDefault(); });
  const canWriteClipboard = (contents, permission) => contents === window.webContents && permission === 'clipboard-sanitized-write' && new URL(contents.getURL()).origin === runtime.address;
  window.webContents.session.setPermissionCheckHandler((contents, permission, origin) => Boolean(contents && canWriteClipboard(contents, permission) && origin === runtime.address));
  window.webContents.session.setPermissionRequestHandler((contents, permission, callback) => callback(canWriteClipboard(contents, permission)));
  window.webContents.session.webRequest.onBeforeSendHeaders({ urls: [runtime.address + '/api/v1/local/setup'] }, (details, callback) => {
    const headers = { ...details.requestHeaders };
    if (details.webContentsId === window.webContents.id && new URL(details.url).origin === runtime.address)
      headers['X-Snail-Setup-Token'] = runtime.setupToken;
    callback({ requestHeaders: headers });
  });
  await window.loadURL(runtime.address);
}).finally(() => { starting = false; }).catch(error => { dialog.showErrorBox('管理端未能启动', `${error.message}\n现有资料保留。`); app.quit(); });
app.on('window-all-closed', () => app.quit());
app.on('before-quit', event => {
  if (shutdownComplete || (!runtime && !starting)) return;
  event.preventDefault(); if (closing) return; closing = true;
  startup.then(() => runtime?.stop()).then(() => { shutdownComplete = true; app.quit(); }).catch(error => { closing = false; dialog.showErrorBox('管理端尚未正常关闭', error.message); });
});
