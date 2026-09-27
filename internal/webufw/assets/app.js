'use strict';
const $ = (id) => document.getElementById(id);
const esc = (value) => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const labels = {overview:'總覽',host:'主機規則',docker:'Docker 規則',logs:'日誌',settings:'設定'};
let pendingStamp = '', changeHint = ''; 
let csrf = '', state = null, page = 'overview', editing = null, preview = null, busy = false, loggedIn = false;
let sourceOptions = [], sourcePreview = null;
const badge = (s,kind='') => `<span class="badge ${kind}">${esc(s)}</span>`;
const sourceLabel = s => ['any','0.0.0.0/0','::/0'].includes(s) ? '所有來源' : s;
function toast(message, error=false) { $('toast').textContent=message; $('toast').className=error?'error':''; $('toast').hidden=false; }
async function api(path, data) {
 const opts={headers:{}};
 if(data!==undefined) { opts.method='POST'; opts.headers['Content-Type']='application/json'; opts.headers['X-CSRF-Token']=csrf; opts.body=JSON.stringify(data); }
 const r=await fetch('/api/v1/'+path,opts); const body=await r.json();
 if(!r.ok) { if(r.status===401 && path!=='login') showLogin(); throw new Error(body.error || '請求失敗'); }
 return body;
}
function showLogin() { loggedIn=false; csrf=''; $('app').hidden=true; $('setup').hidden=true; $('login').hidden=false; for(const id of ['preview','editor']) if($(id).open) $(id).close(); }
async function showApp(token) { csrf=token; loggedIn=true; $('toast').hidden=true; $('setup').hidden=true; $('login').hidden=true; $('app').hidden=false; await refresh(true); }
async function refresh(force=false) {
 if(!loggedIn||busy)return;
 busy=true;
 try {
  const hint=await api('changes');
  if(force||!state||hint.refresh||hint.version!==changeHint){
   const next=await api('status');
   const changed=!state||state.revision!==next.revision||JSON.stringify(state.pending)!==JSON.stringify(next.pending);
   state=next;if(changed)render();
   // Retain the hint from BEFORE the snapshot so a concurrent CLI change
   // cannot be hidden by acknowledging a newer hint than the rendered data.
   changeHint=hint.version;
  }
  $('updated').textContent='更新於 '+new Date().toLocaleTimeString('zh-TW',{hour12:false});
 }catch(e){toast(e.message,true);}finally{busy=false;}
}
function setPage(next) { page=next; for(const name of Object.keys(labels)) $('page-'+name).hidden=name!==next; document.querySelectorAll('[data-page]').forEach(b=>b.classList.toggle('selected',b.dataset.page===next)); $('page-title').textContent=labels[next]; $('breadcrumb').textContent='本機 / '+labels[next];if(next==='logs')loadLogs();if(next==='settings')loadSettings(); }
function empty(cols,text) { return `<tr><td colspan="${cols}" class="empty">${esc(text)}</td></tr>`; }
function statusLabel(value) {return badge(value?'已就緒':'未就緒',value?'ok':'warn');}
function render() {
 const env=state.environment, host=state.rules.filter(r=>r.kind!=='docker'), docker=state.rules.filter(r=>r.kind==='docker'), stale=docker.filter(r=>r.pending_sync);
 $('environment').innerHTML=`<div><dt>UFW 防火牆</dt><dd>${badge(!env.ufw?'未安裝':state.enabled?'已啟用':'已停用',env.ufw&&state.enabled?'ok':'warn')}<span class="muted">${state.ipv6?'IPv4 / IPv6':'IPv4'}</span></dd></div><div><dt>Docker Engine</dt><dd>${statusLabel(env.docker)}<span class="muted">${state.containers.length} 個容器</span></dd></div><div><dt>ufw-docker 腳本</dt><dd>${badge(env.script_installed?'已偵測':'未安裝',env.script_installed?'ok':'')}<span class="muted">${env.docker_writable?'可管理規則':env.script_installed?'目前僅供檢視':'選用功能'}</span></dd></div>`;
 const issues=[]; if(stale.length)issues.push(`<div class="issue">${badge('待同步','warn')}<span>${stale.length} 條 Docker 規則的目的 IP 與容器不同。請到 Docker 規則頁檢查並同步。</span></div>`);
 for(const w of env.warnings||[])issues.push(`<div class="issue">${badge('提示')}<span>${esc(w)}</span></div>`);
 $('issues').innerHTML=issues.join('')||'<div class="issue">目前沒有待處理事項。</div>';
 $('settings-environment').innerHTML=$('issues').innerHTML;
 $('dependency-check').innerHTML=`<div><strong>UFW</strong>${badge(env.ufw?'已安裝':'未安裝',env.ufw?'ok':'warn')}<p class="help">${env.ufw?'可使用主機規則。':'Ubuntu 可自行執行：sudo apt install ufw'}</p></div><div><strong>Docker Engine</strong>${badge(env.docker?'可連線':'未偵測',env.docker?'ok':'')}<p class="help">${env.docker?'可檢視容器。':'不使用 Docker 可直接略過；需要時再安裝 Docker Engine。'}</p></div><div><strong>ufw-docker</strong>${badge(env.script_installed?'已安裝':'未安裝',env.script_installed?'ok':'')}<p class="help">${env.script_installed?'目前來源：'+esc(env.script_source||'未知')+'；路徑：'+esc(env.script_path||'未知'):'需要 Docker 規則時再於下方選擇來源。'}</p></div>`;
 $('summary').innerHTML=`<div class="table-wrap"><table class="summary-table"><tbody><tr><td>主機規則</td><td><strong>${host.length}</strong> 條</td><td>管理主機入站與出站流量</td></tr><tr><td>Docker 轉送規則</td><td><strong>${docker.length}</strong> 條</td><td>依容器、網路與來源管理</td></tr><tr><td>唯讀規則</td><td><strong>${state.rules.filter(r=>r.read_only).length}</strong> 條</td><td>保留原始設定，使用 CLI 維護</td></tr></tbody></table></div>`;
 $('toggle-ufw').textContent=state.enabled?'停用 UFW':'啟用 UFW';$('toggle-ufw').className=state.enabled?'danger':'';$('toggle-ufw').disabled=!!state.pending||!env.ufw;
 $('add-host').disabled=!!state.pending||!env.ufw; $('add-docker').disabled=!!state.pending||!env.docker_writable||!state.enabled;
 $('docker-write-note').textContent=env.docker_writable?'新增來源放行不會移除原有的廣泛放行。需要縮小範圍時，請使用「縮限來源」。':'目前 Docker 規則僅供檢視；請到「設定 → 環境檢查」查看未就緒項目。';
 $('host-rules').innerHTML=host.map(r=>`<tr><td>${badge({allow:'允許',deny:'拒絕',reject:'拒絕並回覆'}[r.action]||r.action,r.action==='allow'?'':'deny')}<small>${r.kind==='forward'?'轉送':r.direction==='out'?'出站':'入站'} · IPv${r.family}</small></td><td class="mono">${esc(sourceLabel(r.source))}</td><td class="mono">${esc(['0.0.0.0/0','::/0'].includes(r.destination)?'所有目的':r.destination)}</td><td class="mono">${esc(r.port)} / ${esc(r.protocol.toUpperCase())}</td><td>${esc(r.comment||'—')}${r.read_only?`<small>${esc(r.reason)}</small>`:''}</td><td>${r.read_only?badge('唯讀'):`<div class="actions"><button class="text-button" data-action="host.edit" data-id="${r.id}" ${state.pending?'disabled':''}>編輯</button><button class="text-button danger" data-action="host.delete" data-id="${r.id}" ${state.pending?'disabled':''}>刪除</button></div>`}</td></tr>`).join('')||empty(6,'尚未建立主機規則');
 $('docker-rules').innerHTML=docker.map(r=>{
 const c=state.containers.find(c=>c.name===r.container),p=c?.ports.find(p=>p.port===r.port&&p.protocol===r.protocol);const disabled=state.pending||!env.docker_writable||!state.enabled;
 return `<tr><td><strong>${esc(r.container||'未辨識')}</strong><small>${esc(r.network||'未指定網路')}</small></td><td class="mono">${(p?.bindings||['—']).map(esc).join('<br>')}</td><td class="mono">${esc(r.destination)}<small>${esc(r.port)}/${esc(r.protocol)} · IPv${r.family}</small></td><td class="mono">${esc(sourceLabel(r.source))}</td><td>${r.read_only?badge('唯讀'):r.pending_sync?badge('待同步','warn'):badge('已同步','ok')}</td><td>${r.read_only?`<small>${esc(r.reason)}</small>`:`<div class="actions">${r.pending_sync?`<button class="text-button" data-action="docker.sync" data-id="${r.id}" ${disabled?'disabled':''}>同步</button>`:''}<button class="text-button" data-action="docker.narrow" data-id="${r.id}" ${disabled?'disabled':''}>縮限來源</button><button class="text-button danger" data-action="docker.delete" data-id="${r.id}" ${disabled?'disabled':''}>移除</button></div>`}</td></tr>`;
 }).join('')||empty(6,'尚未建立 Docker 放行規則');
 $('container-count').textContent=`${state.containers.length} 個容器`;
 $('containers').innerHTML=state.containers.map(c=>`<tr><td><strong>${esc(c.name)}</strong></td><td class="mono">${esc(c.image)}</td><td>${c.networks.map(n=>`${esc(n.name)}<small>${esc(n.ipv4||n.ipv6||'無 IP')} · ${esc(n.driver)}</small>`).join('')}</td><td class="mono">${c.ports.flatMap(p=>p.bindings).map(esc).join('<br>')||'—'}</td><td>${badge(c.running?'執行中':'已停止',c.running?'ok':'')}${c.reason?`<small>${esc(c.reason)}</small>`:''}</td></tr>`).join('')||empty(5,'沒有可顯示的容器');
 renderPending();
}
function renderPending() { const p=state?.pending; $('pending').hidden=!p; if(!p){pendingStamp='';return;} const remaining=Math.max(0,Math.ceil((new Date(p.deadline)-Date.now())/1000));const conflict=p.state==='conflict';const stamp=JSON.stringify(p);if(stamp===pendingStamp){if($('pending-seconds'))$('pending-seconds').textContent=remaining;if($('confirm-pending'))$('confirm-pending').disabled=remaining<=0;return;}pendingStamp=stamp;$('pending').innerHTML=`<strong>${conflict?'變更需要人工核對':`請確認目前連線正常 · <span id="pending-seconds">${remaining}</span> 秒`}</strong><p>${esc(conflict?p.message:'確認後才會保留變更；逾時將由主機 Agent 回復本次操作。')}</p>${conflict?'<p>請以 CLI 檢查目前規則；修復後停止服務，執行 webufw resolve --acknowledge-current-state，再啟動服務。</p>':`<div class="actions"><button class="primary" id="confirm-pending" ${remaining<=0?'disabled':''}>確認保留變更</button><button id="rollback-pending">立即回復</button></div>`}`;if(!conflict){$('confirm-pending').onclick=()=>pendingAction('confirm');$('rollback-pending').onclick=()=>pendingAction('rollback');} }
async function pendingAction(action){try{state=await api(action,{id:state.pending.id});render();toast(action==='confirm'?'變更已確認。':'本次變更已回復。');}catch(e){toast(e.message,true);refresh();}}
function input(name,label,value='',extra=''){return `<label>${esc(label)}<input name="${name}" value="${esc(value)}" ${extra}></label>`;}
function select(name,label,options,value){return `<label>${esc(label)}<select name="${name}">${options.map(([v,l])=>`<option value="${esc(v)}" ${v===value?'selected':''}>${esc(l)}</option>`).join('')}</select></label>`;}
function openHost(rule){editing={kind:rule?'host.edit':'host.add',rule_id:rule?.id};$('edit-title').textContent=rule?'編輯主機規則':'新增主機規則';$('edit-fields').innerHTML=`<div class="form-grid">${select('action','動作',[['allow','允許'],['deny','拒絕'],['reject','拒絕並回覆']],rule?.action||'allow')}${select('direction','方向',[['in','入站'],['out','出站']],rule?.direction||'in')}${select('protocol','協定',[['tcp','TCP'],['udp','UDP'],['any','所有協定']],rule?.protocol||'tcp')}${input('port','連接埠 / 範圍',rule?.port||'','placeholder="443 或 8000:8100；留空代表全部"')}${input('source','來源 IP / CIDR',rule?.source||'any','placeholder="any 或 192.168.1.0/24"')}${input('destination','目的 IP / CIDR',rule?.destination||'any','placeholder="any 或 192.0.2.10"')}<div class="wide">${input('comment','註解',rule?.comment||'','maxlength="128"')}</div></div><p class="help">來源或目的指定 IP 時，只建立對應版本的規則。兩者為 any 時會依 UFW 設定建立 IPv4 / IPv6 規則。</p>`;$('editor').showModal();}
function openDocker(rule){editing={kind:rule?'docker.narrow':'docker.add',rule_id:rule?.id};$('edit-title').textContent=rule?'縮限既有來源放行':'新增 Docker 來源放行';const candidates=state.containers.filter(c=>c.running&&!c.reason&&c.ports.length);if(!candidates.length){toast('沒有可管理的執行中容器。',true);return;}
 $('edit-fields').innerHTML=`${rule?'<p class="notice">此操作會移除同容器、網路與服務埠的「所有來源」放行，保留其他指定來源規則。預覽會列出範圍。</p>':''}<div class="form-grid">${select('container','容器',candidates.map(c=>[c.name,c.name]),rule?.container||candidates[0].name)}${select('network','網路',[],'')}${select('service','容器服務埠',[],'')}${input('destination','目的 IP（自動取得）','','readonly')}<div class="wide">${input('source','允許來源 IP / CIDR',rule?'': 'any','placeholder="203.0.113.10 或 198.51.100.0/24" required')}<p id="port-mapping" class="help"></p></div></div>`;
 const form=$('edit-form');form.elements.container.onchange=()=>updateDockerFields();form.elements.network.onchange=updateDestination;form.elements.service.onchange=updateDestination;form.elements.source.oninput=updateDestination;updateDockerFields(rule);if(rule){form.elements.container.disabled=true;form.elements.network.disabled=true;form.elements.service.disabled=true;}$('editor').showModal();}
