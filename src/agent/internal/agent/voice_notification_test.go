package agent

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestVoiceNotificationManagerDeliversPersistentTailOncePerSeverity(t *testing.T) {
	now := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	manager := NewVoiceNotificationManager(
		VoiceNotificationsConfig{},
		WithVoiceNotificationClock(func() time.Time { return now }),
	)

	err := manager.Publish(context.Background(), VoiceNotificationEvent{
		Code:      "storage",
		Severity:  SeverityWarning,
		State:     VoiceNotificationActive,
		DedupeKey: "storage:device",
	})
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	prepared := manager.PrepareSpokenText(context.Background(), SpokenTextInput{
		ResponseText:   "已经完成。",
		TailAppendable: true,
	})
	if prepared.Mode != SpokenTextModeTail {
		t.Fatalf("PrepareSpokenText() mode = %q, want %q", prepared.Mode, SpokenTextModeTail)
	}
	if prepared.Text != "已经完成。另外提醒一下，设备存储空间不足。" {
		t.Fatalf("PrepareSpokenText() text = %q", prepared.Text)
	}
	if prepared.DeliveryToken == "" {
		t.Fatal("PrepareSpokenText() delivery token is empty")
	}

	manager.ReportDelivery(prepared.DeliveryToken, DeliveryCompleted)

	repeated := manager.PrepareSpokenText(context.Background(), SpokenTextInput{
		ResponseText:   "第二次正常回复。",
		TailAppendable: true,
	})
	if repeated.Mode != SpokenTextModeNormal || repeated.Text != "第二次正常回复。" {
		t.Fatalf("repeated PrepareSpokenText() = %#v, want unchanged normal response", repeated)
	}
	if repeated.DeliveryToken != "" {
		t.Fatalf("repeated delivery token = %q, want empty", repeated.DeliveryToken)
	}
}

func TestVoiceNotificationManagerResolvedEndsActiveCycle(t *testing.T) {
	manager := NewVoiceNotificationManager(VoiceNotificationsConfig{})
	ctx := context.Background()

	if err := manager.Publish(ctx, VoiceNotificationEvent{
		Code:      "storage",
		Severity:  SeverityWarning,
		State:     VoiceNotificationActive,
		DedupeKey: "storage:device",
	}); err != nil {
		t.Fatalf("Publish(active) error = %v", err)
	}
	first := manager.PrepareSpokenText(ctx, SpokenTextInput{ResponseText: "第一次。", TailAppendable: true})
	if first.DeliveryToken == "" {
		t.Fatal("first active cycle did not produce a delivery token")
	}
	if err := manager.Publish(ctx, VoiceNotificationEvent{
		Code:      "storage",
		State:     VoiceNotificationResolved,
		DedupeKey: "storage:device",
	}); err != nil {
		t.Fatalf("Publish(resolved) error = %v", err)
	}
	if err := manager.Publish(ctx, VoiceNotificationEvent{
		Code:      "storage",
		Severity:  SeverityWarning,
		State:     VoiceNotificationActive,
		DedupeKey: "storage:device",
	}); err != nil {
		t.Fatalf("Publish(second active) error = %v", err)
	}

	second := manager.PrepareSpokenText(ctx, SpokenTextInput{ResponseText: "第二次。", TailAppendable: true})
	if second.Mode != SpokenTextModeTail || second.DeliveryToken == "" || second.DeliveryToken == first.DeliveryToken {
		t.Fatalf("second active cycle PrepareSpokenText() = %#v", second)
	}

	manager.ReportDelivery(first.DeliveryToken, DeliveryCompleted)
	manager.ReportDelivery(second.DeliveryToken, DeliveryFailed)
	retry := manager.PrepareSpokenText(ctx, SpokenTextInput{ResponseText: "重试。", TailAppendable: true})
	if retry.Mode != SpokenTextModeTail {
		t.Fatalf("old-cycle delivery callback suppressed new cycle: %#v", retry)
	}
}

