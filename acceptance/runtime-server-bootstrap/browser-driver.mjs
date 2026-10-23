import {spawn} from 'node:child_process';
import {once} from 'node:events';
import {existsSync,readFileSync,writeFileSync} from 'node:fs';
const [id,owner,database,fingerprint,evidence,portFile]=process.argv.slice(2);
const relay=spawn('python3',['-B','acceptance/runtime-server-bootstrap/relay.py',id,owner,portFile,'onboarding-https'],{stdio:['ignore','pipe','inherit']});
let relayLog=''; relay.stdout.on('data',data=>{relayLog+=data;process.stdout.write(data)});
let fixture,browser,browserResult,ready=false;
const stop=()=>{if(browser?.exitCode===null)browser.kill('SIGTERM');if(fixture?.exitCode===null)fixture.kill('SIGTERM');if(relay.exitCode===null)relay.kill('SIGTERM')};
process.once('SIGINT',stop);process.once('SIGTERM',stop);
const timer=setTimeout(stop,620_000);
try {
 for(let i=0;i<50&&!existsSync(portFile);i++){if(relay.exitCode!==null)throw Error('HTTPS relay failed');await new Promise(resolve=>setTimeout(resolve,100))}
 if(!existsSync(portFile)||readFileSync(portFile,'utf8')!=='19443')throw Error('fixed loopback HTTPS relay unavailable');
 fixture=spawn('docker',['exec','--workdir','/internal/api','--env','NEROCD_BOOTSTRAP_ONBOARDING=owned-systemd-target','--env','NEROCD_BOOTSTRAP_BROWSER=1','--env',`NEROCD_BOOTSTRAP_PROBE_FINGERPRINT=${fingerprint}`,'--env',`NEROCD_TEST_DATABASE_URL=${database}`,id,'/usr/bin/timeout','--signal=TERM','--kill-after=2s','600s','/fixture/api.test','-test.v','-test.run','^TestServerBootstrapRuntime$','-test.timeout=10m'],{stdio:['ignore','pipe','inherit']});
 const completed=once(fixture,'exit');let buffer='',fixtureLog='';
 fixture.stdout.on('data',data=>{fixtureLog+=data;buffer+=data;let index;while((index=buffer.indexOf('\n'))!==-1){const line=buffer.slice(0,index);buffer=buffer.slice(index+1);const prefix='SERVER_BOOTSTRAP_BROWSER_READY ';if(!line.startsWith(prefix)){process.stdout.write(line+'\n');continue}if(ready){stop();continue}ready=true;const meta=JSON.parse(line.slice(prefix.length));if(meta.origin!=='https://127.0.0.1:19443'||!meta.spki){stop();continue}
 browser=spawn('bun',['run','--cwd','web/app','smoke:browser','hosting-server-bootstrap.spec.ts','--project','chromium','--no-deps','--workers','1','--output',evidence+'/browser-results'],{env:{...process.env,PLAYWRIGHT_BASE_URL:meta.origin,NEROCD_SERVER_BOOTSTRAP_BROWSER_META:JSON.stringify(meta)},stdio:'inherit'});
 browserResult=once(browser,'exit').then(async([code])=>{const finish=spawn('docker',['exec',id,'touch','/fixture/onboarding-browser.finish'],{stdio:'inherit'});const [finishCode]=await once(finish,'exit');return code===0&&finishCode===0?0:1});
 }});
 const [code]=await completed;writeFileSync(evidence+'/onboarding-runtime.log',fixtureLog);if(code!==0)stop();const browserCode=browserResult?await browserResult:1;if(!ready||code!==0||browserCode!==0)process.exitCode=1;
} finally {
 clearTimeout(timer);stop();if(relay.exitCode===null)await once(relay,'exit');writeFileSync(evidence+'/https-relay.log',relayLog);
}
