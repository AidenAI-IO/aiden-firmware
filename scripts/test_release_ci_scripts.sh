#!/bin/sh
set -eu
ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

ruby - "$ROOT_DIR" <<'RUBY'
require 'yaml'
root = ARGV.fetch(0)
load_workflow = ->(name) { YAML.load_file(File.join(root, '.github/workflows', name)) }
release = load_workflow.call('release.yml')
events = release['on'] || release[true]
raise 'channel releases must have only manual/reusable entrypoints' unless events.keys.sort == %w[workflow_call workflow_dispatch]
inputs = events['workflow_dispatch']['inputs']
call = events['workflow_call']
raise 'reusable release inputs must match manual inputs' unless call['inputs'].keys.sort == inputs.keys.sort
inputs.each do |name, input|
  expected_type = input['type'] == 'choice' ? 'string' : input['type']
  raise "reusable #{name} input type differs" unless call['inputs'][name]['type'] == expected_type
  raise "reusable #{name} default differs" unless call['inputs'][name]['default'] == input['default']
end
raise 'reusable channel must be explicit' unless call['inputs']['channel']['required']
%w[OTA_ED25519_PRIVATE_KEY AGENT_CONFIG_TOML].each do |secret|
  raise "#{secret} must be optional for preview/business builds" unless call['secrets'][secret]['required'] == false
end
raise 'three channel choices required' unless inputs['channel']['options'] == %w[dev staging prod]
raise 'preview must not publish by default' unless inputs['plan_only']['default'] && !inputs['publish']['default']
raise 'all channels must share one lock' unless release['concurrency'] == {
  'group' => 'aiden-channel-release', 'cancel-in-progress' => false
}
jobs = release['jobs']
raise 'plan must be read-only' unless release['permissions']['contents'] == 'read'
raise 'publication needs channel environment' unless jobs['publish']['environment'] == '${{ inputs.channel }}'
raise 'only publication needs write' unless jobs['publish']['permissions']['contents'] == 'write'
raise 'business job must depend on classification' unless jobs['business']['if'].include?("kind == 'business'")
raise 'OTA job must depend on classification' unless jobs['ota']['if'].include?("kind == 'ota'")
raise 'OTA must reuse the system build' unless jobs['ota']['uses'] == './.github/workflows/build.yml'

backup = load_workflow.call('build-backup.yml')
backup_events = backup['on'] || backup[true]
raise 'backup releases must be manual only' unless backup_events.keys == ['workflow_dispatch']
backup_inputs = backup_events['workflow_dispatch']['inputs']
raise 'backup must expose three channels' unless backup_inputs['channel']['options'] == inputs['channel']['options']
raise 'backup must default to preview' unless backup_inputs['plan_only']['default'] && !backup_inputs['publish']['default'] && !backup_inputs['dry_run']['default']
backup_jobs = backup['jobs']
raise 'backup must not have a separate publisher' unless backup_jobs.keys.sort == %w[preflight release]
backup_release = backup_jobs['release']
raise 'backup must reuse channel decisions and publication' unless backup_release['uses'] == './.github/workflows/release.yml'
raise 'backup must use runner 02' unless backup_release['with']['runner'] == 'aiden-hosted-02'
%w[channel source_ref version force_ota plan_only publish].each do |name|
  raise "backup does not forward #{name}" unless backup_release['with'][name] == "${{ inputs.#{name} }}"
end
raise 'backup must pass publication permission to callee' unless backup_release['permissions']['contents'] == 'write'
raise 'backup must forward secrets' unless backup_release['secrets'] == 'inherit'
raise 'preflight must not enter release flow' unless backup_release['if'].include?('!inputs.dry_run')
preflight = backup_jobs['preflight']
raise 'backup preflight must stay artifact-only' unless preflight['uses'] == './.github/workflows/build.yml' && preflight['with']['dry_run'] == true
raise 'preflight must use backup runner and requested source' unless preflight['with']['runner'] == 'aiden-hosted-02' && preflight['with']['source_ref'] == '${{ inputs.source_ref }}'
raise 'preflight must be explicitly enabled' unless preflight['if'].include?('inputs.dry_run &&')
raise 'caller must not acquire the callee release lock' if backup.dig('concurrency', 'group') == release.dig('concurrency', 'group')

%w[build.yml build-scheduled.yml build-backup.yml build-fallback.yml debian-package.yml].each do |name|
  workflow = load_workflow.call(name)
  triggers = workflow['on'] || workflow[true]
  raise "#{name} still schedules publication" if triggers.key?('schedule')
  raise "#{name} still has release write permissions" unless workflow['permissions']['contents'] == 'read'
  raise "#{name} still has a publisher" if workflow['jobs'].key?('publish')
end
build = File.read(File.join(root, '.github/workflows/build.yml'))
raise 'legacy release bypass remains' if build.include?('scripts/create_github_release.sh')
raise 'firmware build entrypoint missing' unless build.include?('run: ./debian_build.sh')
raise 'channel build entrypoint missing' unless build.include?('scripts/release/release.py build --plan')
raise 'signing key must also be rootfs trust anchor' unless build.include?('OTA_TRUST_PUBLIC_KEY_PATH=$public_key')
raise 'SDK checkout must use planned source' unless build.include?('inputs.source_ref || github.sha')
raise 'Go caches must remain cleanable' unless build.include?('chmod -R u+w "$go_mod_cache"')
ci = File.read(File.join(root, '.github/workflows/ci.yml'))
raise 'CI must cover channel decisions' unless ci.include?('python3 scripts/test_channel_release.py')
RUBY

echo 'release CI script tests passed'
