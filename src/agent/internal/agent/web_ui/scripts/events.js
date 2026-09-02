// History loading and server-sent event synchronization.
async function loadHistory() {
    if (typeof currentChatRequestId !== 'undefined' && currentChatRequestId) return;
    try {
        const res = await fetch('/api/history');
        if (!res.ok) throw new Error(await res.text() || 'Failed to load context history');
        const history = await res.json();
        renderHistory(history);
    } catch (err) {
        console.error('Failed to load history:', err);
    }
}

let historyRefreshTimer = null;
function refreshHistoryFromContext() {
    if (typeof currentChatRequestId !== 'undefined' && currentChatRequestId) return;
    if (historyRefreshTimer) clearTimeout(historyRefreshTimer);
    historyRefreshTimer = setTimeout(function() {
        historyRefreshTimer = null;
        loadHistory();
    }, 80);
}

function connectSSE() {
    if (eventSource) {
        eventSource.close();
    }

    eventSource = new EventSource('/api/events');

    eventSource.onopen = function() {
        console.log('[SSE] Connected');
    };

    eventSource.onmessage = function(e) {
        try {
            handleServerEvent(JSON.parse(e.data));
        } catch (err) {
            console.error('[SSE] Parse error:', err);
        }
    };

    eventSource.onerror = function(e) {
        console.error('[SSE] Connection error, will retry...');
    };
}

function handleServerEvent(data) {
    if (!data || data.type === 'connected') return;
    if (data.type === 'assistant' && data.status === 'streaming') {
        addMessage(data);
        return;
    }
    if (data.type) refreshHistoryFromContext();
}

document.addEventListener('visibilitychange', function() {
    if (!document.hidden && (!eventSource || eventSource.readyState === EventSource.CLOSED)) {
        connectSSE();
    }
});
