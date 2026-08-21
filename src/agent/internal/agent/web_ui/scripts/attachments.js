// Image/audio attachments, recording, and WAV encoding.
async function handleImageSelection(event) {
    const files = Array.from(event.target.files || []);
    imageInputEl.value = '';
    if (files.length === 0) return;
    await addImageFiles(files, 'selected');
}

async function handleComposerPaste(event) {
    const files = clipboardImageFiles(event.clipboardData);
    if (files.length === 0) return;
    event.preventDefault();
    if (!canAttachImagesNow()) {
        setComposerHint('Images can be attached after the current run finishes.', true);
        return;
    }
    await addImageFiles(files, 'pasted');
}

function clipboardImageFiles(clipboardData) {
    if (!clipboardData) return [];
    const files = [];
    const items = Array.from(clipboardData.items || []);
    items.forEach(function(item) {
        if (item.kind === 'file' && item.type && item.type.indexOf('image/') === 0) {
            const file = item.getAsFile();
            if (file) files.push(file);
        }
    });
    if (files.length > 0) return files;
    return Array.from(clipboardData.files || []).filter(isImageFile);
}

async function addImageFiles(files, source) {
    const imageFiles = Array.from(files || []).filter(isImageFile);
    if (imageFiles.length === 0) return;
    if (!canAttachImagesNow()) {
        setComposerHint('Images can be attached after the current run finishes.', true);
        return;
    }

    let accepted = 0;
    let skipped = 0;
    let failed = 0;
    for (const file of imageFiles) {
        if (getDraftImageCount() >= maxDraftImageAttachments) {
            skipped++;
            continue;
        }
        try {
            const id = nextAttachmentId++;
            const dataUrl = await readFileAsDataURL(file);
            if (!canAttachImagesNow()) {
                skipped++;
                continue;
            }
            if (getDraftImageCount() >= maxDraftImageAttachments) {
                skipped++;
                continue;
            }
            draftAttachments.push({
                id: id,
                kind: 'image',
                name: imageAttachmentName(file, source, id),
                mime_type: imageMIMEType(file, dataUrl),
                data: extractBase64(dataUrl),
                size: file.size || 0
            });
            accepted++;
        } catch (err) {
            failed++;
            console.error('Failed to read image attachment:', err);
        }
    }

    renderDraftAttachments();
    reportImageAddResult(accepted, skipped, failed);
}

function isImageFile(file) {
    if (!file) return false;
    const mimeType = String(file.type || '').toLowerCase();
    if (mimeType.indexOf('image/') === 0) return true;
    return /\.(png|jpe?g|gif|webp|bmp|tiff?)$/i.test(file.name || '');
}

function getDraftImageCount() {
    return draftAttachments.filter(function(attachment) {
        return attachment.kind === 'image';
    }).length;
}

function canAttachImagesNow() {
    return !loadingDiv.classList.contains('active') && !recorderState.isRecording && !recorderState.isStopping;
}

function imageAttachmentName(file, source, id) {
    if (file && file.name && file.name.trim()) return file.name.trim();
    const prefix = source === 'pasted' ? 'pasted-image' : 'image';
    return prefix + '-' + id + '.' + imageExtensionFromType(file ? file.type : '');
}

function imageExtensionFromType(mimeType) {
    switch (String(mimeType || '').toLowerCase()) {
    case 'image/jpeg':
    case 'image/jpg':
        return 'jpg';
    case 'image/gif':
        return 'gif';
    case 'image/webp':
        return 'webp';
    case 'image/png':
    default:
        return 'png';
    }
}

function imageMIMEType(file, dataUrl) {
    const fileType = String(file && file.type || '').toLowerCase();
    if (fileType.indexOf('image/') === 0) return fileType;
    const name = String(file && file.name || '').toLowerCase();
    if (/\.(jpe?g)$/.test(name)) return 'image/jpeg';
    if (/\.gif$/.test(name)) return 'image/gif';
    if (/\.webp$/.test(name)) return 'image/webp';
    if (/\.bmp$/.test(name)) return 'image/bmp';
    if (/\.tiff?$/.test(name)) return 'image/tiff';
    return imageMIMETypeFromDataURL(dataUrl);
}