func TestVoiceNotificationManagerPublishValidatesEventIdentity(t *testing.T) {
	manager := NewVoiceNotificationManager(VoiceNotificationsConfig{})
	tests := []struct {
		name  string
		event VoiceNotificationEvent
	}{
		{name: "missing code", event: VoiceNotificationEvent{State: VoiceNotificationActive, DedupeKey: "storage:device", Severity: SeverityWarning}},
		{name: "unknown state", event: VoiceNotificationEvent{Code: "storage", State: "pending", DedupeKey: "storage:device", Severity: SeverityWarning}},
		{name: "missing scope", event: VoiceNotificationEvent{Code: "storage", State: VoiceNotificationActive, DedupeKey: "storage:", Severity: SeverityWarning}},
		{name: "code mismatch", event: VoiceNotificationEvent{Code: "storage", State: VoiceNotificationActive, DedupeKey: "network:device", Severity: SeverityWarning}},
		{name: "severity in key", event: VoiceNotificationEvent{Code: "storage", State: VoiceNotificationActive, DedupeKey: "storage:warning", Severity: SeverityWarning}},
		{name: "invalid severity", event: VoiceNotificationEvent{Code: "storage", State: VoiceNotificationActive, DedupeKey: "storage:device", Severity: NotificationSeverity(99)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := manager.Publish(context.Background(), tt.event); err == nil {
				t.Fatalf("Publish(%#v) error = nil", tt.event)
			}
		})
	}
}

func TestVoiceNotificationManagerKeepsSeverityUpgradePendingDuringPlayback(t *testing.T) {
	manager := NewVoiceNotificationManager(VoiceNotificationsConfig{})
	ctx := context.Background()

	if err := manager.Publish(ctx, VoiceNotificationEvent{Code: "storage", Severity: SeverityWarning, State: VoiceNotificationActive, DedupeKey: "storage:device"}); err != nil {
		t.Fatalf("Publish(warning) error = %v", err)
	}
	warning := manager.PrepareSpokenText(ctx, SpokenTextInput{ResponseText: "回复一。", TailAppendable: true})
	if warning.DeliveryToken == "" {
		t.Fatal("warning delivery token is empty")
	}

	if err := manager.Publish(ctx, VoiceNotificationEvent{Code: "storage", Severity: SeverityCritical, State: VoiceNotificationActive, DedupeKey: "storage:device"}); err != nil {
		t.Fatalf("Publish(critical) error = %v", err)
	}
	critical := manager.PrepareSpokenText(ctx, SpokenTextInput{ResponseText: "回复二。", TailAppendable: true})
	if critical.Mode != SpokenTextModeTail || critical.DeliveryToken == "" {
		t.Fatalf("critical PrepareSpokenText() = %#v", critical)
	}
	if critical.Text != "回复二。另外提醒一下，设备存储空间严重不足，请尽快清理。" {
		t.Fatalf("critical text = %q", critical.Text)
	}

	manager.ReportDelivery(warning.DeliveryToken, DeliveryCompleted)
	manager.ReportDelivery(critical.DeliveryToken, DeliveryCompleted)
	if err := manager.Publish(ctx, VoiceNotificationEvent{Code: "storage", Severity: SeverityWarning, State: VoiceNotificationActive, DedupeKey: "storage:device"}); err != nil {
		t.Fatalf("Publish(downgrade) error = %v", err)
	}
	downgraded := manager.PrepareSpokenText(ctx, SpokenTextInput{ResponseText: "回复三。", TailAppendable: true})
	if downgraded.Mode != SpokenTextModeNormal || downgraded.DeliveryToken != "" {
		t.Fatalf("severity downgrade repeated a delivered alert: %#v", downgraded)
	}
}

