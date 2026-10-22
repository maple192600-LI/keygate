'use strict';
const fs=require('node:fs');
const path=require('node:path');
const {execFileSync}=require('node:child_process');
const directory=path.dirname(require.resolve('electron/package.json'));
const executable=path.join(directory,'dist','electron.exe');
if(!fs.existsSync(executable)) execFileSync(process.execPath,[path.join(directory,'install.js')],{stdio:'inherit',windowsHide:true});
if(!fs.statSync(executable).isFile()) throw new Error('Electron Windows 运行时未完整安装');
process.stdout.write(`独立管理运行时就绪：Electron ${require('electron/package.json').version}\n`);
