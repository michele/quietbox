package mail

import "testing"

func TestCapabilitiesHas(t *testing.T) {
	c := Capabilities{"IMAP4REV1", "OBJECTID"}
	if !c.Has("objectid") || !c.HasObjectID() {
		t.Error("expected OBJECTID")
	}
	if c.Has("MOVE") {
		t.Error("unexpected MOVE")
	}
}
