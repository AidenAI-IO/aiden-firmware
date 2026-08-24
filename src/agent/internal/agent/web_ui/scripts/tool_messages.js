// Tool call/result cards and screenshot payload handling.
function renderToolCall(msg) {
    const card = createToolCard(msg, 'Tool Call', 'tool-call-label');
    const inputSection = document.createElement('div');
    inputSection.className = 'tool-section';

    const inputLabel = document.createElement('div');
    inputLabel.className = 'tool-section-label';
    inputLabel.textContent = 'Input';
    inputSection.appendChild(inputLabel);

    const inputBlock = document.createElement('pre');
    inputBlock.className = 'tool-block';
    renderToolPayload(inputBlock, msg.tool_input || '');
    inputSection.appendChild(inputBlock);

    card.appendChild(inputSection);
    return card;
}

function renderToolResult(msg) {
    const card = createToolCard(msg, 'Tool Result', msg.is_error ? 'tool-result-label error' : 'tool-result-label');
    const screenshot = parseScreenshotPayload(msg);

    if (screenshot) {
        const metaSection = document.createElement('div');
        metaSection.className = 'tool-section';

        const metaLabel = document.createElement('div');
        metaLabel.className = 'tool-section-label';
        metaLabel.textContent = 'Screenshot';
        metaSection.appendChild(metaLabel);

        const grid = document.createElement('div');
        grid.className = 'tool-meta-grid';

        const metaEntries = [
            ['Format', screenshot.format || 'jpeg'],
            ['Width', String(screenshot.width)],
            ['Height', String(screenshot.height)],
            ['Bytes', String(screenshot.size)]
        ];
        if (screenshot.action_output) {
            metaEntries.unshift(['Action', screenshot.action_output]);
        }
        metaEntries.forEach(function(entry) {
            const item = document.createElement('div');
            item.className = 'tool-meta-item';

            const key = document.createElement('div');
            key.className = 'tool-meta-key';
            key.textContent = entry[0];

            const value = document.createElement('div');
            value.className = 'tool-meta-value';
            value.textContent = entry[1];

            item.appendChild(key);
            item.appendChild(value);
            grid.appendChild(item);
        });

        metaSection.appendChild(grid);
        card.appendChild(metaSection);

        const preview = document.createElement('div');
        preview.className = 'screenshot-preview';

        const previewLabel = document.createElement('div');
        previewLabel.className = 'tool-section-label';
        previewLabel.textContent = 'Preview';
        preview.appendChild(previewLabel);

        const image = document.createElement('img');
        image.alt = 'Screenshot preview';
        image.src = 'data:image/' + (screenshot.format || 'jpeg') + ';base64,' + screenshot.data;
        preview.appendChild(image);
        card.appendChild(preview);

        const details = document.createElement('details');
        details.className = 'tool-details';

        const summary = document.createElement('summary');
        summary.textContent = 'Raw payload';
        details.appendChild(summary);

        const rawBlock = document.createElement('pre');
        rawBlock.className = 'tool-block';
        renderToolPayload(rawBlock, msg.content || '');
        details.appendChild(rawBlock);
        card.appendChild(details);

        return card;
    }

    const resultSection = document.createElement('div');
    resultSection.className = 'tool-section';

    const resultLabel = document.createElement('div');
    resultLabel.className = 'tool-section-label';
    resultLabel.textContent = msg.is_error ? 'Error' : 'Output';
    resultSection.appendChild(resultLabel);

    const resultBlock = document.createElement('pre');
    resultBlock.className = 'tool-block';
    renderToolPayload(resultBlock, msg.content || '');
    resultSection.appendChild(resultBlock);
    card.appendChild(resultSection);

    return card;
}

function createToolCard(msg, label, labelClass) {
    const wrapper = document.createElement('div');
    wrapper.className = 'tool-card';

    const header = document.createElement('div');
    header.className = 'tool-card-header';

    const title = document.createElement('div');
    title.className = 'tool-card-title';

    const badge = document.createElement('span');
    badge.className = 'tool-label ' + labelClass;
    badge.textContent = label;

    const name = document.createElement('span');
    name.className = 'tool-name';
    name.textContent = msg.tool_name || 'unknown';

    title.appendChild(badge);
    title.appendChild(name);
    header.appendChild(title);
    wrapper.appendChild(header);

    return wrapper;
}

function formatToolPayload(value) {
    if (!value) return '';
    try {
        return JSON.stringify(redactToolPayloadForDisplay(JSON.parse(value)), null, 2);
    } catch (_) {
        return value;
    }
}

function renderToolPayload(block, value) {
    if (!value) {
        block.textContent = '';
        return;
    }
    try {
        const parsed = redactToolPayloadForDisplay(JSON.parse(value));
        const formatted = JSON.stringify(parsed, null, 2);
        block.classList.add('json-highlight');
        block.innerHTML = highlightJson(formatted);
    } catch (_) {
        block.textContent = value;
    }
}

function highlightJson(value) {
    const tokenPattern = /(\"(?:\\.|[^\"\\])*\"(?=\s*:))|(\"(?:\\.|[^\"\\])*\")|(-?\b\d+(?:\.\d+)?(?:[eE][+-]?\d+)?\b)|(\btrue\b|\bfalse\b)|(\bnull\b)/g;
    let html = '';
    let lastIndex = 0;
    let match;
    while ((match = tokenPattern.exec(value)) !== null) {
        html += escapeHtml(value.slice(lastIndex, match.index));
        const tokenClass = match[1] ? 'json-key' : match[2] ? 'json-string' : match[3] ? 'json-number' : match[4] ? 'json-boolean' : 'json-null';
        html += '<span class="' + tokenClass + '">' + escapeHtml(match[0]) + '</span>';
        lastIndex = match.index + match[0].length;
    }
    return html + escapeHtml(value.slice(lastIndex));
}

function escapeHtml(value) {
    return String(value).replace(/[&<>\"']/g, function(character) {
        return {'&': '&amp;', '<': '&lt;', '>': '&gt;', '\"': '&quot;', "'": '&#39;'}[character];
    });
}

function redactToolPayloadForDisplay(value) {
    if (Array.isArray(value)) {
        return value.map(redactToolPayloadForDisplay);
    }
    if (!value || typeof value !== 'object') {
        return value;
    }

    const clone = {};
    Object.keys(value).forEach(function(key) {
        clone[key] = redactToolPayloadForDisplay(value[key]);
    });
    if (isScreenshotPayload(value)) {
        const bytes = Number(value.size);
        const byteLabel = Number.isFinite(bytes) && bytes >= 0 ? bytes + ' bytes' : 'base64 omitted';
        clone.data = '[base64 screenshot omitted: ' + byteLabel + ']';
    }
    return clone;
}

function parseScreenshotPayload(msg) {
    return parseScreenshotOutput(msg.tool_name || '', msg.content || '');
}

function parseScreenshotOutput(toolName, content) {
    if (!content) return null;
    try {
        const parsed = JSON.parse(content);
        if (!isScreenshotPayload(parsed)) return null;
        return parsed;
    } catch (_) {
        return null;
    }
}

function isScreenshotPayload(value) {
    return !!value &&
        typeof value === 'object' &&
        typeof value.data === 'string' &&
        value.data.length > 0 &&
        (value.format === 'jpeg' || value.format === 'jpg' || value.format === 'png' || value.width || value.height || value.size);
}