func TestVoiceNotificationManagerDoesNotDuplicateDowngradedAlertDuringPlayback(t *testing.T) {
	manager := NewVoiceNotificationManager(VoiceNotificationsConfig{})
	ctx := context.Background()

	if err := manager.Publish(ctx, VoiceNotificationEvent{Code: "storage", Severity: SeverityCritical, State: VoiceNotificationActive, DedupeKey: "storage:device"}); err != nil {
		t.Fatalf("Publish(critical) error = %v", err)
	}
	critical := manager.PrepareSpokenText(ctx, SpokenTextInput{ResponseText: "回复一。", TailAppendable: true})
	if critical.DeliveryToken == "" {
		t.Fatal("critical delivery token is empty")
	}

	if err := manager.Publish(ctx, VoiceNotificationEvent{Code: "storage", Severity: SeverityWarning, State: VoiceNotificationActive, DedupeKey: "storage:device"}); err != nil {
		t.Fatalf("Publish(warning downgrade) error = %v", err)
	}
	duringPlayback := manager.PrepareSpokenText(ctx, SpokenTextInput{ResponseText: "回复二。", TailAppendable: true})
	if duringPlayback.Mode != SpokenTextModeNormal || duringPlayback.DeliveryToken != "" {
		t.Fatalf("downgraded alert was delivered while a higher severity was in flight: %#v", duringPlayback)
	}

	manager.ReportDelivery(critical.DeliveryToken, DeliveryFailed)
	retry := manager.PrepareSpokenText(ctx, SpokenTextInput{ResponseText: "回复三。", TailAppendable: true})
	if retry.Mode != SpokenTextModeTail || retry.DeliveryToken == "" {
		t.Fatalf("failed higher-severity delivery did not release current severity for retry: %#v", retry)
	}
}

func TestVoiceNotificationManagerRetriesFailedOrCanceledDeliveryButNotHeartbeat(t *testing.T) {
	manager := NewVoiceNotificationManager(VoiceNotificationsConfig{})
	ctx := context.Background()
	event := VoiceNotificationEvent{Code: "storage", Severity: SeverityWarning, State: VoiceNotificationActive, DedupeKey: "storage:device"}
	if err := manager.Publish(ctx, event); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	failed := manager.PrepareSpokenText(ctx, SpokenTextInput{ResponseText: "一。", TailAppendable: true})
	manager.ReportDelivery(failed.DeliveryToken, DeliveryFailed)
	retryAfterFailure := manager.PrepareSpokenText(ctx, SpokenTextInput{ResponseText: "二。", TailAppendable: true})
	if retryAfterFailure.Mode != SpokenTextModeTail {
		t.Fatalf("failed delivery was not retried: %#v", retryAfterFailure)
	}

	manager.ReportDelivery(retryAfterFailure.DeliveryToken, DeliveryCanceled)
	retryAfterCancel := manager.PrepareSpokenText(ctx, SpokenTextInput{ResponseText: "三。", TailAppendable: true})
	if retryAfterCancel.Mode != SpokenTextModeTail {
		t.Fatalf("canceled delivery was not retried: %#v", retryAfterCancel)
	}
	manager.ReportDelivery(retryAfterCancel.DeliveryToken, DeliveryCompleted)

	if err := manager.Publish(ctx, event); err != nil {
		t.Fatalf("Publish(heartbeat) error = %v", err)
	}
	heartbeat := manager.PrepareSpokenText(ctx, SpokenTextInput{ResponseText: "四。", TailAppendable: true})
	if heartbeat.Mode != SpokenTextModeNormal || heartbeat.DeliveryToken != "" {
		t.Fatalf("same-severity heartbeat repeated delivery: %#v", heartbeat)
	}
}

