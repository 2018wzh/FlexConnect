package session

import (
	"errors"
	"net/http"
	"testing"
)

func TestConnSessionKeepsFirstCloseReason(t *testing.T) {
	sess := &Session{}
	c := sess.NewConnSession(&http.Header{})
	c.RecordClose("tls_read_error", "tls", errors.New("peer closed"))
	c.RecordClose("tun_write_error", "tun", errors.New("later failure"))
	info := c.CloseInfo()
	if info.Code != "tls_read_error" || info.Transport != "tls" || info.Error != "peer closed" {
		t.Fatalf("close info = %+v", info)
	}
}

func TestConnSessionClassifiesActiveClose(t *testing.T) {
	sess := &Session{}
	c := sess.NewConnSession(&http.Header{})
	sess.ActiveClose = true
	c.RecordClose("tls_read_error", "tls", errors.New("peer closed"))
	info := c.CloseInfo()
	if info.Code != "local_requested" || info.Transport != "local" || info.Error != "" {
		t.Fatalf("close info = %+v", info)
	}
}

func TestStaleSessionCloseDoesNotSignalReplacement(t *testing.T) {
	sess := &Session{}
	oldSession := sess.NewConnSession(&http.Header{})
	newSession := sess.NewConnSession(&http.Header{})

	oldSession.Close()

	if sess.CSess != newSession {
		t.Fatal("stale session cleared the replacement session")
	}
	select {
	case <-newSession.CloseChan:
		t.Fatal("stale session closed the replacement close channel")
	default:
	}
	select {
	case <-sess.CloseChan:
		t.Fatal("stale session closed the replacement session notification")
	default:
	}

	newSession.Close()
}

func TestEffectiveDPDFallsBackWhenServerDoesNotAdvertise(t *testing.T) {
	if got := EffectiveDPD(0); got != defaultDPDSeconds {
		t.Fatalf("EffectiveDPD(0) = %d, want %d", got, defaultDPDSeconds)
	}
	if got := EffectiveDPD(-5); got != defaultDPDSeconds {
		t.Fatalf("EffectiveDPD(-5) = %d, want %d", got, defaultDPDSeconds)
	}
	if got := EffectiveDPD(25); got != 25 {
		t.Fatalf("EffectiveDPD(25) = %d, want 25", got)
	}
}

func TestTunnelDoneDefaultsToNil(t *testing.T) {
	sess := &Session{}
	c := sess.NewConnSession(&http.Header{})
	if c.TunnelDone() != nil {
		t.Fatal("TunnelDone should be nil before a controller registers")
	}
	done := make(chan struct{})
	c.SetTunnelDone(done)
	if c.TunnelDone() == nil {
		t.Fatal("TunnelDone should expose the registered channel")
	}
}
