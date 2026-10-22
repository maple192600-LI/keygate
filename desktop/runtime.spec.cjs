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