func TestVoiceNotificationManagerTurnFailureReplacesResponseWithoutConsumingPending(t *testing.T) {
	manager := NewVoiceNotificationManager(VoiceNotificationsConfig{})
	ctx := context.Background()
	if err := manager.Publish(ctx, VoiceNotificationEvent{Code: "storage", Severity: SeverityWarning, State: VoiceNotificationActive, DedupeKey: "storage:device"}); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	replacement := manager.PrepareSpokenText(ctx, SpokenTextInput{
		ResponseText: "不应播放的模型文本",
		TurnFailure:  &TurnFailure{Code: TurnFailureNetworkUnavailable},
	})
	if replacement.Mode != SpokenTextModeReplacement {
		t.Fatalf("replacement mode = %q", replacement.Mode)
	}
	if replacement.Text != "当前网络不可用，暂时无法完成这个请求。" {
		t.Fatalf("replacement text = %q", replacement.Text)
	}
	if replacement.DeliveryToken != "" {
		t.Fatalf("replacement delivery token = %q, want empty", replacement.DeliveryToken)
	}

	next := manager.PrepareSpokenText(ctx, SpokenTextInput{ResponseText: "下一条正常回复。", TailAppendable: true})
	if next.Mode != SpokenTextModeTail {
		t.Fatalf("TurnFailure consumed persistent pending: %#v", next)
	}
}

func TestVoiceNotificationManagerLeaseExpiresAndHeartbeatRenewsIt(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	manager := NewVoiceNotificationManager(
		VoiceNotificationsConfig{
			Expiration: VoiceNotificationExpirationConfig{DefaultTTLSeconds: 10},
		},
		WithVoiceNotificationClock(func() time.Time { return now }),
	)
	ctx := context.Background()
	event := VoiceNotificationEvent{Code: "storage", Severity: SeverityWarning, State: VoiceNotificationActive, DedupeKey: "storage:device"}
	if err := manager.Publish(ctx, event); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	now = now.Add(9 * time.Second)
	if err := manager.Publish(ctx, event); err != nil {
		t.Fatalf("Publish(heartbeat) error = %v", err)
	}
	now = now.Add(9 * time.Second)
	renewed := manager.PrepareSpokenText(ctx, SpokenTextInput{ResponseText: "租约仍有效。", TailAppendable: true})
	if renewed.Mode != SpokenTextModeTail {
		t.Fatalf("heartbeat did not renew lease: %#v", renewed)
	}
	manager.ReportDelivery(renewed.DeliveryToken, DeliveryFailed)

	now = now.Add(11 * time.Second)
	expired := manager.PrepareSpokenText(ctx, SpokenTextInput{ResponseText: "租约已过期。", TailAppendable: true})
	if expired.Mode != SpokenTextModeNormal || expired.DeliveryToken != "" {
		t.Fatalf("expired notification was delivered: %#v", expired)
	}

	if err := manager.Publish(ctx, event); err != nil {
		t.Fatalf("Publish(new cycle) error = %v", err)
	}
	reactivated := manager.PrepareSpokenText(ctx, SpokenTextInput{ResponseText: "新周期。", TailAppendable: true})
	if reactivated.Mode != SpokenTextModeTail {
		t.Fatalf("active after lease expiry did not start a new cycle: %#v", reactivated)
	}
}

func TestVoiceNotificationManagerSelectsAtMostOneHighestSeverityPending(t *testing.T) {
	manager := NewVoiceNotificationManager(VoiceNotificationsConfig{})
	ctx := context.Background()
	for _, event := range []VoiceNotificationEvent{
		{Code: "storage", Severity: SeverityWarning, State: VoiceNotificationActive, DedupeKey: "storage:secondary"},
		{Code: "storage", Severity: SeverityCritical, State: VoiceNotificationActive, DedupeKey: "storage:device"},
	} {
		if err := manager.Publish(ctx, event); err != nil {
			t.Fatalf("Publish(%q) error = %v", event.DedupeKey, err)
		}
	}

	first := manager.PrepareSpokenText(ctx, SpokenTextInput{ResponseText: "正常回复。", TailAppendable: true})
	if first.Text != "正常回复。另外提醒一下，设备存储空间严重不足，请尽快清理。" {
		t.Fatalf("first selected text = %q", first.Text)
	}
	if first.DeliveryToken == "" {
		t.Fatal("first delivery token is empty")
	}
	manager.ReportDelivery(first.DeliveryToken, DeliveryCompleted)

	second := manager.PrepareSpokenText(ctx, SpokenTextInput{ResponseText: "另一条回复。", TailAppendable: true})
	if second.Text != "另一条回复。另外提醒一下，设备存储空间不足。" {
		t.Fatalf("second selected text = %q", second.Text)
	}
}

