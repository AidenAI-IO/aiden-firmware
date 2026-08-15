// Conversation message and episode rendering.
function createMessageNode(msg) {
    const card = document.createElement('article');
    card.className = 'message ' + normalizeType(msg.type);

    const shell = document.createElement('div');
    shell.className = 'message-shell';

    const avatar = document.createElement('div');
    avatar.className = 'message-avatar';
    avatar.textContent = getAvatarLabel(msg.type);

    const body = document.createElement('div');
    body.className = 'message-body';

    const role = document.createElement('div');
    role.className = 'message-role';
    role.textContent = getRoleLabel(msg.type, msg.tool_name, msg.role);

    // Add voice indicator for voice messages
    if (msg.source === 'voice') {
        const voiceIcon = document.createElement('span');
        voiceIcon.className = 'voice-icon';
        voiceIcon.textContent = ' 🎤';
        voiceIcon.title = 'Voice message';
        role.appendChild(voiceIcon);
    }

    body.appendChild(role);

    if (msg.type === 'tool_call') {
        body.appendChild(renderToolCall(msg));
    } else if (msg.type === 'tool_result') {
        body.appendChild(renderToolResult(msg));
    } else {
        const attachmentsEl = renderMessageAttachments(msg.attachments || []);
        if (attachmentsEl) {
            body.appendChild(attachmentsEl);
        }

        if (msg.content) {
            const contentDiv = document.createElement('div');
            contentDiv.className = 'message-copy';
            contentDiv.textContent = msg.content || '';
            body.appendChild(contentDiv);
        }
    }

    const episodeLink = renderEpisodeLink(msg);
    if (episodeLink) {
        body.appendChild(episodeLink);
    }

    // Add audio playback button for voice messages with archived audio
    if (msg.audio_file && msg.audio_file !== '') {
        const audioBtn = document.createElement('button');
        audioBtn.type = 'button';
        audioBtn.className = 'audio-playback-btn';
        audioBtn.textContent = '▶️ Play Audio';

        const filename = msg.audio_file.split('/').pop();
        const audioUrl = '/api/audio/' + encodeURIComponent(filename);

        if (msg.audio_duration_ms > 0) {
            const durationSec = (msg.audio_duration_ms / 1000).toFixed(1);
            audioBtn.title = 'Duration: ' + durationSec + 's';
        }

        audioBtn.addEventListener('click', function() {
            playAudio(audioUrl, audioBtn);
        });

        body.appendChild(audioBtn);
    }

    const timeDiv = document.createElement('div');
    timeDiv.className = 'message-time';
    timeDiv.textContent = formatTime(msg.timestamp);
    body.appendChild(timeDiv);

    shell.appendChild(avatar);
    shell.appendChild(body);
    card.appendChild(shell);
    return card;
}

function playAudio(url, button) {
    const audio = new Audio(url);

    const originalText = button.textContent;
    button.textContent = '⏸️ Playing...';
    button.disabled = true;

    audio.onended = function() {
        button.textContent = originalText;
        button.disabled = false;
    };

    audio.onerror = function() {
        button.textContent = '❌ Error';
        setTimeout(function() {
            button.textContent = originalText;
            button.disabled = false;
        }, 2000);
    };

    audio.play().catch(function(err) {
        console.error('[Audio] Play failed:', err);
        button.textContent = '❌ Error';
        setTimeout(function() {
            button.textContent = originalText;
            button.disabled = false;
        }, 2000);
    });
}

function renderEpisodeLink(msg) {
    if (!msg.episode_id) return null;
    if (msg.type !== 'user' && msg.type !== 'assistant' && msg.type !== 'episode_status') return null;

    const wrap = document.createElement('div');
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'episode-link';
    button.textContent = 'Trace' + (msg.status ? ' · ' + msg.status : '');
    wrap.appendChild(button);

    let panel = null;
    button.addEventListener('click', async function() {
        if (panel) {
            panel.classList.toggle('hidden');
            return;
        }
        panel = document.createElement('div');
        panel.className = 'episode-panel';
        panel.textContent = 'Loading trace...';
        wrap.appendChild(panel);

        try {
            const episode = await loadEpisode(msg.episode_id);
            renderEpisodePanel(panel, episode);
        } catch (err) {
            panel.textContent = 'Trace unavailable: ' + err.message;
        }
    });
    return wrap;
}

async function loadEpisode(id) {
    if (episodeCache[id]) return episodeCache[id];
    const res = await fetch('/api/episodes/' + encodeURIComponent(id));
    if (!res.ok) {
        throw new Error(await res.text() || 'Episode not found');
    }
    const data = await res.json();
    episodeCache[id] = data.episode;
    return data.episode;
}

