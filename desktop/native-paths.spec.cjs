 'use strict';
const {test}=require('node:test'),assert=require('node:assert/strict');
const fs=require('node:fs'),path=require('node:path'),os=require('node:os');
const {spawnSync}=require('node:child_process');
const {startRuntime}=require('./runtime.cjs');

test('中文原生数据库启动、异常辅助文件保留，Go与PG停止后返回告警而不阻止退出',{timeout:60000},async()=>{
 const postgres=process.env.SNAIL_MANAGER_TEST_POSTGRES;
 if(!postgres||!path.isAbsolute(postgres))throw Error('必须指定本任务已核对的PostgreSQL运行目录');
 const root=fs.mkdtempSync(path.join(os.tmpdir(),'snail-native-paths-'));
 console.log('NATIVE_PATH_EVIDENCE',root);
 const data=path.join(root,'中文 资料'),source=path.join(root,'中文 程序'),mappings=path.join(root,'mappings');
 const previous=process.env.ProgramData;
 let runtime,stopped=false;
 fs.mkdirSync(mappings);fs.symlinkSync(postgres,source,'junction');process.env.ProgramData=mappings;
 try {
  const serverRoot=path.resolve(__dirname,'..');
  runtime=await startRuntime({dataRoot:data,postgresRoot:source,serverRoot,serverExecutable:path.join(serverRoot,'bin/snail-license-center.exe'),nativePaths:true});
  const first=await fetch(runtime.address+'/health');assert.equal(first.status,200);
  const names=fs.readdirSync(mappings);assert.equal(names.length,1);
  const alias=path.join(mappings,names[0]),unexpected=path.join(alias,'unexpected.keep');
  fs.writeFileSync(unexpected,'owned-failure-sample');
  const result=await runtime.stop();stopped=true;
  assert.equal(result.cleanupWarning.code,'NATIVE_MAPPING_RETAINED');assert.equal(result.cleanupWarning.directory,alias);
  assert.equal(fs.readFileSync(unexpected,'utf8'),'owned-failure-sample');
  assert.equal(fs.existsSync(path.join(data,'postgres/postmaster.pid')),false);
  const status=spawnSync(path.join(postgres,'bin/pg_ctl.exe'),['status','-D',path.join(data,'postgres')],{windowsHide:true});assert.equal(status.status,3);
  await assert.rejects(fetch(runtime.address+'/health',{signal:AbortSignal.timeout(1000)}));
  assert.equal((await runtime.stop()).cleanupWarning.code,'NATIVE_MAPPING_RETAINED');
  fs.writeFileSync(path.join(root,'acceptance.json'),JSON.stringify({passed:true,chineseProgramAndData:true,goStopped:true,postgresStopped:true,warningReturned:true,unknownRetained:true}));
 } finally {
  if(runtime&&!stopped)await runtime.stop();
  if(previous===undefined)delete process.env.ProgramData;else process.env.ProgramData=previous;
  const status=fs.existsSync(path.join(data,'postgres/PG_VERSION'))?spawnSync(path.join(postgres,'bin/pg_ctl.exe'),['status','-D',path.join(data,'postgres')],{windowsHide:true}).status:3;
  assert.equal(status,3,'数据库未确认停止，隔离资料保留');
  assert.equal(fs.lstatSync(root).isSymbolicLink(),false);assert.equal(path.dirname(fs.realpathSync.native(root)),fs.realpathSync.native(os.tmpdir()));
  assert.equal(fs.lstatSync(source).isSymbolicLink(),true);assert.equal(fs.realpathSync.native(source),fs.realpathSync.native(postgres));fs.unlinkSync(source);
  fs.rmSync(root,{recursive:true});assert.equal(fs.existsSync(root),false);assert.ok(fs.existsSync(path.join(postgres,'bin/postgres.exe')));
 }
});
