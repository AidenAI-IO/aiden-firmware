#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
build_workflow="$repo_root/.github/workflows/build.yml"

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

def repo_branch_guarded?(condition, dynamic_runner)
  normalized = condition.gsub(/\s+/, " ")
  blocks_pr_events =
    normalized.include?("github.event_name != 'pull_request'") &&
    normalized.include?("github.event_name != 'pull_request_target'")
  requires_repo_branch =
    normalized.include?("startsWith(github.ref, 'refs/heads/')") ||
    normalized.include?('startsWith(github.ref, "refs/heads/")')
  runner_guard =
    !dynamic_runner ||
    normalized.include?("inputs.runner != 'self-hosted'") ||
    normalized.include?("inputs.runner == 'self-hosted'")

  blocks_pr_events && requires_repo_branch && runner_guard
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
    next if repo_branch_guarded?(condition, dynamic_runner)

    relative_file = file.delete_prefix("#{repo_root}/")
    failures << "#{relative_file}:#{job_name} can run on self-hosted without a job-level repo-branch guard"
  end
end

if failures.any?
  warn "Unsafe self-hosted GitHub Actions configuration:"
  failures.each { |failure| warn " - #{failure}" }
  exit 1
end

puts "GitHub Actions self-hosted repo-branch safety check passed."
RUBY

sanitize_line="$(grep -n 'Remove unusable pico-sdk submodule checkout' "$build_workflow" | sed 's/:.*//' | head -n 1 || true)"
checkout_line="$(grep -n 'uses: actions/checkout@v4' "$build_workflow" | sed 's/:.*//' | head -n 1 || true)"
if [[ -z "$sanitize_line" || -z "$checkout_line" || "$sanitize_line" -ge "$checkout_line" ]]; then
  echo "self-hosted build workflow must remove unusable pico-sdk submodule checkouts before actions/checkout" >&2
  exit 1
fi

if ! grep -Fq 'git -C "$sdk_dir" rev-parse --verify HEAD' "$build_workflow" || \
   ! grep -Fq 'sdk_git_dir="$GITHUB_WORKSPACE/.git/modules/pico-sdk"' "$build_workflow" || \
   ! grep -Fq 'rm -rf -- "$sdk_dir" "$sdk_git_dir"' "$build_workflow"; then
  echo "self-hosted build workflow must detect and remove pico-sdk checkouts whose current revision is unavailable" >&2
  exit 1
fi
