import {byId, registerRuntime, runtimeFunction} from './state.js';
const hideOtaLogPanel=runtimeFunction('hideOtaLogPanel');
function setBanner(message,isError){const el=byId('actionBanner');el.textContent=message;el.className='banner'+(isError?' error':'');}
    function updateActionDetailsVisibility(){const wrap=byId('actionDetails');const body=byId('actionDetailsText');const ota=byId('otaLogPanel');const hasText=!!(body&&body.style.display!=='none'&&body.textContent);const hasOta=!!(ota&&ota.style.display!=='none');wrap.style.display=(hasText||hasOta)?'block':'none';}
    function setDetails(text,options){const body=byId('actionDetailsText');if(!(options&&options.keepOtaLog)){hideOtaLogPanel();}if(text){body.style.display='block';body.textContent=text;}else{body.style.display='none';body.textContent='';}updateActionDetailsVisibility();}
    async function request(url,options){const res=await fetch(url,options);const text=await res.text();let body={};try{body=text?JSON.parse(text):{}}catch(err){body={ok:false,error:text||err.message}}if(!res.ok){throw new Error(body.error||('HTTP '+res.status))}return body;}

export { request, setBanner, setDetails, updateActionDetailsVisibility };
registerRuntime({ request, setBanner, setDetails, updateActionDetailsVisibility });
