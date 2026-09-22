/**
 * Configuration grouping metadata for Agent Settings.
 * Defines logical groups of configuration fields for a more user-friendly UI.
 *
 * Wi-Fi, Bluetooth, and About use dedicated Config Web controls. The remaining
 * entries describe the product settings hierarchy and its metadata-backed fields.
 */

export const AGENT_SETTINGS_GROUPS = {
  basic_settings: {
    id: 'basic_settings',
    titleKey: 'groups.basic_settings',
    title: 'Basic Settings',
    order: 2,
    subsections: {
      language_timezone: {
        id: 'language_timezone',
        titleKey: 'groups.language_timezone',
        title: 'Language & Time Zone',
        order: 1,
        fields: [
          { path: 'agent.locale', section: 'agent', key: 'locale' },
          { path: 'agent.timezone', section: 'agent', key: 'timezone' },
        ]
      },
      device_settings: {
        id: 'device_settings',
        titleKey: 'groups.device_settings',
        title: 'Device Settings',
        order: 2,
        sections: ['device', 'hid'],
        fields: [
          { path: 'device.device_type', section: 'device', key: 'device_type' },
          { path: 'hid.keyboard_layout', section: 'hid', key: 'keyboard_layout' },
        ]
      }
    }
  },

  conversation_settings: {
    id: 'conversation_settings',
    titleKey: 'groups.conversation_settings',
    title: 'Conversation Settings',
    order: 3,
    subsections: {
      prompts: {
        id: 'prompts',
        titleKey: 'groups.prompts',
        title: 'Agent Prompt',
        order: 1,
        fields: [
          { path: 'agent.prompt', section: 'agent', key: 'prompt' },
        ]
      },
      iteration_control: {
        id: 'iteration_control',
        titleKey: 'groups.iteration_control',
        title: 'Max Iterations',
        order: 2,
        fields: [
          { path: 'agent.max_iterations', section: 'agent', key: 'max_iterations' },
        ]
      },
      context_management: {
        id: 'context_management',
        titleKey: 'groups.context_management',
        title: 'Context Management',
        order: 3,
        fields: [
          { path: 'agent.context_prune_threshold', section: 'agent', key: 'context_prune_threshold' },
          { path: 'agent.context_compaction_threshold', section: 'agent', key: 'context_compaction_threshold' },
          { path: 'agent.screenshot_keep_n', section: 'agent', key: 'screenshot_keep_n' },
          { path: 'agent.screenshot_prune_interval', section: 'agent', key: 'screenshot_prune_interval' },
        ]
      },
      tool_settings: {
        id: 'tool_settings',
        titleKey: 'groups.tool_settings',
        title: 'Tool Settings',
        order: 4,
        subsections: {
          websearch: {
            id: 'websearch',
            titleKey: 'groups.websearch',
            title: 'Web Search',
            order: 1,
            sections: ['search'],
            fields: [
              { path: 'search.provider', section: 'search', key: 'provider' },
              { path: 'search.api_key', section: 'search', key: 'api_key', isSecret: true }
            ]
          },
          termination_policy: {
            id: 'termination_policy',
            titleKey: 'groups.termination_policy',
            title: 'Termination Policy',
            order: 2,
            sections: ['termination_policy']
          }
        }
      }
    }
  },

  model_settings: {
    id: 'model_settings',
    titleKey: 'groups.model_settings',
    title: 'Model Settings',
    order: 4,
    sections: ['model', 'model_providers'],
    fields: [
      { path: 'model.provider', section: 'model', key: 'provider', required: true },
      { path: 'model.model', section: 'model', key: 'model', required: true },
      { path: 'model.temperature', section: 'model', key: 'temperature' },
      { path: 'model.max_response_tokens', section: 'model', key: 'max_response_tokens' },
      { path: 'model.reasoning_effort', section: 'model', key: 'reasoning_effort' },
      { path: 'model.context_window', section: 'model', key: 'context_window' },
      { path: 'model.model_max_output_tokens', section: 'model', key: 'model_max_output_tokens' },
      { path: 'model.api_mode', section: 'model', key: 'api_mode' },
      { path: 'model.responses_context_management', section: 'model', key: 'responses_context_management' },
      { path: 'model.responses_compact_threshold', section: 'model', key: 'responses_compact_threshold' },
      { path: 'model.responses_truncation', section: 'model', key: 'responses_truncation' },
      { path: 'model.responses_include', section: 'model', key: 'responses_include' },
    ]
  },

  voice_settings: {
    id: 'voice_settings',
    titleKey: 'groups.voice_settings',
    title: 'Voice Settings',
    order: 5,
    subsections: {
      voice_mode: {
        id: 'voice_mode',
        titleKey: 'groups.voice_mode',
        title: 'Voice Mode',
        order: 0,
        fields: [
          { path: 'agent.input_mode', section: 'agent', key: 'input_mode' },
        ]
      },
      realtime_mode: {
        id: 'realtime_mode',
        titleKey: 'groups.realtime_mode',
        title: 'Realtime Mode',
        order: 1,
        visibleWhen: { field: 'agent.input_mode', value: 'realtime' },
        sections: ['voice_model', 'voice_model_providers'],
        fields: [
          { path: 'voice_model.provider', section: 'voice_model', key: 'provider', required: true },
        ]
      },
      classic_mode: {
        id: 'classic_mode',
        titleKey: 'groups.classic_mode',
        title: 'Classic Mode (STT + TTS)',
        order: 2,
        visibleWhen: { field: 'agent.input_mode', value: 'stt' },
        subsections: {
          vad_settings: {
            id: 'vad_settings',
            titleKey: 'groups.vad_settings',
            title: 'VAD Settings',
            order: 1,
            fields: [
              { path: 'agent.vad_backend', section: 'agent', key: 'vad_backend' },
              { path: 'agent.vad_speech_threshold', section: 'agent', key: 'vad_speech_threshold' },
              { path: 'agent.silence_ms', section: 'agent', key: 'silence_ms' },
              { path: 'agent.min_speech_ms', section: 'agent', key: 'min_speech_ms' },
            ]
          },
          stt: {
            id: 'stt',
            titleKey: 'groups.stt',
            title: 'STT',
            order: 2,
            testable: true,
            sections: ['stt', 'stt_providers'],
            fields: [
              { path: 'stt.provider', section: 'stt', key: 'provider', required: true },
              { path: 'stt.language', section: 'stt', key: 'language' },
            ]
          },
          tts: {
            id: 'tts',
            titleKey: 'groups.tts',
            title: 'TTS',
            order: 3,
            testable: true,
            sections: ['tts', 'tts_providers'],
            fields: [
              { path: 'tts.provider', section: 'tts', key: 'provider', required: true },
              { path: 'tts.speed', section: 'tts', key: 'speed' },
            ]
          },
          audio: {
            id: 'audio',
            titleKey: 'groups.audio',
            title: 'Audio Configuration',
            order: 4,
            sections: ['audio'],
          },
          audio_archive: {
            id: 'audio_archive',
            titleKey: 'groups.audio_archive',
            title: 'Audio Archive',
            order: 5,
            sections: ['audio_archive'],
          }
        }
      }
    }
  },

  memory_settings: {
    id: 'memory_settings',
    titleKey: 'groups.memory_settings',
    title: 'Memory Settings',
    order: 6,
    subsections: {
      screen_memory: {
        id: 'screen_memory',
        titleKey: 'groups.screen_memory',
        title: 'Screen Memory Retention',
        order: 1,
        sections: ['quick_capture'],
        fields: [
          { path: 'quick_capture.enabled', section: 'quick_capture', key: 'enabled' },
          { path: 'quick_capture.screen_memory_ttl', section: 'quick_capture', key: 'screen_memory_ttl' },
        ]
      },
      notification_memory: {
        id: 'notification_memory',
        titleKey: 'groups.notification_memory',
        title: 'Notification Memory Retention',
        order: 2,
        sections: ['voice_notifications'],
        fields: [
          { path: 'voice_notifications.retention_days', section: 'voice_notifications', key: 'retention_days' },
        ]
      },
      reset_conversation: {
        id: 'reset_conversation',
        titleKey: 'groups.reset_conversation',
        title: 'Reset Conversation & Memory',
        order: 3,
        // This is handled through special UI controls in Config Web
        customUI: true
      }
    }
  },

  storage_settings: {
    id: 'storage_settings',
    titleKey: 'groups.storage_settings',
    title: 'Storage Settings',
    order: 7,
    subsections: {
      storage_status: {
        id: 'storage_status',
        titleKey: 'groups.storage_status',
        title: 'Storage Status',
        order: 1,
        readOnly: true,
        customUI: true  // Rendered by dedicated storage status component
      },
      microsd_settings: {
        id: 'microsd_settings',
        titleKey: 'groups.microsd_settings',
        title: 'microSD Settings',
        order: 2,
        customUI: true  // Format/eject operations via special UI
      },
      data_sync: {
        id: 'data_sync',
        titleKey: 'groups.data_sync',
        title: 'Backup & Restore',
        order: 3,
        sections: ['storage'],
        customUI: true  // Backup/restore via special UI
      }
    }
  },

  advanced_settings: {
    id: 'advanced_settings',
    titleKey: 'groups.advanced_settings',
    title: 'Advanced Settings',
    order: 8,
    subsections: {
      logs: {
        id: 'logs',
        titleKey: 'groups.logs',
        title: 'Logs',
        order: 1,
        sections: ['log'],
        fields: [
          { path: 'log.level', section: 'log', key: 'level' },
          { path: 'log.llm_http_retention_days', section: 'log', key: 'llm_http_retention_days' },
          { path: 'model.log_raw_http', section: 'model', key: 'log_raw_http' },
        ]
      },
      manual_config: {
        id: 'manual_config',
        titleKey: 'groups.manual_config',
        title: 'Manual configuration',
        order: 2,
        customUI: true
      }
    }
  },

  about: {
    id: 'about',
    titleKey: 'groups.about',
    title: 'About',
    order: 9,
    customUI: true
  }
};