function imageMIMETypeFromDataURL(dataUrl) {
    const match = String(dataUrl || '').match(/^data:([^;,]+)[;,]/);
    const mimeType = match && match[1] ? match[1].toLowerCase() : '';
    return mimeType.indexOf('image/') === 0 ? mimeType : 'image/png';
}

function reportImageAddResult(accepted, skipped, failed) {
    if (skipped > 0) {
        setComposerHint('Only 4 images can be attached.', true);
        return;
    }
    if (failed > 0) {
        setComposerHint('Some images could not be read.', true);
        return;
    }
    if (accepted > 0) {
        resetComposerHint();
    }
}

function setComposerHint(text, isError) {
    if (!composerHintEl) return;
    if (composerHintTimer) {
        clearTimeout(composerHintTimer);
        composerHintTimer = null;
    }
    composerHintEl.textContent = text || defaultComposerHint;
    composerHintEl.classList.toggle('error', !!isError);
    if (text) {
        composerHintTimer = setTimeout(resetComposerHint, 2800);
    }
}

function resetComposerHint() {
    if (!composerHintEl) return;
    if (composerHintTimer) {
        clearTimeout(composerHintTimer);
        composerHintTimer = null;
    }
    composerHintEl.textContent = defaultComposerHint;
    composerHintEl.classList.remove('error');
}

async function toggleRecording() {
    try {
        if (recorderState.isRecording) {
            await stopRecording();
        } else {
            await startRecording();
        }
    } catch (err) {
        console.error('Audio recording error:', err);
        alert('Audio recording failed: ' + err.message);
        await teardownRecorder();
    }
}

async function startRecording() {
    if (recorderState.isRecording) return;

    const startedOnServer = await startServerRecording();
    if (startedOnServer) {
        recorderState = {
            isRecording: true,
            isStopping: false,
            mode: 'server',
            stream: null,
            context: null,
            source: null,
            processor: null,
            sink: null,
            chunks: [],
            sampleRate: targetAudioSampleRate
        };
        return;
    }

    const AudioContextClass = window.AudioContext || window.webkitAudioContext;
    if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia || !AudioContextClass) {
        throw new Error('Device audio recording is unavailable and this browser cannot record audio from the page.');
    }

    const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
    const context = new AudioContextClass();
    const source = context.createMediaStreamSource(stream);
    const processor = context.createScriptProcessor(4096, 1, 1);
    const sink = context.createGain();
    sink.gain.value = 0;

    const chunks = [];
    processor.onaudioprocess = function(event) {
        if (!recorderState.isRecording) return;
        const channelData = event.inputBuffer.getChannelData(0);
        chunks.push(new Float32Array(channelData));
    };

    source.connect(processor);
    processor.connect(sink);
    sink.connect(context.destination);

    recorderState = {
        isRecording: true,
        isStopping: false,
        mode: 'browser',
        stream: stream,
        context: context,
        source: source,
        processor: processor,
        sink: sink,
        chunks: chunks,
        sampleRate: context.sampleRate
    };

}

async function stopRecording() {
    if (!recorderState.isRecording || recorderState.isStopping) return;

    const mode = recorderState.mode;
    recorderState.isStopping = true;
    setComposerState(loadingDiv.classList.contains('active'));

    try {
        if (mode === 'server') {
            const attachment = await stopServerRecording();
            await teardownRecorder({ forceServerStop: false });
            if (attachment) {
                upsertAudioAttachment({
                    id: nextAttachmentId++,
                    kind: attachment.kind || 'audio',
                    name: attachment.name || 'recording.wav',
                    mime_type: attachment.mime_type || 'audio/wav',
                    data: attachment.data || '',
                    size: attachment.size || 0,
                    transcript: attachment.transcript || ''
                });
            }
            return;
        }

        const chunks = recorderState.chunks.slice();
        const sampleRate = recorderState.sampleRate;
        await teardownRecorder({ forceServerStop: false });

        const wavBlob = createWavBlob(chunks, sampleRate, targetAudioSampleRate);
        const dataUrl = await readBlobAsDataURL(wavBlob);

        upsertAudioAttachment({
            id: nextAttachmentId++,
            kind: 'audio',
            name: 'recording.wav',
            mime_type: 'audio/wav',
            data: extractBase64(dataUrl),
            size: wavBlob.size,
            preview_url: URL.createObjectURL(wavBlob)
        });
    } finally {
        recorderState.isRecording = false;
        recorderState.isStopping = false;
        setComposerState(loadingDiv.classList.contains('active'));
    }
}

