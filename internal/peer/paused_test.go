package peer

import "testing"

func TestStoppedDeviceEditsPersist(t *testing.T) {
	e, box := testEngine(t)
	if _, err := e.BeginPairing(); err != nil {
		t.Fatal(err)
	}
	p, _ := testPairRequest(t, e)
	if err := e.DecidePair(p.sid, true); err != nil {
		t.Fatal(err)
	}
	id := e.Devices()[0].ID
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	box.fail = true
	if err := e.RenameDevice(id, "Renamed"); err == nil || e.Devices()[0].Name == "Renamed" {
		t.Fatal("failed stopped rename changed identity")
	}
	box.fail = false
	if err := e.RenameDevice(id, "Renamed"); err != nil {
		t.Fatal(err)
	}
	reload := func() *Engine {
		next, err := newEngine(e.cfg, box)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = next.Close() })
		return next
	}
	if devices := reload().Devices(); len(devices) != 1 || devices[0].Name != "Renamed" {
		t.Fatal("stopped rename did not survive reload", devices)
	}
	box.fail = true
	if err := e.RevokeDevice(id); err == nil || len(e.Devices()) != 1 {
		t.Fatal("failed stopped revocation changed identity")
	}
	box.fail = false
	if err := e.RevokeDevice(id); err != nil {
		t.Fatal(err)
	}
	if len(reload().Devices()) != 0 {
		t.Fatal("stopped revocation did not survive reload")
	}
	if e.RenameDevice(id, "Unknown") == nil || e.RevokeDevice(id) == nil {
		t.Fatal("stopped engine accepted an unknown device")
	}
}
