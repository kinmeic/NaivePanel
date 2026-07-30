package sysd

import (
	"strings"
	"testing"
)

func TestLimitedBufferCapsOutputWithoutShortWrite(t *testing.T) {
	buffer := newLimitedBuffer(5)
	written, err := buffer.Write([]byte("123456789"))
	if err != nil || written != 9 {
		t.Fatalf("written=%d err=%v", written, err)
	}
	if text := buffer.String(); !strings.HasPrefix(text, "12345") || !strings.Contains(text, "已截断") {
		t.Fatalf("unexpected limited output: %q", text)
	}
}
