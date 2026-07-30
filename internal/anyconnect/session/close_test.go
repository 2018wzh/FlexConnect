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
