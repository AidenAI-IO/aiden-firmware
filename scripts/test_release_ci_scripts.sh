#!/bin/sh
set -eu
ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

ruby - "$ROOT_DIR" <<'RUBY'
require 'yaml'
root = ARGV.fetch(0)
load_workflow = ->(name) { YAML.load_file(File.join(root, '.github/workflows', name)) }
release = load_workflow.call('release.yml')
events = release['on'] || release[true]
raise 'channel releases must be manual only' unless events.keys == ['workflow_dispatch']
inputs = events['workflow_dispatch']['inputs']
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
