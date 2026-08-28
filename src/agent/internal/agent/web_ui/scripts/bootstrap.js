// Initialize only after every feature script has been evaluated.
loadHistory();
setInterval(loadHistory, 2000);
refreshCurrentLiveActivity();
setInterval(refreshCurrentLiveActivity, 2000);
loadToolCatalog();
loadStorageStatus();
setInterval(loadStorageStatus, 5000);
autoResizeInput();
connectSSE();
configureTerminal();

inputEl.addEventListener('input', autoResizeInput);
imageInputEl.addEventListener('change', handleImageSelection);
inputEl.addEventListener('paste', handleComposerPaste);
stateModalCloseEl.addEventListener('click', closeStateDetails);
stateModalEl.addEventListener('click', function(event) {
    if (event.target === stateModalEl) closeStateDetails();
});
toolSelectEl.addEventListener('change', syncSelectedTool);
inputEl.addEventListener('keydown', function(event) {
    if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
        event.preventDefault();
        sendMessage();
    }
});

document.addEventListener('keydown', function(event) {
    if (event.key === 'Escape' && activeStateMessageKey) {
        closeStateDetails();
    }
});

function configureTerminal() {
    const terminalUrl = '/wetty/';
    terminalFrameEl.src = terminalUrl;
    terminalOpenLinkEl.href = terminalUrl;
}
