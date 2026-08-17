// Tool Lab catalog, invocation, and skill export controls.
async function loadToolCatalog() {
    try {
        const res = await fetch('/api/tools');
        if (!res.ok) {
            throw new Error(await res.text() || 'Failed to load tools');
        }
        const data = await res.json();
        toolCatalog = data.tools || [];
        renderToolCatalog();
    } catch (err) {
        console.error('Failed to load tools:', err);
        toolCatalog = [];
        toolSelectEl.innerHTML = '<option value="">Tools unavailable</option>';
        toolSelectEl.disabled = true;
        toolInputEl.disabled = true;
        toolExampleBtnEl.disabled = true;
        toolInvokeBtnEl.disabled = true;
        toolDescriptionEl.textContent = 'Tool metadata failed to load: ' + err.message;
        toolStatusEl.textContent = 'Failed to load tools.';
        toolStatusEl.classList.add('error');
    }
}

function renderToolCatalog() {
    toolSelectEl.innerHTML = '';

    if (toolCatalog.length === 0) {
        toolSelectEl.innerHTML = '<option value="">No tools exposed</option>';
        toolSelectEl.disabled = true;
        toolInputEl.disabled = true;
        toolExampleBtnEl.disabled = true;
        toolInvokeBtnEl.disabled = true;
        return;
    }

    toolSelectEl.disabled = false;
    toolInputEl.disabled = false;
    toolExampleBtnEl.disabled = false;
    toolInvokeBtnEl.disabled = false;

    toolCatalog.forEach(function(tool) {
        const option = document.createElement('option');
        option.value = tool.name;
        option.textContent = tool.name;
        toolSelectEl.appendChild(option);
    });

    syncSelectedTool();
}

function getSelectedTool() {
    const selectedName = toolSelectEl.value;
    for (let i = 0; i < toolCatalog.length; i++) {
        if (toolCatalog[i].name === selectedName) {
            return toolCatalog[i];
        }
    }
    return toolCatalog.length > 0 ? toolCatalog[0] : null;
}

function syncSelectedTool() {
    const tool = getSelectedTool();
    if (!tool) return;

    const switched = toolInputEl.dataset.toolName !== tool.name;
    if (switched) {
        clearToolResultPreview();
    }

    if (toolSelectEl.value !== tool.name) {
        toolSelectEl.value = tool.name;
    }

    renderToolDescription(tool);
    toolInputEl.placeholder = tool.example_input || '';
    if (switched) {
        toolInputEl.value = tool.example_input || '';
        toolInputEl.dataset.toolName = tool.name;
    }
    toolStatusEl.classList.remove('error');
}

function renderToolDescription(tool) {
    toolDescriptionEl.textContent = tool.description || '';
    if (!tool || tool.name !== 'keyboard_tap') {
        return;
    }

    toolDescriptionEl.appendChild(document.createTextNode(' '));
    const link = document.createElement('a');
    link.href = '/keyboard-tap-android-keys';
    link.target = '_blank';
    link.rel = 'noopener noreferrer';
    link.textContent = 'Open Android key guide';
    toolDescriptionEl.appendChild(link);
}

function loadSelectedToolExample() {
    const tool = getSelectedTool();
    if (!tool) return;
    toolInputEl.value = tool.example_input || '';
    toolStatusEl.textContent = 'Example loaded.';
    toolStatusEl.classList.remove('error');
}

async function invokeSelectedTool() {
    const tool = getSelectedTool();
    if (!tool || toolInvokeBtnEl.disabled) return;

    toolInvokeBtnEl.disabled = true;
    toolExampleBtnEl.disabled = true;
    toolStatusEl.textContent = 'Running...';
    toolStatusEl.classList.remove('error');

    try {
        const res = await fetch(tool.http.path, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                raw_input: toolInputEl.value
            })
        });

        if (!res.ok) {
            throw new Error(await res.text() || 'Tool call failed');
        }

        const data = await res.json();
        renderToolInvokeResult(data);
    } catch (err) {
        console.error('Failed to invoke tool:', err);
        toolStatusEl.textContent = 'Tool call failed: ' + err.message;
        toolStatusEl.classList.add('error');
        toolResultPanelEl.classList.add('hidden');
    } finally {
        toolInvokeBtnEl.disabled = false;
        toolExampleBtnEl.disabled = false;
    }
}

