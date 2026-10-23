'use strict';
const fs = require('node:fs');
const path = require('node:path');
const net = require('node:net');
const { randomBytes, createHmac, createHash, timingSafeEqual } = require('node:crypto');
const { spawn, execFileSync } = require('node:child_process');
const runtimeConfigDigest = Symbol('runtimeConfigDigest');

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
  process.stderr.write('MANAGER_STAGE acl-start\n');
  if (process.platform !== 'win32') throw new Error('本管理客户端仅支持 Windows。');
  const sid = currentUserSID();
  execFileSync(systemExe('icacls.exe'), [target, '/inheritance:r', '/grant:r', `*${sid}:(OI)(CI)F`], { windowsHide: true, stdio: 'pipe' });
  process.stderr.write('MANAGER_STAGE acl-done\n');
}
function currentUserSID() {
  const user = execFileSync(systemExe('whoami.exe'), ['/user', '/fo', 'csv', '/nh'], { windowsHide: true, encoding: 'utf8' });
  const sid = /S-1-\d+(?:-\d+)+/.exec(user)?.[0];
  if (!sid) throw new Error('无法取得本机资料访问身份。');
  return sid;
}
function privateMappingDirectory(target, sid) {
  try { fs.lstatSync(target); throw new Error('运行辅助路径已经存在，已停止使用。'); }
  catch (error) { if (error.code !== 'ENOENT') throw error; }
  const literal = value => "'" + value.replaceAll("'", "''") + "'";
  const script = `$ErrorActionPreference='Stop'; $p=${literal(target)}; $sid=${literal(sid)};
$security=New-Object System.Security.AccessControl.DirectorySecurity;
$security.SetSecurityDescriptorSddlForm(('O:'+ $sid +'D:P(A;OICI;FA;;;'+ $sid +')'));
[System.IO.Directory]::CreateDirectory($p,$security) | Out-Null;
if (([System.IO.File]::GetAttributes($p) -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {throw 'Unexpected link'};
$actual=[System.IO.Directory]::GetAccessControl($p); $rules=$actual.GetAccessRules($true,$true,[System.Security.Principal.SecurityIdentifier]);
if ($actual.GetOwner([System.Security.Principal.SecurityIdentifier]).Value -ne $sid -or -not $actual.AreAccessRulesProtected -or $rules.Count -ne 1 -or $rules[0].IdentityReference.Value -ne $sid -or $rules[0].AccessControlType -ne [System.Security.AccessControl.AccessControlType]::Allow -or $rules[0].FileSystemRights -ne [System.Security.AccessControl.FileSystemRights]::FullControl) {throw 'Unexpected directory owner or access rules'};`;
  try { execFileSync(systemExe('WindowsPowerShell/v1.0/powershell.exe'), ['-NoProfile', '-NonInteractive', '-EncodedCommand', Buffer.from(script, 'utf16le').toString('base64')], { windowsHide: true, stdio: 'pipe', timeout: 10000 }); }
  catch (cause) { throw new Error('无法安全创建数据库辅助路径，请保留资料并联系支持。', { cause }); }
  directory(target);
}
function nativePostgresPaths(dataRoot, postgresRoot) {
  // PostgreSQL's Windows bootstrap emits native-codepage installation paths
  // into UTF-8 SQL. ASCII junctions retain all original program and data paths.
  const common = process.env.ProgramData;
  if (!common || !path.isAbsolute(common) || /[^\x20-\x7e]/.test(common) || !fs.statSync(common).isDirectory())
    throw new Error('系统公共资料路径无法用于数据库启动，请保留资料并联系支持。');
  const sid = currentUserSID(), aliases = path.join(common, `SnailLicenseCenter-${sid}-${randomBytes(16).toString('hex')}`);
  privateMappingDirectory(aliases, sid);
  const targets = [['data', dataRoot], ['postgresql', postgresRoot]];
  for (const [name, target] of targets) {
    const alias = path.join(aliases, name);
    fs.symlinkSync(target, alias, 'junction');
    if (fs.realpathSync.native(alias).toLowerCase() !== fs.realpathSync.native(target).toLowerCase())
      throw new Error('数据库路径映射验证失败；现有资料保留。');
  }
  let released = false;
  return { dataRoot: path.join(aliases, 'data'), postgresRoot: path.join(aliases, 'postgresql'), release() {
    if (released) return;
    const entries = fs.readdirSync(aliases).sort();
    if (entries.length !== 2 || entries[0] !== 'data' || entries[1] !== 'postgresql') throw new Error('运行辅助路径出现未知对象，已保留目录。');
    for (const [name, target] of targets) {
      const alias = path.join(aliases, name);
      if (!fs.lstatSync(alias).isSymbolicLink() || fs.realpathSync.native(alias).toLowerCase() !== fs.realpathSync.native(target).toLowerCase())
        throw new Error('运行辅助路径发生变化，已保留目录。');
    }
    for (const [name] of targets) fs.unlinkSync(path.join(aliases, name));
    fs.rmdirSync(aliases); released = true;
  } };
}
async function freePort(preferred) {
  process.stderr.write('MANAGER_STAGE port-request\n');
  return new Promise((resolve, reject) => {
    const listener = net.createServer();
    listener.once('error', reject);
    listener.listen(preferred, '127.0.0.1', () => { process.stderr.write('MANAGER_STAGE port-listening\n'); const port = listener.address().port; listener.close(error => { process.stderr.write('MANAGER_STAGE port-released\n'); error ? reject(error) : resolve(port); }); });
  });
}
function readSecrets(file) {
  const stat = fs.lstatSync(file);
  if (!stat.isFile() || stat.isSymbolicLink() || stat.size > 16384) throw new Error('运行配置文件不完整，请保留资料并恢复经验证的备份。');
  const raw = fs.readFileSync(file);
  const value = JSON.parse(raw.toString('utf8'));
  if (value.formatVersion !== 1 || !['databasePassword', 'jwtSecret', 'encryptionSecret'].every(key => /^[a-f0-9]{64}$/.test(value[key]))
      || ![value.databasePort, value.appPort].every(port => Number.isInteger(port) && port > 1024 && port < 65536) || value.databasePort === value.appPort
      || (value.databaseName !== undefined && !/^(snail_license_center|snail_restore_[a-f0-9]{32})$/.test(value.databaseName))
      || (value.identityDirectory !== undefined && !/^restores\/[a-f0-9-]{36}$/.test(value.identityDirectory)))
    throw new Error('运行秘密配置无效，请保留原文件，不要重新初始化已有资料。');
  Object.defineProperty(value, runtimeConfigDigest, { value: createHash('sha256').update(raw).digest('hex') });
  return value;
}
async function availableRuntimePort(dataRoot, config, key) {
  try { await freePort(config[key]); return config; }
  catch (error) { if (error.code !== 'EADDRINUSE') throw error; }
  let port;
  do { port = await freePort(0); } while (port === config.appPort || port === config.databasePort);
  const file = path.join(dataRoot, 'runtime.json'), temporary = path.join(dataRoot, 'runtime-' + randomBytes(16).toString('hex') + '.tmp');
  const value = { ...config, [key]: port };
  const fd=fs.openSync(temporary,'wx',0o600);
  try { fs.writeFileSync(fd,JSON.stringify(value));fs.fsyncSync(fd); } finally {fs.closeSync(fd);}
  try {
    if (readSecrets(file)[runtimeConfigDigest] !== config[runtimeConfigDigest]) throw new Error('运行配置已变化，请重新启动；原资料保留。');
    fs.renameSync(temporary,file);
    return readSecrets(file);
  } finally {if(fs.existsSync(temporary))fs.unlinkSync(temporary);}
}
async function secrets(dataRoot) {
  directory(dataRoot); secureDirectory(dataRoot);
  process.stderr.write('MANAGER_STAGE secrets-root-ready\n');
  const file = path.join(dataRoot, 'runtime.json');
  if (fs.existsSync(file)) return availableRuntimePort(dataRoot, readSecrets(file), 'appPort');
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
async function waitForHealth(address, child, token, deadline = 30000) {
  process.stderr.write('MANAGER_STAGE health-wait-start\n');
  const until = Date.now() + deadline;
  while (Date.now() < until) {
    if (child.exitCode !== null || child.signalCode) throw new Error('本地管理服务启动失败，请查看运行日志。');
    try {
      const challenge = randomBytes(32);
      const response = await fetch(`${address}/health?challenge=${challenge.toString('hex')}`, { signal: AbortSignal.timeout(1000) });
      process.stderr.write(`MANAGER_STAGE health-response ${response.status}\n`);
      const value = await response.json();
      if (response.ok && value.data?.deployment === 'local_offline') {
        const expected = createHmac('sha256', Buffer.from(token, 'hex')).update('snail-runtime-ready/v1\0').update(challenge).digest();
        const proof = value.data.proof;
        if (typeof proof !== 'string' || !/^[0-9a-f]{64}$/.test(proof) || !timingSafeEqual(expected, Buffer.from(proof, 'hex')))
          throw Object.assign(new Error('本地服务身份未通过校验，已停止打开管理窗口。'), { code: 'LOCAL_RUNTIME_UNTRUSTED' });
        if (child.exitCode !== null || child.signalCode) throw new Error('本地管理服务已经退出，未打开管理窗口。');
        process.stderr.write('MANAGER_STAGE health-verified\n');
        return;
      }
    } catch (error) { if (error.code === 'LOCAL_RUNTIME_UNTRUSTED') throw error; /* Bounded readiness retry. */ }
    await new Promise(resolve => setTimeout(resolve, 200));
  }
  throw new Error('本地管理服务未能就绪，请查看运行日志；现有资料保留。');
}

async function startRuntime({ dataRoot, postgresRoot, serverRoot, serverExecutable, nativePaths = false }) {
  // No database process or web service may start before these secrets exist.
  let config = await secrets(dataRoot);
  process.stderr.write('MANAGER_STAGE config-ready\n');
  const native = nativePaths ? nativePostgresPaths(dataRoot, postgresRoot) : { dataRoot, postgresRoot };
  const cluster = path.join(native.dataRoot, 'postgres');
  const logs = path.join(dataRoot, 'logs'); directory(logs);
  const pg = name => path.join(native.postgresRoot, 'bin', `${name}.exe`);
  if (!fs.existsSync(path.join(cluster, 'PG_VERSION'))) {
    if (fs.existsSync(cluster) && fs.readdirSync(cluster).length) throw new Error('数据库初始化尚未完成，请保留资料并联系支持；不会清空目录。');
    const passwordFile = path.join(native.dataRoot, `init-${randomBytes(16).toString('hex')}.tmp`);
    fs.writeFileSync(passwordFile, config.databasePassword, { flag: 'wx', mode: 0o600 });
    const initializationLog = fs.openSync(path.join(logs, 'initialization.log'), 'a', 0o600);
    try { process.stderr.write('MANAGER_STAGE initdb-start\n'); execute(pg('initdb'), ['-D', cluster, '-U', 'snail_runtime', '--pwfile', passwordFile, '--auth-host=scram-sha-256', '--auth-local=scram-sha-256', '--encoding=UTF8', '--locale=C'], { stdio: ['ignore', initializationLog, initializationLog], timeout: 30000 }); process.stderr.write('MANAGER_STAGE initdb-done\n'); }
    finally { fs.closeSync(initializationLog); fs.unlinkSync(passwordFile); }
  }
  let running = false;
  try { execFileSync(pg('pg_ctl'), ['status', '-D', cluster], { windowsHide: true, stdio: 'ignore' }); running = true; } catch (error) { if (error.status !== 3) throw new Error('无法确认本机数据库状态，请保留资料并检查运行日志。'); }
  // On Windows pg_ctl's detached postgres child can inherit captured pipe
  // handles even with -l. No captured pipes may outlive this startup command.
  if (!running) config = await availableRuntimePort(dataRoot, config, 'databasePort');
  if (!running) execute(pg('pg_ctl'), ['start', '-D', cluster, '-l', path.join(native.dataRoot, 'logs', 'postgres.log'), '-o', `-h 127.0.0.1 -p ${config.databasePort} -c timezone=UTC`, '-w', '-t', '20'], { stdio: 'ignore' });
  let databaseStopped = false;
  function stopDatabase() {
    if (!databaseStopped) {
      let running = true;
      try { execFileSync(pg('pg_ctl'), ['status', '-D', cluster], { windowsHide: true, stdio: 'ignore' }); }
      catch (error) { if (error.status !== 3) throw error; running = false; }
      if (running) execute(pg('pg_ctl'), ['stop', '-D', cluster, '-m', 'fast', '-w', '-t', '20']);
      databaseStopped = true;
    }
    try { native.release?.(); }
    catch (error) {
      const cleanupWarning = { code: 'NATIVE_MAPPING_RETAINED', directory: path.dirname(native.dataRoot), message: error.message };
      process.stderr.write(`MANAGER_CLEANUP_WARNING ${JSON.stringify(cleanupWarning)}\n`);
      return { cleanupWarning };
    }
  }
  let stop = async () => stopDatabase();
  try {
  const databaseArgs = ['-h', '127.0.0.1', '-p', String(config.databasePort), '-U', 'snail_runtime'];
  const databaseName = config.databaseName || 'snail_license_center';
  const pgEnv = { ...process.env, PGPASSWORD: config.databasePassword };
  const exists = execute(pg('psql'), [...databaseArgs, '-d', 'postgres', '-tAc', `SELECT 1 FROM pg_database WHERE datname='${databaseName}'`], { env: pgEnv });
  if (exists.trim() !== '1') {
    if (config.databaseName) throw new Error('恢复后的数据库不存在，请保留运行配置和原资料，不要重新初始化。');
    execute(pg('createdb'), [...databaseArgs, databaseName], { env: pgEnv });
  }
  config = await availableRuntimePort(dataRoot, config, 'appPort');
  const address = `http://127.0.0.1:${config.appPort}`;
  const shutdownToken = randomBytes(32).toString('hex');
  const log = fs.openSync(path.join(logs, 'server.log'), 'a', 0o600);
  let child;
  try {
    child = spawn(serverExecutable, [], { cwd: serverRoot, windowsHide: true, stdio: ['ignore', log, log], env: { ...process.env,
      SNAIL_DEPLOYMENT: 'local_offline', SNAIL_DATA_DIR: dataRoot, SNAIL_RUNTIME_SHUTDOWN_TOKEN: shutdownToken,
      SNAIL_RUNTIME_CONFIG_SHA256: config[runtimeConfigDigest],
      SNAIL_IDENTITY_DIR: path.resolve(dataRoot, config.identityDirectory || '.'),
      ENVIRONMENT: 'production', PORT: String(config.appPort), BASE_URL: address,
      DATABASE_URL: `postgres://snail_runtime:${config.databasePassword}@127.0.0.1:${config.databasePort}/${databaseName}?sslmode=disable`,
      JWT_SECRET: config.jwtSecret, RELEASE_KEY_ENCRYPTION_KEY: config.encryptionSecret,
      LICENSE_SIGNING_KEY: '', ADMIN_EMAILS: '', SMTP_HOST: '', SMTP_PASSWORD: '', REDIS_URL: '',
      STRIPE_SECRET_KEY: '', STRIPE_WEBHOOK_SECRET: '', STORAGE_BUCKET: '', STORAGE_ACCESS_KEY: '', STORAGE_SECRET_KEY: '', STORAGE_ENDPOINT: '' } });
  } finally { fs.closeSync(log); }
  process.stderr.write('MANAGER_STAGE server-spawned\n');
  let spawnError, ready = false;
  child.on('error', error => { spawnError = error; });
  const exited = new Promise(resolve => { child.once('exit', resolve); child.once('error', resolve); });
  stop = async function stopRuntime() {
    if (child.exitCode === null && !spawnError) {
      if (ready) await fetch(`${address}/internal/shutdown`, { method: 'POST', headers: { 'X-Snail-Runtime-Token': shutdownToken }, signal: AbortSignal.timeout(3000) });
      else child.kill(); // Only our unverified child; never trust a competing HTTP listener.
      let timer;
      await Promise.race([exited, new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('管理服务尚未正常关闭，请保留窗口并重试。')), 15000); })]).finally(() => clearTimeout(timer));
    }
    return stopDatabase();
  };
  await waitForHealth(address, child, shutdownToken); if (spawnError) throw spawnError; ready = true;
  return { address, stop, setupToken: shutdownToken };
  } catch (error) { try { await stop(); } catch (shutdownError) { throw new AggregateError([error, shutdownError], '管理端启动及关闭失败，请保留运行日志与资料。'); }; throw error; }
}
module.exports = { startRuntime, secrets, availableRuntimePort, waitForHealth, nativePostgresPaths, privateMappingDirectory };