function renderEpisodePanel(panel, episode) {
    panel.innerHTML = '';
    const meta = document.createElement('div');
    meta.className = 'episode-meta';
    const outcome = episode.outcome || {};
    meta.textContent = [
        episode.id || 'episode',
        episode.status || 'unknown',
        outcome.success ? 'success' : (outcome.failure_reason ? 'failed' : 'pending')
    ].join(' · ');
    panel.appendChild(meta);

    const events = (episode.events || []).slice(-12);
    if (events.length === 0) {
        const empty = document.createElement('div');
        empty.className = 'episode-meta';
        empty.textContent = 'No trace events recorded.';
        panel.appendChild(empty);
        return;
    }

    const list = document.createElement('div');
    list.className = 'episode-events';
    events.forEach(function(event) {
        list.appendChild(renderEpisodeEvent(event));
    });
    panel.appendChild(list);
}

function renderEpisodeEvent(event) {
    const item = document.createElement('div');
    item.className = 'episode-event';

    const title = document.createElement('div');
    title.className = 'episode-event-title';
    const parts = [event.type || 'event'];
    if (event.role) parts.push(event.role);
    if (event.tool_name) parts.push(event.tool_name);
    title.textContent = parts.join(' · ');
    item.appendChild(title);

    const content = document.createElement('div');
    content.className = 'episode-event-content';
    content.textContent = episodeEventText(event);
    item.appendChild(content);
    return item;
}

function episodeEventText(event) {
    if (event.next_step) return event.next_step;
    if (event.content) return event.content;
    if (event.observation) return formatToolPayload(event.observation);
    if (event.reason) return event.reason;
    if (event.plan && event.plan.length) return event.plan.join('\n');
    return '';
}

function updateEmptyState() {
    emptyStateEl.classList.toggle('hidden', messagesDiv.children.length > 0);
}

function setComposerState(isLoading) {
    sendBtn.disabled = recorderState.isStopping || pendingSteerSubmitting || (isLoading && !currentChatRequestId);
    sendBtn.textContent = currentChatRequestId ? 'Steer' : 'Send';
    const imageSlotsFull = getDraftImageCount() >= maxDraftImageAttachments;
    imageBtn.disabled = isLoading || recorderState.isRecording || recorderState.isStopping || imageSlotsFull;
    imageBtn.title = imageSlotsFull ? 'Maximum 4 images attached' : 'Add image';
    recordBtn.disabled = isLoading || recorderState.isStopping;
    stopRunBtn.disabled = !isLoading;
    if (!isLoading) {
        resetStopRunArm();
    } else if (Date.now() < stopRunArmedUntil) {
        stopRunBtn.textContent = 'Click again to stop';
    } else {
        stopRunBtn.textContent = 'Stop';
    }
    loadingDiv.classList.toggle('active', isLoading);
    renderPendingSteer();
    updateRecordButton();
}

function renderPendingSteer() {
    if (!pendingSteer) {
        pendingSteerEl.classList.add('hidden');
        pendingSteerTextEl.textContent = '';
        cancelSteerBtn.disabled = true;
        return;
    }
    pendingSteerEl.classList.remove('hidden');
    pendingSteerTextEl.textContent = pendingSteer.content || '';
    cancelSteerBtn.disabled = pendingSteerSubmitting;
}

function autoResizeInput() {
    inputEl.style.height = 'auto';
    inputEl.style.height = Math.min(inputEl.scrollHeight, 180) + 'px';
}

function scrollToBottom() {
    conversationEl.scrollTop = conversationEl.scrollHeight;
}

function normalizeType(type) {
    return type || 'assistant';
}

function getRoleLabel(type, toolName, role) {
    if (type === 'user') return 'You';
    if (type === 'steer') return 'Steer';
    if (type === 'episode_status') return 'Task Trace';
    if (type === 'role_output') return 'Role · ' + (role || 'agent');
    if (type === 'tool_call') return toolName ? 'Tool Call · ' + toolName : 'Tool Call';
    if (type === 'tool_result') return toolName ? 'Tool Result · ' + toolName : 'Tool Result';
    return 'Aiden';
}

function getAvatarLabel(type) {
    if (type === 'user') return 'You';
    if (type === 'steer') return 'Edit';
    if (type === 'episode_status') return 'Task';
    if (type === 'role_output') return 'Role';
    if (type === 'tool_call') return 'Call';
    if (type === 'tool_result') return 'Tool';
    return 'AI';
}

function formatTime(timestamp) {
    const date = new Date(timestamp);
    return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

function openImagePicker() {
    if (imageBtn.disabled) return;
    imageInputEl.click();
}
