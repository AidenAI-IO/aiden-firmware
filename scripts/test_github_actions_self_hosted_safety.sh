#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

ruby - "$repo_root" <<'RUBY'
require "yaml"

repo_root = ARGV.fetch(0)
workflow_dir = File.join(repo_root, ".github", "workflows")

def runs_on_values(value)
  case value
  when String
    [value]
  when Array
    value.flat_map { |entry| runs_on_values(entry) }
  else
    []
  end
end

def dynamic_self_hosted_capable?(runs_on)
  runs_on_values(runs_on).any? { |value| value.include?("inputs.runner") }
end

def self_hosted_capable?(runs_on)
  values = runs_on_values(runs_on)
  values.include?("self-hosted") || dynamic_self_hosted_capable?(runs_on)
end

def main_only_guarded?(condition, dynamic_runner)
  normalized = condition.gsub(/\s+/, " ")
  blocks_pr_events =
    normalized.include?("github.event_name != 'pull_request'") &&
    normalized.include?("github.event_name != 'pull_request_target'")
  requires_main = normalized.include?("github.ref == 'refs/heads/main'")
  runner_guard =
    !dynamic_runner ||
    normalized.include?("inputs.runner != 'self-hosted'") ||
    normalized.include?("inputs.runner == 'self-hosted'")

  blocks_pr_events && requires_main && runner_guard
end

failures = []

Dir.glob(File.join(workflow_dir, "*.{yml,yaml}")).sort.each do |file|
  workflow = YAML.load_file(file)
  jobs = workflow.fetch("jobs", {})
  jobs.each do |job_name, job|
    next unless job.is_a?(Hash)
    runs_on = job["runs-on"]
    next unless self_hosted_capable?(runs_on)

    dynamic_runner = dynamic_self_hosted_capable?(runs_on)
    condition = job["if"].to_s
    next if main_only_guarded?(condition, dynamic_runner)

    relative_file = file.delete_prefix("#{repo_root}/")
    failures << "#{relative_file}:#{job_name} can run on self-hosted without a job-level main-only guard"
  end
end

if failures.any?
  warn "Unsafe self-hosted GitHub Actions configuration:"
  failures.each { |failure| warn " - #{failure}" }
  exit 1
end

puts "GitHub Actions self-hosted main-only safety check passed."
RUBY
