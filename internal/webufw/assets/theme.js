'use strict';
(() => {
 const key='webufw.theme';
 const media=window.matchMedia('(prefers-color-scheme: dark)');
 let preference='system';
 try { const saved=localStorage.getItem(key); if(['light','dark','system'].includes(saved)) preference=saved; } catch (_) { /* Storage can be disabled. */ }
 function apply() {
  document.documentElement.dataset.theme=preference==='system'?(media.matches?'dark':'light'):preference;
  document.documentElement.style.colorScheme=document.documentElement.dataset.theme;
  window.dispatchEvent(new Event('webufw:theme'));
 }
 window.webufwTheme={
  getPreference:()=>preference,
  isDark:()=>document.documentElement.dataset.theme==='dark',
  setPreference(value){if(!['light','dark','system'].includes(value))return;preference=value;try{localStorage.setItem(key,value);}catch(_){}apply();}
 };
 media.addEventListener('change',()=>{if(preference==='system')apply();});
 apply();
})();