func TestVoiceNotificationManagerKeepsPendingWhenTailCannotBeAppended(t *testing.T) {
	disabled := false
	disabledManager := NewVoiceNotificationManager(VoiceNotificationsConfig{
		ResponseTail: VoiceNotificationResponseTailConfig{Enabled: &disabled},
	})
	ctx := context.Background()
	event := VoiceNotificationEvent{Code: "storage", Severity: SeverityWarning, State: VoiceNotificationActive, DedupeKey: "storage:device"}
	if err := disabledManager.Publish(ctx, event); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	disabledTail := disabledManager.PrepareSpokenText(ctx, SpokenTextInput{ResponseText: "正常回复。", TailAppendable: true})
	if disabledTail.Mode != SpokenTextModeNormal || disabledTail.DeliveryToken != "" {
		t.Fatalf("disabled response tail changed reply: %#v", disabledTail)
	}

	manager := NewVoiceNotificationManager(VoiceNotificationsConfig{})
	if err := manager.Publish(ctx, event); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	notAppendable := manager.PrepareSpokenText(ctx, SpokenTextInput{ResponseText: "流式回复。", TailAppendable: false})
	if notAppendable.Mode != SpokenTextModeNormal || notAppendable.DeliveryToken != "" {
		t.Fatalf("non-appendable response consumed pending: %#v", notAppendable)
	}

	appendable := manager.PrepareSpokenText(ctx, SpokenTextInput{ResponseText: "下一条回复。", TailAppendable: true})
	if appendable.Mode != SpokenTextModeTail {
		t.Fatalf("pending was not preserved for next appendable response: %#v", appendable)
	}
}

func TestVoiceNotificationManagerCapsActiveRecords(t *testing.T) {
	manager := NewVoiceNotificationManager(VoiceNotificationsConfig{MaxPending: 1})
	ctx := context.Background()
	first := VoiceNotificationEvent{Code: "storage", Severity: SeverityWarning, State: VoiceNotificationActive, DedupeKey: "storage:device"}
	second := VoiceNotificationEvent{Code: "storage", Severity: SeverityCritical, State: VoiceNotificationActive, DedupeKey: "storage:secondary"}
	if err := manager.Publish(ctx, first); err != nil {
		t.Fatalf("Publish(first) error = %v", err)
	}
	if err := manager.Publish(ctx, second); err == nil {
		t.Fatal("Publish(second) error = nil, want capacity error")
	}
	first.State = VoiceNotificationResolved
	if err := manager.Publish(ctx, first); err != nil {
		t.Fatalf("Publish(resolved) error = %v", err)
	}
	if err := manager.Publish(ctx, second); err != nil {
		t.Fatalf("Publish(second after resolved) error = %v", err)
	}
}

func TestVoiceNotificationManagerUsesConfiguredLocale(t *testing.T) {
	manager := NewVoiceNotificationManager(VoiceNotificationsConfig{
		DefaultLocale: "en-US",
		ResponseTail:  VoiceNotificationResponseTailConfig{MaxTextChars: 100},
	})
	ctx := context.Background()
	if err := manager.Publish(ctx, VoiceNotificationEvent{Code: "storage", Severity: SeverityWarning, State: VoiceNotificationActive, DedupeKey: "storage:device"}); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	tail := manager.PrepareSpokenText(ctx, SpokenTextInput{ResponseText: "Done.", TailAppendable: true})
	if tail.Text != "Done. Also, your device is running low on storage." {
		t.Fatalf("localized tail = %q", tail.Text)
	}

	replacement := manager.PrepareSpokenText(ctx, SpokenTextInput{TurnFailure: &TurnFailure{Code: TurnFailureTokenInsufficient}})
	if replacement.Text != "The service quota is exhausted, so I cannot complete this request right now." {
		t.Fatalf("localized replacement = %q", replacement.Text)
	}
}

