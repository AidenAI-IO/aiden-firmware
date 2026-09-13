/**
 * 字段可见性白名单
 * 只有在这个列表中的字段才会在 UI 上显示
 */

export const VISIBLE_FIELDS = {
  // 基础设置
  'agent.locale': true,
  'device.device_type': true,
  'hid.keyboard_layout': true,

  // 主模型设置 (完整 model section)
  'model.provider': true,
  'model.model': true,
  'model.api_mode': true,
  'model.responses_context_management': true,
  'model.responses_compact_threshold': true,
  'model.responses_context_edit_trigger': true,
  'model.responses_context_edit_keep': true,
  'model.responses_context_edit_clear_thinking': true,
  'model.responses_truncation': true,
  'model.responses_include': true,
  'model.temperature': true,
  'model.context_window': true,
  'model.max_response_tokens': true,
  'model.model_max_output_tokens': true,
  'model.reasoning_effort': true,
  'model.reasoning_budget_tokens': true,

  // Realtime Mode (完整 voice_model section)
  'voice_model.provider': true,
  'voice_model.model': true,
  // 其他 voice_model 字段根据 provider 动态显示

  // 语音设置 - STT (完整 stt section)
  'stt.provider': true,
  'stt.model': true,
  'stt.language': true,

  // 语音设置 - TTS (完整 tts section)
  'tts.provider': true,
  'tts.model': true,
  'tts.voice_id': true,
  'tts.speed': true,
  'tts.emotion': true,

  // 对话设置 - 对话模式
  'agent.input_mode': true,
  // Classic mode is selected here; provider-specific STT/TTS fields are
  // rendered by their provider dialogs.
  'agent.max_iterations': true,
  'agent.custom_instruction': true,
  'agent.additional_prompt': true,
  'agent.context_prune_threshold': true,
  'agent.context_compaction_threshold': true,
  'agent.voice_max_response_tokens': true,

  // 对话设置 - websearch tool
  'search.provider': true,
  'search.api_key': true,

  // 记忆设置目前只有保留期对外展示；GPIO 抓取和截图稳定性属于调试项。
  'quick_capture.screen_memory_ttl': true,

  // 高级设置 - 日志
  'log.llm_http_retention_days': true,
  'model.log_raw_http': true,

  // 文档未定义的 audio/frame/storage/telemetry fields remain hidden.
};

/**
 * 应用字段可见性规则
 */
export function applyFieldVisibilityRules() {
  // 隐藏所有不在白名单中的字段
  document.querySelectorAll('[data-config-field]').forEach(fieldEl => {
    const fieldPath = fieldEl.getAttribute('data-config-field');
    if (!VISIBLE_FIELDS[fieldPath]) {
      fieldEl.style.display = 'none';
      fieldEl.classList.add('hidden-by-whitelist');
    }
  });
}