async function forceStopServerRecording() {
    try {
        await fetch('/api/audio/record/stop', { method: 'POST' });
    } catch (err) {
        console.warn('Force stop device recording failed:', err);
    }
}

async function startServerRecording() {
    const res = await fetch('/api/audio/record/start', { method: 'POST' });
    if (res.status === 404) {
        return false;
    }
    if (res.status === 409) {
        await forceStopServerRecording();
        const retry = await fetch('/api/audio/record/start', { method: 'POST' });
        if (retry.status === 404) {
            return false;
        }
        if (!retry.ok) {
            throw new Error(await retry.text() || 'Device audio recording failed to start.');
        }
        return true;
    }
    if (!res.ok) {
        const detail = await res.text() || 'Device audio recording failed to start.';
        if (detail.indexOf('audio service') !== -1 || detail.indexOf('audio_service') !== -1 || detail.indexOf('no such file') !== -1) {
            throw new Error('Audio service is not running. Run: /etc/init.d/S53audio_service start');
        }
        throw new Error(detail);
    }
    return true;
}

async function stopServerRecording() {
    const res = await fetch('/api/audio/record/stop', { method: 'POST' });
    if (res.status === 400) {
        return null;
    }
    if (!res.ok) {
        throw new Error(await res.text() || 'Device audio recording failed to stop.');
    }
    const data = await res.json();
    if (!data.attachment || !data.attachment.data) {
        throw new Error('Device audio recording returned no audio data.');
    }
    return data.attachment;
}

function upsertAudioAttachment(attachment) {
    const nextDrafts = [];
    draftAttachments.forEach(function(item) {
        if (item.kind === 'audio') {
            revokeAttachmentPreview(item);
            return;
        }
        nextDrafts.push(item);
    });
    nextDrafts.push(attachment);
    draftAttachments = nextDrafts;
    renderDraftAttachments();
}

async function teardownRecorder(options) {
    const opts = options || {};
    const wasServerMode = recorderState.mode === 'server';
    if (recorderState.processor) {
        recorderState.processor.disconnect();
    }
    if (recorderState.source) {
        recorderState.source.disconnect();
    }
    if (recorderState.sink) {
        recorderState.sink.disconnect();
    }
    if (recorderState.stream) {
        recorderState.stream.getTracks().forEach(function(track) { track.stop(); });
    }
    if (recorderState.context && recorderState.context.state !== 'closed') {
        await recorderState.context.close();
    }
    recorderState = createRecorderState();
    if (wasServerMode && opts.forceServerStop !== false) {
        await forceStopServerRecording();
    }
}

