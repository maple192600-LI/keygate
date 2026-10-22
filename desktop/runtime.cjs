'use strict';
const fs = require('node:fs');
const path = require('node:path');
const net = require('node:net');
const { randomBytes } = require('node:crypto');
const { spawn, execFileSync } = require('node:child_process');

function directory(target) {
  fs.mkdirSync(target, { recursive: true });
  const stat = fs.lstatSync(target);
  if (!stat.isDirectory() || stat.isSymbolicLink() || path.resolve(fs.realpathSync.native(target)).toLowerCase() !== path.resolve(target).toLowerCase())
    throw new Error('运行资料目录不是安全的本机目录，请保留资料并检查路径。');
}
function systemExe(name) {
  const root = process.env.SystemRoot || process.env.WINDIR;
  if (!root || !path.isAbsolute(root)) throw new Error('Windows 系统路径缺失，请正常启动管理端。');
  return path.join(root, 'System32', name);
}
function secureDirectory(target) {
  if (process.platform !== 'win32') throw new Error('本管理客户端仅支持 Windows。');
  const user = execFileSync(systemExe('whoami.exe'), ['/user', '/fo', 'csv', '/nh'], { windowsHide: true, encoding: 'utf8' });
  const sid = /S-1-\d+(?:-\d+)+/.exec(user)?.[0];
  if (!sid) throw new Error('无法取得本机资料访问身份。');
  execFileSync(systemExe('icacls.exe'), [target, '/inheritance:r', '/grant:r', `*${sid}:(OI)(CI)F`], { windowsHide: true, stdio: 'pipe' });
}
async function freePort(preferred) {
  return new Promise((resolve, reject) => {
    const listener = net.createServer();
    listener.once('error', reject);
    listener.listen(preferred, '127.0.0.1', () => { const port = listener.address().port; listener.close(error => error ? reject(error) : resolve(port)); });
  });
}
function readSecrets(file) {
  const stat = fs.lstatSync(file);
  if (!stat.isFile() || stat.isSymbolicLink() || stat.size > 16384) throw new Error('运行配置文件不完整，请保留资料并恢复经验证的备份。');
  const value = JSON.parse(fs.readFileSync(file, 'utf8'));
  if (value.formatVersion !== 1 || !['databasePassword', 'jwtSecret', 'encryptionSecret'].every(key => /^[a-f0-9]{64}$/.test(value[key]))
      || ![value.databasePort, value.appPort].every(port => Number.isInteger(port) && port > 1024 && port < 65536) || value.databasePort === value.appPort)
    throw new Error('运行秘密配置无效，请保留原文件，不要重新初始化已有资料。');
  return value;
}
async function secrets(dataRoot) {
  directory(dataRoot); secureDirectory(dataRoot);
  const file = path.join(dataRoot, 'runtime.json');
  if (fs.existsSync(file)) return readSecrets(file);
  if (fs.existsSync(path.join(dataRoot, 'postgres'))) throw new Error('已有数据库但运行秘密缺失，请恢复配套备份；不能自动换密码。');
  let appPort;
  try { appPort = await freePort(9000); } catch { appPort = await freePort(0); }
  const value = { formatVersion: 1, databasePassword: randomBytes(32).toString('hex'), jwtSecret: randomBytes(32).toString('hex'),
    encryptionSecret: randomBytes(32).toString('hex'), appPort, databasePort: await freePort(0) };
  const fd = fs.openSync(file, 'wx', 0o600);
  try { fs.writeFileSync(fd, JSON.stringify(value)); fs.fsyncSync(fd); } finally { fs.closeSync(fd); }
  return readSecrets(file);
}
function execute(executable, args, options = {}) {
  try { return execFileSync(executable, args, { encoding: 'utf8', windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'], ...options }); }
  catch (cause) { throw new Error(`本地运行步骤失败：${path.basename(executable)}。请检查资料目录中的运行日志。`, { cause }); }
}
async function waitForHealth(address, child, deadline = 30000) {
  const until = Date.now() + deadline;
  while (Date.now() < until) {
    if (child.exitCode !== null) throw new Error('本地管理服务启动失败，请查看运行日志。');
    try {
      const response = await fetch(`${address}/health`, { signal: AbortSignal.timeout(1000) });
      const value = await response.json();
      if (response.ok && value.data?.deployment === 'local_offline') return;
    } catch { /* readiness is checked again within the bounded deadline */ }
    await new Promise(resolve => setTimeout(resolve, 200));
  }
  throw new Error('本地管理服务未能就绪，请查看运行日志；现有资料保留。');
}

async function startRuntime({ dataRoot, postgresRoot, serverRoot, serverExecutable }) {
  // No database process or web service may start before these secrets exist.
  const config = await secrets(dataRoot);
  const cluster = path.join(dataRoot, 'postgres');
  const logs = path.join(dataRoot, 'logs'); directory(logs);
  const pg = name => path.join(postgresRoot, 'bin', `${name}.exe`);
  if (!fs.existsSync(path.join(cluster, 'PG_VERSION'))) {
    if (fs.existsSync(cluster) && fs.readdirSync(cluster).length) throw new Error('数据库初始化尚未完成，请保留资料并联系支持；不会清空目录。');
    const passwordFile = path.join(dataRoot, `init-${randomBytes(16).toString('hex')}.tmp`);
    fs.writeFileSync(passwordFile, config.databasePassword, { flag: 'wx', mode: 0o600 });
    try { execute(pg('initdb'), ['-D', cluster, '-U', 'snail_runtime', '--pwfile', passwordFile, '--auth-host=scram-sha-256', '--auth-local=scram-sha-256', '--encoding=UTF8', '--locale=C']); }
    finally { fs.unlinkSync(passwordFile); }
  }
  let running = false;
  try { execFileSync(pg('pg_ctl'), ['status', '-D', cluster], { windowsHide: true, stdio: 'ignore' }); running = true; } catch (error) { if (error.status !== 3) throw new Error('无法确认本机数据库状态，请保留资料并检查运行日志。'); }
  // On Windows pg_ctl's detached postgres child can inherit captured pipe
  // handles even with -l. No captured pipes may outlive this startup command.
  if (!running) execute(pg('pg_ctl'), ['start', '-D', cluster, '-l', path.join(logs, 'postgres.log'), '-o', `-h 127.0.0.1 -p ${config.databasePort} -c timezone=UTC`, '-w', '-t', '20'], { stdio: 'ignore' });
  let stop = async () => execute(pg('pg_ctl'), ['stop', '-D', cluster, '-m', 'fast', '-w', '-t', '20']);
  try {
  const databaseArgs = ['-h', '127.0.0.1', '-p', String(config.databasePort), '-U', 'snail_runtime'];
  const pgEnv = { ...process.env, PGPASSWORD: config.databasePassword };
  const exists = execute(pg('psql'), [...databaseArgs, '-d', 'postgres', '-tAc', "SELECT 1 FROM pg_database WHERE datname='snail_license_center'"], { env: pgEnv });
  if (exists.trim() !== '1') execute(pg('createdb'), [...databaseArgs, 'snail_license_center'], { env: pgEnv });
  await freePort(config.appPort);
  const address = `http://127.0.0.1:${config.appPort}`;
  const shutdownToken = randomBytes(32).toString('hex');
  const log = fs.openSync(path.join(logs, 'server.log'), 'a', 0o600);
  let child;
  try {
    child = spawn(serverExecutable, [], { cwd: serverRoot, windowsHide: true, stdio: ['ignore', log, log], env: { ...process.env,
      SNAIL_DEPLOYMENT: 'local_offline', SNAIL_DATA_DIR: dataRoot, SNAIL_RUNTIME_SHUTDOWN_TOKEN: shutdownToken,
      ENVIRONMENT: 'production', PORT: String(config.appPort), BASE_URL: address,
      DATABASE_URL: `postgres://snail_runtime:${config.databasePassword}@127.0.0.1:${config.databasePort}/snail_license_center?sslmode=disable`,
      JWT_SECRET: config.jwtSecret, RELEASE_KEY_ENCRYPTION_KEY: config.encryptionSecret,
      LICENSE_SIGNING_KEY: '', ADMIN_EMAILS: '', SMTP_HOST: '', SMTP_PASSWORD: '', REDIS_URL: '',
      STRIPE_SECRET_KEY: '', STRIPE_WEBHOOK_SECRET: '', STORAGE_BUCKET: '', STORAGE_ACCESS_KEY: '', STORAGE_SECRET_KEY: '', STORAGE_ENDPOINT: '' } });
  } finally { fs.closeSync(log); }
  let spawnError;
  child.on('error', error => { spawnError = error; });
  const exited = new Promise(resolve => { child.once('exit', resolve); child.once('error', resolve); });
  stop = async function stopRuntime() {
    if (child.exitCode === null && !spawnError) {
      await fetch(`${address}/internal/shutdown`, { method: 'POST', headers: { 'X-Snail-Runtime-Token': shutdownToken }, signal: AbortSignal.timeout(3000) });
      let timer;
      await Promise.race([exited, new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('管理服务尚未正常关闭，请保留窗口并重试。')), 15000); })]).finally(() => clearTimeout(timer));
    }
    execute(pg('pg_ctl'), ['stop', '-D', cluster, '-m', 'fast', '-w', '-t', '20']);
  };
  await waitForHealth(address, child); if (spawnError) throw spawnError;
  return { address, stop };
  } catch (error) { try { await stop(); } catch (shutdownError) { throw new AggregateError([error, shutdownError], '管理端启动及关闭失败，请保留运行日志与资料。'); }; throw error; }
}
module.exports = { startRuntime, secrets };
