package tray

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/kshitijdhara/burntout/assets"
)

func TestICO(t *testing.T) {
	b := ico(assets.TrayColor16, assets.TrayColor32)
	var head [3]uint16
	if err := binary.Read(bytes.NewReader(b), binary.LittleEndian, &head); err != nil || head != [3]uint16{0, 1, 2} {
		t.Fatalf("header %v %v", head, err)
	}
	for i, p := range [][]byte{assets.TrayColor16, assets.TrayColor32} {
		e := b[6+16*i : 6+16*(i+1)]
		size := binary.LittleEndian.Uint32(e[8:12])
		off := binary.LittleEndian.Uint32(e[12:16])
		if want := uint8(16 << i); e[0] != want || e[1] != want {
			t.Errorf("entry %d is %dx%d, want %d", i, e[0], e[1], want)
		}
		if !bytes.Equal(b[off:off+size], p) {
			t.Errorf("entry %d doesn't point at its PNG", i)
		}
	}
}
