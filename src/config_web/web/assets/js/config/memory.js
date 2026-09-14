import {request, setBanner, setDetails} from './api.js?v=configuration-groups-20260914-v16';
import {t} from './i18n.js?v=configuration-groups-20260914-v16';

async function resetConversationMemory() {
  if (!window.confirm(t('memory.reset.confirm'))) return;
  const button = document.querySelector('[data-action="reset-conversation-memory"]');
  if (button) button.disabled = true;
  setBanner(t('memory.reset.running'), false);
  setDetails('');
  try {
    await request('/api/memory/reset', {method: 'POST'});
    setBanner(t('memory.reset.done'), false);
    setDetails('');
  } catch (err) {
    setBanner(t('memory.reset.failed'), true);
    setDetails(err.message);
  } finally {
    if (button) button.disabled = false;
  }
}

export {
  resetConversationMemory,
};