function renderToolInvokeResult(result) {
    toolResultPanelEl.classList.remove('hidden');
    toolResultMetaEl.textContent = [
        result.tool && result.tool.name ? result.tool.name : 'tool',
        result.duration_ms + ' ms',
        result.is_error ? 'error' : 'ok'
    ].join(' · ');
    toolResultOutputEl.textContent = formatToolPayload(result.output || '');

    const screenshot = parseScreenshotOutput(result.tool ? result.tool.name : '', result.output || '');
    if (screenshot) {
        toolResultPreviewWrapEl.classList.remove('hidden');
        toolResultPreviewEl.src = 'data:image/' + (screenshot.format || 'jpeg') + ';base64,' + screenshot.data;
    } else {
        clearToolResultPreview();
    }

    if (result.is_error) {
        toolStatusEl.textContent = result.error || 'Error';
        toolStatusEl.classList.add('error');
    } else {
        toolStatusEl.textContent = 'Done at ' + formatTime(result.called_at) + '.';
        toolStatusEl.classList.remove('error');
    }
}

function clearToolResultPreview() {
    toolResultPreviewWrapEl.classList.add('hidden');
    toolResultPreviewEl.removeAttribute('src');
}

async function loadToolSkills() {
    try {
        const res = await fetch('/api/tool-skills');
        if (!res.ok) {
            throw new Error(await res.text() || 'Failed to load tool skills');
        }
        const data = await res.json();
        toolSkills = data.skills || [];
        renderToolSkills();
    } catch (err) {
        console.error('Failed to load tool skills:', err);
        toolSkills = [];
        skillSelectEl.innerHTML = '<option value="">Skills unavailable</option>';
        skillSelectEl.disabled = true;
        skillMarkdownEl.value = '';
        copySkillBtnEl.disabled = true;
        skillStatusEl.textContent = 'Skill export unavailable: ' + err.message;
        skillStatusEl.classList.add('error');
    }
}

function renderToolSkills() {
    skillSelectEl.innerHTML = '';

    if (toolSkills.length === 0) {
        skillSelectEl.innerHTML = '<option value="">No generated skills</option>';
        skillSelectEl.disabled = true;
        skillMarkdownEl.value = '';
        copySkillBtnEl.disabled = true;
        return;
    }

    skillSelectEl.disabled = false;
    copySkillBtnEl.disabled = false;

    toolSkills.forEach(function(skill) {
        const option = document.createElement('option');
        option.value = skill.name;
        option.textContent = skill.name;
        skillSelectEl.appendChild(option);
    });

    syncSelectedSkill();
}

function getSelectedSkill() {
    const selectedName = skillSelectEl.value;
    for (let i = 0; i < toolSkills.length; i++) {
        if (toolSkills[i].name === selectedName) {
            return toolSkills[i];
        }
    }
    return toolSkills.length > 0 ? toolSkills[0] : null;
}

function syncSelectedSkill() {
    const skill = getSelectedSkill();
    if (!skill) return;
    if (skillSelectEl.value !== skill.name) {
        skillSelectEl.value = skill.name;
    }
    skillMarkdownEl.value = skill.markdown || '';
    skillStatusEl.textContent = skill.description || '';
    skillStatusEl.classList.remove('error');
}

async function copySelectedSkill() {
    const skill = getSelectedSkill();
    if (!skill || !skill.markdown) return;

    try {
        if (navigator.clipboard && navigator.clipboard.writeText) {
            await navigator.clipboard.writeText(skill.markdown);
        } else {
            skillMarkdownEl.focus();
            skillMarkdownEl.select();
            document.execCommand('copy');
        }
        skillStatusEl.textContent = 'Copied.';
        skillStatusEl.classList.remove('error');
    } catch (err) {
        console.error('Failed to copy skill:', err);
        skillStatusEl.textContent = 'Copy failed.';
        skillStatusEl.classList.add('error');
    }
}
