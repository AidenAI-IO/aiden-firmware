import {appState, byId, registerRuntime, runtimeFunction} from './state.js';
const request=runtimeFunction('request');const setBanner=runtimeFunction('setBanner');const setDetails=runtimeFunction('setDetails');const t=runtimeFunction('t');
function storageModeName(mode){if(mode===1)return t('storage.mode.emmc');if(mode===2)return t('storage.mode.dual');return t('storage.mode.auto');}
    function storageGB(bytes){return (Number(bytes||0)/(1024*1024*1024)).toFixed(1)+' GB';}
    function normalizeStorageStatus(payload){const p=payload&&typeof payload==='object'?payload:{};const available=!!(p.card&&typeof p.card==='object');const card=available?p.card:{};const internal=p.internal&&typeof p.internal==='object'?p.internal:{};return {available:available,present:!!card.present,mounted:!!card.mounted,device:card.device||'',mountPoint:p.mount_point||'',totalBytes:card.total_bytes,freeBytes:card.free_bytes,reason:card.reason||'',effectiveMode:p.effective_mode,formatJob:p.format_job||{},migration:p.migration||{},internalAvailable:!!internal.available,internalTotalBytes:internal.total_bytes,internalFreeBytes:internal.free_bytes};}
    function renderStorage(payload){const summary=byId('storageSummary');const warning=byId('storageWarning');const fsSel=byId('storageFormatFs');const fmtBtn=byId('storageFormatBtn');const ejBtn=byId('storageEjectBtn');const jobEl=byId('storageJobStatus');const totalEl=byId('storageTotalValue');const availableEl=byId('storageAvailableValue');if(!summary)return;const p=normalizeStorageStatus(payload);appState.storage=payload;if(totalEl)totalEl.textContent=p.internalAvailable?storageGB(p.internalTotalBytes):t('storage.value_unavailable');if(availableEl)availableEl.textContent=p.internalAvailable?storageGB(p.internalFreeBytes):t('storage.value_unavailable');const job=p.formatJob;const formatting=job.status==='running';let text='';if(!p.available){text=t('storage.status_unavailable');}else if(p.mounted){text=t('storage.card_mounted',{mount:p.mountPoint||'/mnt/sdcard',free:storageGB(p.freeBytes),total:storageGB(p.totalBytes)});}else if(p.present){text=t('storage.card_unusable');}else{text=t('storage.no_card');}summary.textContent=t('storage.running',{mode:storageModeName(p.effectiveMode),status:text});const mig=p.migration;let warn='';if(mig.status==='failed'){warn=t('storage.migration_failed',{error:mig.error||mig.detail||t('storage.unknown_error')});}else if(p.reason){warn=t('storage.card_issue',{reason:p.reason});}warning.textContent=warn;warning.style.display=warn?'block':'none';const cardOk=!!(p.available&&p.present);fsSel.disabled=!cardOk||formatting;fmtBtn.disabled=!cardOk||formatting;ejBtn.disabled=!(p.available&&p.mounted)||formatting;let jobText='';if(formatting){jobText=t(job.auto?'storage.auto_formatting':'storage.formatting',{fs:job.fs||''});}else if(mig.status==='running'){jobText=t('storage.migrating',{files:mig.moved_files||0,size:storageGB(mig.moved_bytes)});}else if(job.status==='failed'){jobText=t('storage.last_format_failed',{error:job.error||t('storage.unknown_error')});}else if(job.status==='success'){jobText=t('storage.last_format_complete',{fs:job.fs||''});}jobEl.textContent=jobText;jobEl.style.display=jobText?'block':'none';jobEl.className='fw-health'+(job.status==='failed'?' error':'');}
    async function refreshStorage(showBanner){const btn=byId('storageRefreshBtn');if(btn&&showBanner)btn.disabled=true;try{const payload=await request('/api/storage/status',{method:'GET'});renderStorage(payload);if(showBanner){setBanner(t('storage.refreshed'),false);}}catch(err){const summary=byId('storageSummary');if(summary)summary.textContent=t('storage.load_failed',{error:err.message});}finally{if(btn&&showBanner)btn.disabled=false;}}
    async function startStorageFormat(){const fsSel=byId('storageFormatFs');const fs=fsSel.value;if(!window.confirm(t('storage.format_confirm',{fs:fs.toUpperCase()})))return;if(!window.confirm(t('storage.erase_confirm')))return;const btn=byId('storageFormatBtn');btn.disabled=true;try{await request('/api/storage/format',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({fs:fs,confirm:'format-sd-card'})});setBanner(t('storage.format_started'),false);}catch(err){setBanner(t('storage.format_start_failed'),true);setDetails(err.message);}finally{await refreshStorage(false);}}
    async function ejectStorageCard(){if(!window.confirm(t('storage.eject_confirm')))return;const btn=byId('storageEjectBtn');btn.disabled=true;try{await request('/api/storage/eject',{method:'POST',headers:{'Content-Type':'application/json'},body:'{}'});setBanner(t('storage.ejected'),false);}catch(err){setBanner(t('storage.eject_failed'),true);setDetails(err.message);}finally{await refreshStorage(false);}}

