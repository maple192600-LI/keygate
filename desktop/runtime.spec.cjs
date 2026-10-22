'use strict';
const {test}=require('node:test');
const assert=require('node:assert/strict');
const http=require('node:http');
const {createHmac,randomBytes}=require('node:crypto');
const {waitForHealth}=require('./runtime.cjs');

async function server(t,reply){
  const listener=http.createServer(reply);
  await new Promise(resolve=>listener.listen(0,'127.0.0.1',resolve));
  t.after(()=>new Promise((resolve,reject)=>listener.close(error=>error?reject(error):resolve())));
  return `http://127.0.0.1:${listener.address().port}`;
}
test('伪健康标记不能让启动器信任被抢占端口，启动秘密不发给监听者',async t=>{
  const token=randomBytes(32).toString('hex');
  const address=await server(t,(request,response)=>{
    assert.equal(request.headers['x-snail-runtime-token'],undefined);
    assert.equal(request.headers['x-snail-setup-token'],undefined);
    assert.match(new URL(request.url,'http://localhost').searchParams.get('challenge'),/^[a-f0-9]{64}$/);
    response.end(JSON.stringify({data:{deployment:'local_offline',proof:'0'.repeat(64)}}));
  });
  await assert.rejects(waitForHealth(address,{exitCode:null,signalCode:null},token,1000),{code:'LOCAL_RUNTIME_UNTRUSTED'});
});
test('服务持有本次秘密才能响应随机挑战，响应后已退出的子进程不能就绪',async t=>{
  const token=randomBytes(32).toString('hex');
  const address=await server(t,(request,response)=>{
    const challenge=Buffer.from(new URL(request.url,'http://localhost').searchParams.get('challenge'),'hex');
    const proof=createHmac('sha256',Buffer.from(token,'hex')).update('snail-runtime-ready/v1\0').update(challenge).digest('hex');
    response.end(JSON.stringify({data:{deployment:'local_offline',proof}}));
  });
  await waitForHealth(address,{exitCode:null,signalCode:null},token,1000);
  await assert.rejects(waitForHealth(address,{exitCode:1,signalCode:null},token,1000),/启动失败/);
  await assert.rejects(waitForHealth(address,{exitCode:null,signalCode:'SIGTERM'},token,1000),/启动失败/);
});


test('中文程序与资料使用私有随机英文映射，停止后只删本次链接，保留原数据及未知对象',()=>{
  const fs=require('node:fs'),path=require('node:path'),os=require('node:os'),{execFileSync}=require('node:child_process');
  const {nativePostgresPaths,privateMappingDirectory}=require('./runtime.cjs');
  const root=fs.mkdtempSync(path.join(os.tmpdir(),'snail-postgres-path-')),before=process.env.ProgramData;
  try {
    process.env.ProgramData=root;
    const data=path.join(root,'中文 资料'),original=path.join(root,'中文 程序一'),updated=path.join(root,'中文 程序二');
    for(const dir of [data,original,updated])fs.mkdirSync(dir);
    fs.writeFileSync(path.join(data,'keep.txt'),'original-data');fs.writeFileSync(path.join(original,'keep.txt'),'original-program');
    const first=nativePostgresPaths(data,original),second=nativePostgresPaths(data,updated);
    assert.match(first.dataRoot,/^[\x20-\x7e]+$/);assert.notEqual(path.dirname(first.dataRoot),path.dirname(second.dataRoot));
    assert.equal(fs.realpathSync.native(first.dataRoot),fs.realpathSync.native(data));
    first.release();first.release();assert.equal(fs.existsSync(path.dirname(first.dataRoot)),false);
    assert.equal(fs.readFileSync(path.join(data,'keep.txt'),'utf8'),'original-data');assert.equal(fs.readFileSync(path.join(original,'keep.txt'),'utf8'),'original-program');
    const unexpected=path.join(path.dirname(second.dataRoot),'keep.txt');fs.writeFileSync(unexpected,'unknown-object');
    assert.throws(()=>second.release(),/未知对象/);assert.equal(fs.readFileSync(unexpected,'utf8'),'unknown-object');
    fs.unlinkSync(unexpected);fs.unlinkSync(second.dataRoot);fs.symlinkSync(updated,second.dataRoot,'junction');
    assert.throws(()=>second.release(),/发生变化/);assert.equal(fs.realpathSync.native(second.dataRoot),fs.realpathSync.native(updated));
    assert.equal(fs.readFileSync(path.join(data,'keep.txt'),'utf8'),'original-data');
    const occupied=path.join(root,'preoccupied');fs.mkdirSync(occupied);fs.writeFileSync(path.join(occupied,'keep.txt'),'preexisting-object');
    const system=process.env.SystemRoot||process.env.WINDIR;
    execFileSync(path.join(system,'System32/icacls.exe'),[occupied,'/grant','*S-1-1-0:(OI)(CI)F'],{windowsHide:true,stdio:'pipe'});
    const acl=execFileSync(path.join(system,'System32/icacls.exe'),[occupied],{windowsHide:true});
    const sid=/S-1-\d+(?:-\d+)+/.exec(execFileSync(path.join(system,'System32/whoami.exe'),['/user','/fo','csv','/nh'],{encoding:'utf8',windowsHide:true}))[0];
    assert.throws(()=>privateMappingDirectory(occupied,sid),/已经存在/);
    assert.deepEqual(execFileSync(path.join(system,'System32/icacls.exe'),[occupied],{windowsHide:true}),acl);
    assert.equal(fs.readFileSync(path.join(occupied,'keep.txt'),'utf8'),'preexisting-object');
  } finally {
    if(before===undefined)delete process.env.ProgramData;else process.env.ProgramData=before;
    assert.equal(fs.lstatSync(root).isSymbolicLink(),false);assert.equal(path.dirname(fs.realpathSync.native(root)),fs.realpathSync.native(os.tmpdir()));
    fs.rmSync(root,{recursive:true});assert.equal(fs.existsSync(root),false);
  }
});