function renderDraftAttachments() {
    draftAttachmentsEl.innerHTML = '';

    draftAttachments.forEach(function(attachment) {
        const row = document.createElement('div');
        row.className = 'draft-attachment';

        if (attachment.kind === 'image') {
            const image = document.createElement('img');
            image.alt = attachment.name || 'Image attachment';
            image.src = attachmentDataURL(attachment);
            row.appendChild(image);
        } else if (attachment.kind === 'audio') {
            const audio = document.createElement('audio');
            audio.controls = true;
            audio.src = attachmentDataURL(attachment);
            row.appendChild(audio);
        }

        const copy = document.createElement('div');
        copy.className = 'draft-attachment-copy';

        const name = document.createElement('div');
        name.className = 'draft-attachment-name';
        name.textContent = attachment.name || getAttachmentTitle(attachment.kind);
        copy.appendChild(name);

        const meta = document.createElement('div');
        meta.className = 'draft-attachment-meta';
        meta.textContent = getAttachmentTitle(attachment.kind) + ' · ' + formatBytes(attachment.size || 0);
        copy.appendChild(meta);
        row.appendChild(copy);

        const removeBtn = document.createElement('button');
        removeBtn.type = 'button';
        removeBtn.className = 'draft-remove';
        removeBtn.textContent = 'Remove';
        removeBtn.onclick = function() {
            removeDraftAttachment(attachment.id);
        };
        row.appendChild(removeBtn);

        draftAttachmentsEl.appendChild(row);
    });

    setComposerState(loadingDiv.classList.contains('active'));
}

function removeDraftAttachment(id) {
    draftAttachments = draftAttachments.filter(function(attachment) {
        if (attachment.id === id) {
            revokeAttachmentPreview(attachment);
            return false;
        }
        return true;
    });
    renderDraftAttachments();
}

function clearDraftAttachments() {
    draftAttachments.forEach(revokeAttachmentPreview);
    draftAttachments = [];
    renderDraftAttachments();
}

function revokeAttachmentPreview(attachment) {
    if (attachment && attachment.preview_url && attachment.preview_url.indexOf('blob:') === 0) {
        URL.revokeObjectURL(attachment.preview_url);
    }
}

function renderMessageAttachments(attachments) {
    if (!attachments || attachments.length === 0) return null;

    const wrapper = document.createElement('div');
    wrapper.className = 'message-attachments';

    attachments.forEach(function(attachment) {
        const card = document.createElement('div');
        card.className = 'attachment-card';

        if (attachment.kind === 'image') {
            const image = document.createElement('img');
            image.alt = attachment.name || 'Image attachment';
            image.src = attachmentDataURL(attachment);
            card.appendChild(image);
        } else if (attachment.kind === 'audio') {
            const audio = document.createElement('audio');
            audio.controls = true;
            audio.src = attachmentDataURL(attachment);
            card.appendChild(audio);
        }

        const meta = document.createElement('div');
        meta.className = 'attachment-meta';

        const badge = document.createElement('span');
        badge.className = 'attachment-kind';
        badge.textContent = getAttachmentTitle(attachment.kind);
        meta.appendChild(badge);

        const name = document.createElement('span');
        name.textContent = attachment.name || getAttachmentTitle(attachment.kind);
        meta.appendChild(name);
        card.appendChild(meta);

        if (attachment.transcript) {
            const transcript = document.createElement('div');
            transcript.className = 'attachment-transcript';
            transcript.textContent = 'Transcript: ' + attachment.transcript;
            card.appendChild(transcript);
        }

        wrapper.appendChild(card);
    });

    return wrapper;
}

function cloneAttachmentsForTransport(attachments) {
    return attachments.map(function(attachment) {
        return {
            kind: attachment.kind,
            name: attachment.name,
            mime_type: attachment.mime_type,
            data: attachment.data,
            size: attachment.size,
            transcript: attachment.transcript || ''
        };
    });
}

function cloneAttachmentsForMessage(attachments) {
    return attachments.map(function(attachment) {
        return {
            kind: attachment.kind,
            name: attachment.name,
            mime_type: attachment.mime_type,
            data: attachment.data,
            size: attachment.size,
            preview_url: attachment.preview_url || '',
            transcript: attachment.transcript || ''
        };
    });
}

function attachmentDataURL(attachment) {
    if (attachment.preview_url) return attachment.preview_url;
    if (!attachment.data) return '';
    return 'data:' + (attachment.mime_type || 'application/octet-stream') + ';base64,' + attachment.data;
}

function getAttachmentTitle(kind) {
    if (kind === 'audio') return 'Audio';
    if (kind === 'image') return 'Image';
    return 'Attachment';
}

