package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func TestWirelessWaitHonorsConfiguredTimeoutBelowItsPollInterval(t *testing.T) {
	f := newWirelessFixture(t)
	f.device.answer(statusWithoutStation, "ubus", "call", "network.wireless", "status")
	settings := f.settings
	settings.cfg.Checks.SwitchTimeoutSeconds = 1
	f.radio.settings = settings
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.radio.awaitLine(ctx, application.WirelessPlan{Radio: "radio1", SSID: "jxnu_stu"}) }()
	if err := f.clock.BlockUntilContext(ctx, 1); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(time.Second)
	select {
	case err := <-done:
		code, _ := domain.CodeOf(err)
		if code != domain.CodeDeadlineExceeded || f.clock.Waiters() != 0 {
			t.Fatalf("%v", err)
		}
	case <-ctx.Done():
		t.Fatal("configured one-second timeout was ignored")
	}
}