func TestTurnFailureFromErrorClassifiesFinalLLMFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "canceled", err: context.Canceled, want: ""},
		{name: "dns", err: &net.DNSError{Err: "no such host", Name: "api.example.com"}, want: TurnFailureNetworkUnavailable},
		{name: "timeout", err: context.DeadlineExceeded, want: TurnFailureNetworkUnavailable},
		{name: "quota", err: errors.New("provider returned 402: insufficient balance"), want: TurnFailureTokenInsufficient},
		{name: "openai quota code", err: errors.New(`API error 429: {"error":{"code":"insufficient_quota"}}`), want: TurnFailureTokenInsufficient},
		{name: "openai quota message", err: errors.New("You exceeded your current quota, please check your plan and billing details."), want: TurnFailureTokenInsufficient},
		{name: "rate limited", err: errors.New("API error 429: rate limit exceeded"), want: TurnFailureNetworkUnavailable},
		{name: "generic", err: errors.New("provider returned 500"), want: TurnFailureLLMUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			failure := TurnFailureFromError(tt.err)
			if tt.want == "" {
				if failure != nil {
					t.Fatalf("TurnFailureFromError() = %#v, want nil", failure)
				}
				return
			}
			if failure == nil || failure.Code != tt.want {
				t.Fatalf("TurnFailureFromError() = %#v, want code %q", failure, tt.want)
			}
		})
	}
}

func TestVoiceNotificationManagerDoesNotRankRecentDowngradeAsUpgrade(t *testing.T) {
	now := time.Unix(1000, 0)
	manager := NewVoiceNotificationManager(DefaultConfig().VoiceNotifications, WithVoiceNotificationClock(func() time.Time { return now }))
	if err := manager.RegisterPersistentText("storage", SeverityWarning, "zh-CN", "storage warning"); err != nil {
		t.Fatalf("RegisterPersistentText(storage) error = %v", err)
	}
	if err := manager.RegisterPersistentText("battery", SeverityWarning, "zh-CN", "battery warning"); err != nil {
		t.Fatalf("RegisterPersistentText(battery) error = %v", err)
	}
	ctx := context.Background()
	if err := manager.Publish(ctx, VoiceNotificationEvent{
		Code: "storage", Severity: SeverityCritical, State: VoiceNotificationActive, DedupeKey: "storage:device",
	}); err != nil {
		t.Fatalf("Publish(storage critical) error = %v", err)
	}
	now = now.Add(time.Minute)
	if err := manager.Publish(ctx, VoiceNotificationEvent{
		Code: "battery", Severity: SeverityWarning, State: VoiceNotificationActive, DedupeKey: "battery:device",
	}); err != nil {
		t.Fatalf("Publish(battery warning) error = %v", err)
	}
	now = now.Add(time.Minute)
	if err := manager.Publish(ctx, VoiceNotificationEvent{
		Code: "storage", Severity: SeverityWarning, State: VoiceNotificationActive, DedupeKey: "storage:device",
	}); err != nil {
		t.Fatalf("Publish(storage downgrade) error = %v", err)
	}

	prepared := manager.PrepareSpokenText(ctx, SpokenTextInput{ResponseText: "reply", TailAppendable: true})
	if !strings.Contains(prepared.Text, "battery warning") {
		t.Fatalf("recent downgrade outranked actual pending condition: %q", prepared.Text)
	}
}