function updateDockerFields(rule){const f=$('edit-form'),c=state.containers.find(c=>c.name===f.elements.container.value);if(!c)return;f.elements.network.innerHTML=c.networks.filter(n=>n.supported).map(n=>`<option value="${esc(n.name)}" ${n.name===rule?.network?'selected':''}>${esc(n.name)}</option>`).join('');f.elements.service.innerHTML=c.ports.map(p=>`<option value="${esc(p.port+'/'+p.protocol)}" ${p.port===rule?.port&&p.protocol===rule?.protocol?'selected':''}>${esc(p.port+'/'+p.protocol)}</option>`).join('');updateDestination();}
function updateDestination(){const f=$('edit-form'),c=state.containers.find(c=>c.name===f.elements.container.value),n=c?.networks.find(n=>n.name===f.elements.network.value),p=c?.ports.find(p=>p.port+'/'+p.protocol===f.elements.service.value),src=f.elements.source.value;let ips=[n?.ipv4,n?.ipv6].filter(Boolean);if(src&&src!=='any')ips=ips.filter(ip=>ip.includes(':')===src.includes(':'));f.elements.destination.value=ips.join(' / ')||'無相符目的 IP';$('port-mapping').textContent='宿主機映射：'+(p?.bindings.join('；')||'未發布')+(c?.networks.length>1?'。多網路容器：請核對發布埠實際轉送的網路，規則只適用於所選目的 IP。':'');}
async function showPreview(change){try{preview=await api('preview',{...change,revision:state.revision});if($('editor').open)$('editor').close();const describe=r=>`${r.kind==='docker'?r.container+' / '+r.network:r.direction==='in'?'主機入站':'主機出站'} · ${r.source} → ${r.destination} · ${r.port}/${r.protocol} ${r.comment?'· '+r.comment:''}`;
 let html='';for(const r of preview.removed)html+=`<div class="change-row">${badge('移除','deny')}${esc(describe(r))}</div>`;for(const r of preview.added)html+=`<div class="change-row">${badge('新增','ok')}${esc(describe(r))}</div>`;if(change.kind==='ufw.toggle')html+=`<div class="change-row">${change.enabled?'啟用':'停用'} UFW 防火牆</div>`;
 if(preview.dangerous)html+='<p class="notice">套用後需在 60 秒內確認連線正常，否則 Agent 會嘗試回復本次變更。</p>';if(preview.warnings.length)html+=`<ul class="warning-list">${preview.warnings.map(w=>`<li>${esc(w)}</li>`).join('')}</ul>`;
 html+=`<details><summary>查看執行參數</summary><pre>${preview.commands.map(c=>esc((c.tool==='docker'?'ufw-docker':'ufw')+' '+c.args.map(a=>JSON.stringify(a)).join(' '))).join('\n')}</pre></details>`;$('preview-content').innerHTML=html;$('preview').showModal();}catch(e){toast(e.message,true);refresh();}}
