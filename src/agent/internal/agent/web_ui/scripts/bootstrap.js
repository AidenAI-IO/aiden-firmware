// Initialize only after every feature script has been evaluated.
loadHistory();
refreshCurrentLiveActivity();
setInterval(refreshCurrentLiveActivity, 2000);
loadToolCatalog();
loadToolSkills();
loadStorageStatus();
setInterval(loadStorageStatus, 5000);
autoResizeInput();
connectSSE();

inputEl.addEventListener('input', autoResizeInput);
imageInputEl.addEventListener('change', handleImageSelection);
inputEl.addEventListener('paste', handleComposerPaste);
toolSelectEl.addEventListener('change', syncSelectedTool);
skillSelectEl.addEventListener('change', syncSelectedSkill);
inputEl.addEventListener('keydown', function(event) {
    if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
        event.preventDefault();
        sendMessage();
    }
});
