'use strict';
const fs = require('node:fs');
const path = require('node:path');
const {createHash} = require('node:crypto');
const {execFileSync} = require('node:child_process');
const {build, Platform} = require('electron-builder');
const root = path.resolve(__dirname, '..');
const managerVersion = require('./package.json').version;
function requiredDirectory(name) {
  const value = process.env[name];
  if (!value || !path.isAbsolute(value) || !fs.statSync(value).isDirectory()) throw new Error(`请指定完整目录 ${name}`);
  return value;
}
async function main() {
  const postgres = requiredDirectory('SNAIL_POSTGRES_RUNTIME');
  const crt = requiredDirectory('SNAIL_MSVC_RUNTIME');
  const nativeLock = require('./native-runtime.lock.json');
  for (const file of nativeLock.msvc.files) {
    const bytes=fs.readFileSync(path.join(crt,'dlls',file.file));
    if(createHash('sha256').update(bytes).digest('hex')!==file.sha256) throw new Error(`微软运行库与固定清单不符 ${file.file}`);
  }
  for(const file of ['Microsoft-VCRuntime-LICENSE.txt','Microsoft-VCRuntime-LICENSE.docx'])
    if(!fs.statSync(path.join(crt,file)).isFile()) throw new Error(`缺少运行库许可 ${file}`);
  const output = requiredDirectory('SNAIL_MANAGER_OUTPUT');
  const candidate = execFileSync('git', ['rev-parse', 'HEAD'], {cwd:root,encoding:'utf8',windowsHide:true}).trim();
  if(execFileSync('git',['status','--porcelain'],{cwd:root,encoding:'utf8',windowsHide:true}).trim()) throw new Error('先固定干净源码候选，再构建交付包。');
  for(const name of ['SNAIL_GO_EXECUTABLE','SNAIL_BUN_EXECUTABLE'])
    if(!process.env[name] || !path.isAbsolute(process.env[name]) || !fs.statSync(process.env[name]).isFile()) throw new Error(`请指定完整构建工具路径 ${name}`);
  execFileSync(process.env.SNAIL_GO_EXECUTABLE,['build','-p','1','-trimpath','-o','bin/snail-license-center.exe','./cmd/server'],{cwd:root,stdio:'inherit',windowsHide:true,env:{...process.env,GOMAXPROCS:'2',GOOS:'windows',GOARCH:'amd64',CGO_ENABLED:'0'}});
  execFileSync(process.env.SNAIL_BUN_EXECUTABLE,['install','--frozen-lockfile'],{cwd:path.join(root,'web'),stdio:'inherit',windowsHide:true});
  execFileSync(process.env.SNAIL_BUN_EXECUTABLE,['run','build'],{cwd:path.join(root,'web'),stdio:'inherit',windowsHide:true,env:{...process.env,VITE_SNAIL_DEPLOYMENT:'local_offline',VITE_SNAIL_MANAGER_VERSION:managerVersion}});
  for (const file of ['bin/snail-license-center.exe','web/dist/index.html','LICENSE','NOTICE','docs/蜗牛客户与授权中心_本地操作说明.md'])
    if (!fs.statSync(path.join(root,file)).isFile()) throw new Error(`缺少构建输入 ${file}`);
  for (const file of ['bin/postgres.exe','bin/pg_ctl.exe','bin/initdb.exe','bin/psql.exe','bin/createdb.exe','server_license.txt','commandlinetools_3rd_party_licenses.txt'])
    if (!fs.statSync(path.join(postgres,file)).isFile()) throw new Error(`缺少 PostgreSQL 运行文件或许可 ${file}`);
  // A candidate is never overlaid onto another package or user data directory.
  if(fs.readdirSync(output).length) throw new Error('交付输出目录必须为空；已有产物保留。');
  await build({projectDir:__dirname, targets:Platform.WINDOWS.createTarget(['dir','zip']), publish:'never', config:{
    appId:'cn.snailfinance.licensecenter',productName:'小云雀客户授权中心',asar:true,
    electronDist:'node_modules/electron/dist',
    electronFuses:{enableEmbeddedAsarIntegrityValidation:true,onlyLoadAppFromAsar:true,enableNodeOptionsEnvironmentVariable:false,enableNodeCliInspectArguments:false,runAsNode:false},
    files:['main.cjs','runtime.cjs','package.json'], directories:{output},
    extraMetadata:{snailSourceCandidate:candidate},
    extraResources:[
      {from:path.join(root,'bin/snail-license-center.exe'),to:'server/bin/snail-license-center.exe'},
      {from:path.join(root,'db/migrations'),to:'server/db/migrations',filter:['*.up.sql']},
      {from:path.join(root,'web/dist'),to:'server/web/dist'},
      {from:path.join(postgres,'bin'),to:'postgresql/bin',filter:['*.dll','postgres.exe','pg_ctl.exe','initdb.exe','psql.exe','createdb.exe']},
      {from:path.join(postgres,'lib'),to:'postgresql/lib',filter:['*.dll']},
      {from:path.join(postgres,'share'),to:'postgresql/share'},
      {from:path.join(crt,'dlls'),to:'postgresql/bin',filter:nativeLock.msvc.files.map(file=>file.file)},
      ...['Microsoft-VCRuntime-LICENSE.txt','Microsoft-VCRuntime-LICENSE.docx'].map(file=>({from:path.join(crt,file),to:`postgresql/${file}`})),
      {from:path.join(__dirname,'native-runtime.lock.json'),to:'native-runtime.lock.json'},
      ...['server_license.txt','commandlinetools_3rd_party_licenses.txt'].map(file=>({from:path.join(postgres,file),to:`postgresql/${file}`})),
      ...['LICENSE','NOTICE'].map(file=>({from:path.join(root,file),to:file})),
      {from:path.join(root,'docs/蜗牛客户与授权中心_本地操作说明.md'),to:'中文操作说明.md'},
    ],
    win:{target:['dir','zip'],artifactName:'小云雀客户授权中心-${version}-${arch}.${ext}',signAndEditExecutable:true},
    afterPack:context=>{
      for(const file of nativeLock.msvc.files) {
        const bytes=fs.readFileSync(path.join(context.appOutDir,'resources/postgresql/bin',file.file));
        if(createHash('sha256').update(bytes).digest('hex')!==file.sha256) throw new Error(`包内微软运行库校验失败 ${file.file}`);
      }
      for(const file of ['run.bat','stop.bat','configure-startup.ps1']) fs.writeFileSync(path.join(context.appOutDir,file),fs.readFileSync(path.join(__dirname,file),'utf8').replace(/\r?\n/g,'\r\n'),'utf8');
      fs.writeFileSync(path.join(context.appOutDir,'源码与许可.txt'),`本包为小云雀客户授权中心，基于 Keygate。\r\nPowered by Keygate：https://keygate.app\r\n上游 LICENSE 和 NOTICE 在 resources 目录，随附对应衍生源码与构建说明。\r\n源码候选：${candidate}\r\n运行资料保存在当前用户 AppData/Roaming/SnailLicenseCenter，升级或重装请保留。\r\n`,'utf8');
    },
  }});
  const sourceArchive=path.join(output,`蜗牛授权中心-对应源码-${candidate.slice(0,12)}.zip`);
  execFileSync('git',['archive','--format=zip',`--output=${sourceArchive}`,'HEAD'],{cwd:root,windowsHide:true});
  fs.copyFileSync(path.join(root,'docs/蜗牛授权中心_构建与源码交付.md'),path.join(output,'构建与源码交付说明.md'));
  const artifact=path.join(output,`小云雀客户授权中心-${managerVersion}-x64.zip`);
  fs.writeFileSync(path.join(output,'管理包验收身份.json'),JSON.stringify({formatVersion:1,candidate,artifact:path.basename(artifact),sha256:createHash('sha256').update(fs.readFileSync(artifact)).digest('hex'),sourceArchive:path.basename(sourceArchive),sourceSHA256:createHash('sha256').update(fs.readFileSync(sourceArchive)).digest('hex'),deployment:'local_offline',docker:false,installedAcceptance:false},null,2)+'\n');
}
main().catch(error=>{process.stderr.write(`管理包构建失败：${error.message}\n`);process.exitCode=1;});