function formatBytes(size) {
    if (!size) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB'];
    let value = size;
    let unitIndex = 0;
    while (value >= 1024 && unitIndex < units.length - 1) {
        value = value / 1024;
        unitIndex++;
    }
    const rounded = unitIndex === 0 ? String(Math.round(value)) : value.toFixed(1);
    return rounded + ' ' + units[unitIndex];
}

function extractBase64(dataUrl) {
    const parts = String(dataUrl || '').split(',');
    return parts.length > 1 ? parts[1] : '';
}

function readFileAsDataURL(file) {
    return new Promise(function(resolve, reject) {
        const reader = new FileReader();
        reader.onload = function() { resolve(reader.result); };
        reader.onerror = function() { reject(reader.error || new Error('Failed to read file.')); };
        reader.readAsDataURL(file);
    });
}

function readBlobAsDataURL(blob) {
    return new Promise(function(resolve, reject) {
        const reader = new FileReader();
        reader.onload = function() { resolve(reader.result); };
        reader.onerror = function() { reject(reader.error || new Error('Failed to read audio blob.')); };
        reader.readAsDataURL(blob);
    });
}

function createWavBlob(chunks, sourceSampleRate, targetSampleRate) {
    const merged = mergeFloat32Chunks(chunks);
    const downsampled = downsampleBuffer(merged, sourceSampleRate, targetSampleRate);
    const wavBuffer = encodeWAV(downsampled, targetSampleRate);
    return new Blob([wavBuffer], { type: 'audio/wav' });
}

function mergeFloat32Chunks(chunks) {
    let totalLength = 0;
    chunks.forEach(function(chunk) {
        totalLength += chunk.length;
    });

    const merged = new Float32Array(totalLength);
    let offset = 0;
    chunks.forEach(function(chunk) {
        merged.set(chunk, offset);
        offset += chunk.length;
    });
    return merged;
}

function downsampleBuffer(buffer, inputRate, outputRate) {
    if (!buffer || buffer.length === 0) return new Float32Array(0);
    if (inputRate === outputRate) return buffer;

    const ratio = inputRate / outputRate;
    const newLength = Math.round(buffer.length / ratio);
    const result = new Float32Array(newLength);
    let offsetResult = 0;
    let offsetBuffer = 0;

    while (offsetResult < newLength) {
        const nextOffsetBuffer = Math.round((offsetResult + 1) * ratio);
        let accum = 0;
        let count = 0;

        for (let i = offsetBuffer; i < nextOffsetBuffer && i < buffer.length; i++) {
            accum += buffer[i];
            count++;
        }

        result[offsetResult] = count > 0 ? accum / count : 0;
        offsetResult++;
        offsetBuffer = nextOffsetBuffer;
    }

    return result;
}

function encodeWAV(samples, sampleRate) {
    const buffer = new ArrayBuffer(44 + samples.length * 2);
    const view = new DataView(buffer);

    writeAscii(view, 0, 'RIFF');
    view.setUint32(4, 36 + samples.length * 2, true);
    writeAscii(view, 8, 'WAVE');
    writeAscii(view, 12, 'fmt ');
    view.setUint32(16, 16, true);
    view.setUint16(20, 1, true);
    view.setUint16(22, 1, true);
    view.setUint32(24, sampleRate, true);
    view.setUint32(28, sampleRate * 2, true);
    view.setUint16(32, 2, true);
    view.setUint16(34, 16, true);
    writeAscii(view, 36, 'data');
    view.setUint32(40, samples.length * 2, true);

    let offset = 44;
    for (let i = 0; i < samples.length; i++) {
        const sample = Math.max(-1, Math.min(1, samples[i]));
        view.setInt16(offset, sample < 0 ? sample * 0x8000 : sample * 0x7fff, true);
        offset += 2;
    }

    return buffer;
}

function writeAscii(view, offset, text) {
    for (let i = 0; i < text.length; i++) {
        view.setUint8(offset + i, text.charCodeAt(i));
    }
}
