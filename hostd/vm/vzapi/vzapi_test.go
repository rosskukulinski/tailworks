package vzapi

import (
	"testing"

	"github.com/rosskukulinski/tailworks/internal/version"
)

func TestCheckProto(t *testing.T) {
	if err := CheckProto(0); err != nil {
		t.Error(err)
	}
	if err := CheckProto(version.VZProtocol); err != nil {
		t.Error(err)
	}
	if err := CheckProto(version.VZProtocol + 1); err == nil {
		t.Error("a newer protocol was accepted")
	}
}