async function loadLogs(){try{const data=await api('logs');$('firewall-logs').textContent=(data.firewall||[]).join('\n')||'目前沒有 UFW 日誌。';$('audit-logs').innerHTML=[...(data.audit||[])].reverse().map(a=>`<tr><td>${esc(new Date(a.at).toLocaleString('zh-TW',{hour12:false}))}</td><td class="mono">${esc(a.action)}</td><td>${esc(a.detail)}</td></tr>`).join('')||empty(3,'尚無操作紀錄');}catch(e){toast(e.message,true);}}
async function loadSettings(){try{const [data]=await Promise.all([api('settings'),loadSources()]);$('settings-form').elements.listen.value=data.listen;}catch(e){toast(e.message,true);}}
async function loadSources(){const data=await api('sources');sourceOptions=data.options||[];$('source-select').innerHTML=sourceOptions.map(o=>`<option value="${esc(o.id)}">${esc(o.name)}</option>`).join('');$('source-select').value=sourceOptions.some(o=>o.id===data.source?.id)?data.source.id:'hsbearbig';$('source-current').textContent=data.installed?`已偵測：${data.source?.id||'未知來源'} · ${data.source?.commit||'版本未知'} · SHA256 ${data.source?.sha256||'未知'} · ${data.compatible?'可由 WebUFW 管理 Docker 規則':'WebUFW 僅供檢視'}。CLI 路徑：${data.path}`:'目前未偵測到 ufw-docker。';sourcePreview=null;$('source-preview').hidden=true;updateSourceSupport();}
function updateSourceSupport(){const o=sourceOptions.find(o=>o.id===$('source-select').value);$('source-support').textContent=o?`${o.support}。來源：${o.url}`:'';sourcePreview=null;$('source-preview').hidden=true;}
$('source-select').onchange=updateSourceSupport;
$('source-prepare').onclick=async()=>{const b=$('source-prepare');b.disabled=true;try{sourcePreview=await api('source.prepare',{id:$('source-select').value});$('source-preview').innerHTML=`<p><strong>${esc(sourceOptions.find(o=>o.id===sourcePreview.id)?.name||sourcePreview.id)}</strong></p><p class="mono">Commit：${esc(sourcePreview.commit)}<br>SHA256：${esc(sourcePreview.sha256)}<br>大小：${sourcePreview.size} bytes</p><p class="help">${sourcePreview.compatible?'此版本支援 WebUFW Docker 規則寫入。':'此版本只供 CLI 使用，WebUFW Docker 規則維持唯讀。'}</p><button type="button" id="source-install" class="primary">確認安裝此版本</button>`;$('source-preview').hidden=false;$('source-install').onclick=installSelectedSource;}catch(e){toast(e.message,true);}finally{b.disabled=false;}};
async function installSelectedSource(){if(!sourcePreview)return;const b=$('source-install');b.disabled=true;try{const result=await api('source.install',{id:sourcePreview.id,sha256:sourcePreview.sha256});toast('已安裝 ufw-docker 至 '+result.path);await loadSources();await refresh(true);}catch(e){toast(e.message,true);b.disabled=false;}}
$('setup-form').onsubmit=async e=>{e.preventDefault();const f=e.target,b=f.querySelector('button');b.disabled=true;$('setup-error').textContent='';try{const password=f.elements.password.value;await api('setup',{code:f.elements.code.value,password});f.reset();const result=await api('login',{username:'admin',password});await showApp(result.csrf);setPage('settings');}catch(err){$('setup-error').textContent=err.message;}finally{b.disabled=false;}};
$('login-form').onsubmit=async e=>{e.preventDefault();const f=e.target,b=f.querySelector('button');b.disabled=true;$('login-error').textContent='';try{const res=await api('login',Object.fromEntries(new FormData(f)));f.elements.password.value='';await showApp(res.csrf);}catch(e){$('login-error').textContent=e.message;}finally{b.disabled=false;}};
$('edit-form').onsubmit=async e=>{e.preventDefault();const f=e.target;const change={...editing};if(editing.kind.startsWith('host.'))change.host=Object.fromEntries(new FormData(f));else{const [port,protocol]=f.elements.service.value.split('/');change.docker={container:f.elements.container.value,network:f.elements.network.value,port,protocol,source:f.elements.source.value};}await showPreview(change);};
$('apply').onclick=async()=>{if(!preview)return;const button=$('apply');button.disabled=true;try{state=await api('apply',{id:preview.id});$('preview').close();render();toast(state.pending?'已套用，請確認連線正常後保留變更。':'規則已套用。');}catch(e){$('preview').close();toast(e.message,true);refresh();}finally{button.disabled=false;preview=null;}};
$('settings-form').onsubmit=async e=>{e.preventDefault();try{const res=await api('settings.update',Object.fromEntries(new FormData(e.target)));toast(res.message);}catch(e){toast(e.message,true);}};
$('password-form').onsubmit=async e=>{e.preventDefault();try{await api('password',Object.fromEntries(new FormData(e.target)));e.target.reset();showLogin();$('login-error').textContent='密碼已更新，請重新登入。';}catch(e){toast(e.message,true);}};
$('logout').onclick=async()=>{try{await api('logout',{});showLogin();}catch(e){toast(e.message,true);}};
$('refresh').onclick=()=>refresh(true);$('load-logs').onclick=loadLogs;$('add-host').onclick=()=>openHost();$('add-docker').onclick=()=>openDocker();$('toggle-ufw').onclick=()=>showPreview({kind:'ufw.toggle',enabled:!state.enabled});
for(const b of document.querySelectorAll('[data-page]'))b.onclick=()=>setPage(b.dataset.page);
for(const b of document.querySelectorAll('[data-close]'))b.onclick=()=>$(b.dataset.close).close();
document.addEventListener('click',e=>{const b=e.target.closest('[data-action]');if(!b||b.disabled)return;const r=state?.rules.find(r=>r.id===b.dataset.id);if(!r)return;const action=b.dataset.action;if(action==='host.edit')openHost(r);else if(action==='docker.narrow')openDocker(r);else showPreview({kind:action,rule_id:r.id});});
setInterval(()=>{if(loggedIn&&!document.hidden)refresh();},3000);setInterval(()=>{if(loggedIn)renderPending();},1000);
api('setup').then(s=>{if(s.required){$('login').hidden=true;$('setup').hidden=false;return;}return api('session').then(session=>showApp(session.csrf)).catch(()=>showLogin());}).catch(()=>showLogin());

function updateThemeControls(){
 $('theme-toggle').textContent=window.webufwTheme.isDark()?'切換淺色模式':'切換深色模式';
 $('theme-select').value=window.webufwTheme.getPreference();
}
$('theme-toggle').onclick=()=>window.webufwTheme.setPreference(window.webufwTheme.isDark()?'light':'dark');
$('theme-select').onchange=e=>window.webufwTheme.setPreference(e.target.value);
window.addEventListener('webufw:theme',updateThemeControls);
updateThemeControls();
