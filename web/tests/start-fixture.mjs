// Starts one offline fixture server per Playwright worker (ports 8788, 8789, ...).
// Initialization runs once and its state is copied, so every server shares the
// same owner credential, and all servers boot side by side. Port 8787 answers
// /ready once every server is healthy; Playwright waits on that single URL.
import {cpSync,mkdtempSync,mkdirSync,writeFileSync} from 'node:fs';
import {createServer} from 'node:http';
import {tmpdir} from 'node:os';
import {join,resolve} from 'node:path';
import {spawn,spawnSync} from 'node:child_process';
const count=Number(process.argv[2]??1);
const root=mkdtempSync(join(tmpdir(),'smsbot-e2e-'));const owner=join(root,'owner.txt');
const binary=resolve('../bin/'+(process.platform==='win32'?'smsbot.exe':'smsbot'));
const seed=join(root,'seed');
const setup=spawnSync(binary,['--data',join(seed,'state'),'--key',join(seed,'master.key'),'--init','--owner-file',owner],{stdio:'inherit'});
if(setup.status!==0)process.exit(1);
mkdirSync('.test-state',{recursive:true});
const children=[];
for(let i=0;i<count;i++){
 const port=8788+i,dir=join(root,`server-${i}`);
 cpSync(seed,dir,{recursive:true});
 writeFileSync(`.test-state/connection-${port}.json`,JSON.stringify({owner}));
 const child=spawn(binary,['--data',join(dir,'state'),'--key',join(dir,'master.key'),'--fixture','--fixture-speed',process.env.SMSBOT_FIXTURE_SPEED??'10','--listen',`127.0.0.1:${port}`],{stdio:'inherit'});
 child.on('exit',code=>{stop();process.exit(code??0);});
 children.push(child);
}
function stop(){for(const c of children)if(c.exitCode===null)c.kill();}
process.on('SIGINT',stop);process.on('SIGTERM',stop);
const healthy=port=>fetch(`http://127.0.0.1:${port}/healthz`).then(r=>r.ok,()=>false);
let ready=false;
createServer(async(_,res)=>{
 ready||=(await Promise.all(children.map((_,i)=>healthy(8788+i)))).every(Boolean);
 res.writeHead(ready?200:503).end();
}).listen(8787,'127.0.0.1');