// Helper to flatten all field paths for quick lookup
export function getAllFieldPaths() {
  const paths = new Set();

  function addFields(item) {
    if (item.fields) {
      item.fields.forEach(f => paths.add(f.path));
    }
    if (item.subsections) {
      Object.values(item.subsections).forEach(addFields);
    }
  }

  Object.values(AGENT_SETTINGS_GROUPS).forEach(addFields);
  return paths;
}

// Helper to get all sections referenced in a group
export function getGroupSections(groupId) {
  const group = AGENT_SETTINGS_GROUPS[groupId];
  if (!group) return [];

  const sections = new Set();

  function collectSections(item) {
    if (item.sections) {
      item.sections.forEach(s => sections.add(s));
    }
    if (item.fields) {
      item.fields.forEach(f => {
        if (f.section) sections.add(f.section);
      });
    }
    if (item.subsections) {
      Object.values(item.subsections).forEach(collectSections);
    }
  }

  collectSections(group);
  return Array.from(sections);
}

// Map original sections to new groups for backward compatibility
export const SECTION_TO_GROUP_MAP = {
  'agent': ['basic_settings', 'conversation_settings', 'voice_settings'],
  'device': 'basic_settings.device_settings',
  'hid': 'basic_settings.device_settings',
  'model': 'model_settings',
  'model_providers': 'model_settings',
  'voice_model': 'voice_settings.realtime_mode',
  'voice_model_providers': 'voice_settings.realtime_mode',
  'stt': 'voice_settings.classic_mode.stt',
  'stt_providers': 'voice_settings.classic_mode.stt',
  'tts': 'voice_settings.classic_mode.tts',
  'tts_providers': 'voice_settings.classic_mode.tts',
  'audio': 'voice_settings.classic_mode.audio',
  'audio_archive': 'voice_settings.classic_mode.audio_archive',
  'search': 'conversation_settings.tool_settings.websearch',
  'termination_policy': 'conversation_settings.tool_settings.termination_policy',
  'quick_capture': 'memory_settings.screen_memory',
  'voice_notifications': 'memory_settings.notification_memory',
  'storage': 'storage_settings.data_sync',
  'storage.degraded_mode': 'storage_settings.data_sync',
  'storage.cleanup': 'storage_settings.data_sync',
  'log': 'advanced_settings.logs',
};
