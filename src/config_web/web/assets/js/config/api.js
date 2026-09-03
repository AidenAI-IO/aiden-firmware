import {byId, registerRuntime, runtimeFunction} from './state.js';
const hideOtaLogPanel=runtimeFunction('hideOtaLogPanel');
function setBanner(message,isError){const el=byId('actionBanner');el.textContent=message;el.className='banner'+(isError?' error':'');}
    function updateActionDetailsVisibility(){const wrap=byId('actionDetails');const body=byId('actionDetailsText');const ota=byId('otaLogPanel');const hasText=!!(body&&body.style.display!=='none'&&body.textContent);const hasOta=!!(ota&&ota.style.display!=='none');wrap.style.display=(hasText||hasOta)?'block':'none';}
    function setDetails(text,options){const body=byId('actionDetailsText');if(!(options&&options.keepOtaLog)){hideOtaLogPanel();}if(text){body.style.display='block';body.textContent=text;}else{body.style.display='none';body.textContent='';}updateActionDetailsVisibility();}
    // All configuration resources, including model/STT/storage operations,
    // are served by Config Web on the current origin. Agent runtime APIs are
    // intentionally not reachable from the portal.
    function agentURL(path){const current=new URL(window.location.href);const target=new URL(path,current.href);current.pathname=target.pathname;current.search=target.search;current.hash=target.hash;return current.toString();}
    async function agentRequest(path,options){return request(path,options);}
    async function request(url,options){const res=await fetch(url,options);const text=await res.text();let body={};try{body=text?JSON.parse(text):{}}catch(err){body={ok:false,error:text||err.message}}if(!res.ok){const error=new Error(body.error||('HTTP '+res.status));error.status=res.status;if(body&&typeof body==='object')Object.keys(body).forEach(function(key){error[key]=body[key];});throw error;}return body;}

export { request, agentRequest, agentURL, setBanner, setDetails, updateActionDetailsVisibility };
registerRuntime({ request, agentRequest, agentURL, setBanner, setDetails, updateActionDetailsVisibility });
