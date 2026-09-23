/**
 * Field visibility allowlist.
 * Only fields listed here are rendered in the UI.
 */

export const VISIBLE_FIELDS = {
  // Basic settings
  'agent.locale': true,
  'agent.timezone': true,
  'device.device_type': true,
  'hid.keyboard_layout': true,

  // Model settings
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

  // Provider records are edited in modal dialogs.  Keep their metadata
  // fields in the same whitelist as the top-level configuration fields so
  // conditional provider-specific controls are not hidden after rendering.
  'model_providers.type': true,
  'model_providers.api_key': true,
  'model_providers.base_url': true,

  // Realtime mode
  'voice_model.provider': true,
  'voice_model.use_backend_agent': true,
  'voice_model.model': true,
  // Other voice_model fields are shown dynamically for each provider.

  // Classic voice settings: STT
  'stt.provider': true,
  'stt.model': true,
  'stt.language': true,

  // Classic voice settings: TTS
  'tts.provider': true,
  'tts.model': true,
  'tts.voice_id': true,
  'tts.speed': true,
  'tts.emotion': true,

  // Classic TTS provider records
  'tts_providers.type': true,
  'tts_providers.api_key': true,
  'tts_providers.model': true,
  'tts_providers.voice_id': true,
  'tts_providers.reference_id': true,
  'tts_providers.emotion': true,

  // Classic STT provider records
  'stt_providers.type': true,
  'stt_providers.api_key': true,
  'stt_providers.model': true,
  'stt_providers.base_url': true,
  'stt_providers.app_id': true,
  'stt_providers.secret_id': true,
  'stt_providers.secret_key': true,
  'stt_providers.region': true,
  'stt_providers.engine_model_type': true,

  // Realtime provider records
  'voice_model_providers.type': true,
  'voice_model_providers.upstream_provider': true,
  'voice_model_providers.agent_id': true,
  'voice_model_providers.api_key': true,
  'voice_model_providers.auth_mode': true,
  'voice_model_providers.project_id': true,
  'voice_model_providers.location': true,
  'voice_model_providers.model': true,
  'voice_model_providers.workspace_id': true,
  'voice_model_providers.endpoint': true,
  'voice_model_providers.realtime_protocol': true,
  'voice_model_providers.thinking_level': true,
  'voice_model_providers.base_url': true,
  'voice_model_providers.region': true,
  'voice_model_providers.turn_detection': true,
  'voice_model_providers.turn_detection_threshold': true,
  'voice_model_providers.turn_detection_silence_ms': true,
  'voice_model_providers.voice': true,

  // Conversation mode
  'agent.input_mode': true,
  // Classic mode is selected here; provider-specific STT/TTS fields are
  // rendered by their provider dialogs.
  'agent.max_iterations': true,
  'agent.prompt': true,
  'agent.context_prune_threshold': true,
  'agent.context_compaction_threshold': true,

  // Web search tool
  'search.provider': true,
  'search.api_key': true,

  // Memory settings expose retention only. GPIO capture and screenshot
  // stability remain internal diagnostics.
  'quick_capture.screen_memory_ttl': true,
  'voice_notifications.retention_days': true,

  // Advanced settings: logs
  'log.level': true,
  'log.llm_http_retention_days': true,
  'model.log_raw_http': true,

  // Audio, frame, storage, and telemetry fields without product controls remain hidden.
};

/**
 * Apply field visibility rules.
 */
export function applyFieldVisibilityRules() {
  // Hide every field outside the allowlist.
  document.querySelectorAll('[data-config-field]').forEach(fieldEl => {
    const fieldPath = fieldEl.getAttribute('data-config-field');
    if (!VISIBLE_FIELDS[fieldPath]) {
      fieldEl.style.display = 'none';
      fieldEl.classList.add('hidden-by-whitelist');
    }
  });
}