func TestVoiceNotificationsConfigDefaultsAndValidation(t *testing.T) {
	defaults := DefaultConfig().VoiceNotifications
	if !defaults.EnabledOrDefault() || defaults.DefaultLocaleOrDefault() != "zh-CN" || defaults.MaxPendingOrDefault() != 8 {
		t.Fatalf("voice notification defaults = %#v", defaults)
	}
	if !defaults.ResponseTail.EnabledOrDefault() || defaults.ResponseTail.MaxItems != 1 || defaults.ResponseTail.MaxTextCharsOrDefault() != 40 {
		t.Fatalf("response tail defaults = %#v", defaults.ResponseTail)
	}
	if defaults.Expiration.CodeTTLSeconds["storage"] != 900 {
		t.Fatalf("storage TTL = %d, want 900", defaults.Expiration.CodeTTLSeconds["storage"])
	}

	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "negative max pending", mutate: func(cfg *Config) { cfg.VoiceNotifications.MaxPending = -1 }},
		{name: "multiple tail items", mutate: func(cfg *Config) { cfg.VoiceNotifications.ResponseTail.MaxItems = 2 }},
		{name: "negative tail length", mutate: func(cfg *Config) { cfg.VoiceNotifications.ResponseTail.MaxTextChars = -1 }},
		{name: "negative default ttl", mutate: func(cfg *Config) { cfg.VoiceNotifications.Expiration.DefaultTTLSeconds = -1 }},
		{name: "negative code ttl", mutate: func(cfg *Config) { cfg.VoiceNotifications.Expiration.CodeTTLSeconds["storage"] = -1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Config.Validate() error = nil")
			}
		})
	}
}

func TestRuntimeSharesVoiceNotificationManagerWithScenarioProducers(t *testing.T) {
	runtime := NewRuntimeWithDeps(DefaultConfig(), nil, nil, nil, NewSkillIndex())
	manager := runtime.VoiceNotifications()
	if manager == nil {
		t.Fatal("Runtime.VoiceNotifications() = nil")
	}
	sink := runtime.VoiceNotificationSink()
	if sink == nil {
		t.Fatal("Runtime.VoiceNotificationSink() = nil")
	}
	if err := sink.Publish(context.Background(), VoiceNotificationEvent{Code: "storage", Severity: SeverityWarning, State: VoiceNotificationActive, DedupeKey: "storage:device"}); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	prepared := manager.PrepareSpokenText(context.Background(), SpokenTextInput{ResponseText: "正常回复。", TailAppendable: true})
	if prepared.Mode != SpokenTextModeTail {
		t.Fatalf("shared manager did not receive scenario event: %#v", prepared)
	}
}

func TestVoiceNotificationManagerPrefersTaskRelatedPolicy(t *testing.T) {
	manager := NewVoiceNotificationManager(VoiceNotificationsConfig{})
	if err := manager.RegisterPersistentText("battery", SeverityWarning, "zh-CN", "另外提醒一下，设备电量较低。"); err != nil {
		t.Fatalf("RegisterPersistentText() error = %v", err)
	}
	ctx := context.Background()
	for _, event := range []VoiceNotificationEvent{
		{Code: "storage", Severity: SeverityCritical, State: VoiceNotificationActive, DedupeKey: "storage:device"},
		{Code: "battery", Severity: SeverityWarning, State: VoiceNotificationActive, DedupeKey: "battery:device"},
	} {
		if err := manager.Publish(ctx, event); err != nil {
			t.Fatalf("Publish(%q) error = %v", event.Code, err)
		}
	}

	prepared := manager.PrepareSpokenText(ctx, SpokenTextInput{
		ResponseText:   "电池检查完成。",
		TailAppendable: true,
		RelatedCodes:   []string{"battery"},
	})
	if prepared.Text != "电池检查完成。另外提醒一下，设备电量较低。" {
		t.Fatalf("task-related selection = %q", prepared.Text)
	}
}