async function exportConfigBackup() {
  const btn = byId('configBackupExportBtn');
  if (btn) btn.disabled = true;
  setBanner(t('storage.backup_exporting'), false);
  setDetails('');
  try {
    const response = await fetch('/api/config/backup');
    if (!response.ok) {
      const body = await response.json().catch(() => ({}));
      throw new Error(body.error || ('HTTP ' + response.status));
    }
    const blob = await response.blob();
    const disposition = response.headers.get('Content-Disposition') || '';
    const match = disposition.match(/filename="([^"]+)"/i);
    const url = URL.createObjectURL(blob);
    const link = document.createElement('a');
    link.href = url;
    link.download = match ? match[1] : 'aiden-config.toml';
    document.body.appendChild(link);
    link.click();
    link.remove();
    URL.revokeObjectURL(url);
    setBanner(t('storage.backup_exported'), false);
  } catch (err) {
    setBanner(t('storage.backup_export_failed'), true);
    setDetails(err.message);
  } finally {
    if (btn) btn.disabled = false;
  }
}

function chooseConfigBackup() {
  const input = byId('configBackupInput');
  if (input) input.click();
}

async function restoreConfigBackup(input) {
  const file = input && input.files && input.files[0] ? input.files[0] : null;
  if (!file) return false;
  const btn = byId('configBackupImportBtn');
  try {
    if (!/\.toml$/i.test(file.name)) throw new Error(t('storage.backup_file_required'));
    if (!window.confirm(t('storage.backup_import_confirm', {name: file.name}))) return false;
    if (btn) btn.disabled = true;
    setBanner(t('storage.backup_importing'), false);
    setDetails('');
    const payload = await request('/api/config/backup', {
      method: 'PUT',
      headers: {'Content-Type': 'application/toml; charset=utf-8'},
      body: file
    });
    setBanner(t(payload.pending ? 'storage.backup_imported_pending' : 'storage.backup_imported'), false);
    return true;
  } catch (err) {
    if (err && err.persisted === true) {
      setBanner(t('storage.backup_imported_not_applied'), true);
      setDetails(err.message);
      return true;
    }
    setBanner(t('storage.backup_import_failed'), true);
    setDetails(err.message);
    return false;
  } finally {
    if (btn) btn.disabled = false;
    if (input) input.value = '';
  }
}

export {
  normalizeStorageStatus, renderStorage, refreshStorage, startStorageFormat, ejectStorageCard,
  exportConfigBackup, chooseConfigBackup, restoreConfigBackup
};
registerRuntime({
  renderStorage, refreshStorage, startStorageFormat, ejectStorageCard,
  exportConfigBackup, chooseConfigBackup, restoreConfigBackup
});
