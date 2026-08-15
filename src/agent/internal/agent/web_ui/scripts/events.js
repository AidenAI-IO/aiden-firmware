// History loading and server-sent event synchronization.
async function loadHistory() {
    try {
        const res = await fetch('/api/history');
        const history = await res.json();
        renderHistory(history);
    } catch (err) {
        console.error('Failed to load history:', err);
    }
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
            const data = JSON.parse(e.data);

            if (data.type === 'connected') {
                return;
            }

            if (data.type) {
                addMessage(data);
            }
        } catch (err) {
            console.error('[SSE] Parse error:', err);
        }
    };

    eventSource.onerror = function(e) {
        console.error('[SSE] Connection error, will retry...');
    };
}

document.addEventListener('visibilitychange', function() {
    if (!document.hidden && (!eventSource || eventSource.readyState === EventSource.CLOSED)) {
        connectSSE();
    }
});
