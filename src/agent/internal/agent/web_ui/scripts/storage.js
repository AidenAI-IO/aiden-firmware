// Storage status and safe-eject controls.
async function loadStorageStatus() {
    try {
        const res = await fetch('/api/storage/status');
        if (!res.ok) throw new Error('HTTP ' + res.status);
        renderStorageStatus(await res.json());
    } catch (err) {
        const summary = document.getElementById('storageSummary');
        if (summary) summary.textContent = 'Storage status unavailable: ' + err.message;
    }
}

function storageModeName(mode) {
    if (mode === 1) return 'eMMC only';
    if (mode === 2) return 'Dual storage';
    return 'Auto';
}

function storageGB(bytes) {
    return (bytes / (1024 * 1024 * 1024)).toFixed(1) + ' GB';
}

function renderStorageStatus(status) {
    const summary = document.getElementById('storageSummary');
    const warning = document.getElementById('storageWarning');
    const ejectBtn = document.getElementById('storageEjectBtn');
    if (!summary || !warning) return;

    let text = 'Running: ' + storageModeName(status.effective_mode) + '. ';
    if (status.card.mounted) {
        text += 'SD card mounted at ' + status.mount_point + ' (' +
            storageGB(status.card.free_bytes) + ' free of ' + storageGB(status.card.total_bytes) + ').';
    } else if (status.card.present) {
        text += 'SD card present but not in use.';
    } else {
        text += 'No SD card.';
    }
    summary.textContent = text;

    let warn = '';
    if (status.format_job && status.format_job.status === 'running') {
        warn = 'Formatting card (' + status.format_job.fs + ')...';
    } else if (status.migration && status.migration.status === 'running') {
        warn = 'eMMC is filling up; migrating older recordings to SD (' +
            (status.migration.moved_files || 0) + ' files moved)...';
    } else if (status.migration && status.migration.status === 'failed') {
        warn = 'Storage migration failed: ' + (status.migration.error || status.migration.detail || 'unknown error');
    } else if (status.card.reason) {
        warn = 'Card issue: ' + status.card.reason;
    }
    warning.textContent = warn;
    warning.hidden = !warn;

    const formatting = status.format_job && status.format_job.status === 'running';
    if (ejectBtn) ejectBtn.disabled = !status.card.mounted || formatting;
}

function setStorageStatusMsg(text, isError) {
    const el = document.getElementById('storageStatusMsg');
    if (!el) return;
    el.textContent = text;
    el.classList.toggle('error', !!isError);
}

async function postStorage(path, body, pendingMsg, okMsg) {
    setStorageStatusMsg(pendingMsg, false);
    try {
        const res = await fetch(path, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(body)
        });
        const data = await res.json();
        if (!res.ok) throw new Error(data.error || ('HTTP ' + res.status));
        renderStorageStatus(data);
        setStorageStatusMsg(okMsg, false);
    } catch (err) {
        setStorageStatusMsg(err.message, true);
        loadStorageStatus();
    }
}

async function ejectStorage() {
    if (!confirm('Sync and unmount the SD card so it can be safely removed?')) return;
    await postStorage('/api/storage/eject', {}, 'Ejecting...', 'Card ejected. Safe to remove.');
}
