package h264

import (
	"bytes"
	"os"
	"testing"
)

func nal(header byte, rest ...byte) []byte {
	return append([]byte{header}, rest...)
}

func annexB(nals ...[]byte) []byte {
	var out []byte
	for _, n := range nals {
		out = append(out, StartCode...)
		out = append(out, n...)
	}
	return out
}

func TestSplitAnnexBHandlesBothStartCodes(t *testing.T) {
	stream := []byte{0, 0, 0, 1, 0x67, 0xAA, 0, 0, 1, 0x68, 0xBB, 0xCC}

	nals := SplitAnnexB(stream)
	if len(nals) != 2 {
		t.Fatalf("got %d NALs, want 2", len(nals))
	}
	if !bytes.Equal(nals[0], []byte{0x67, 0xAA}) {
		t.Errorf("first NAL = %x, want 67aa", nals[0])
	}
	if !bytes.Equal(nals[1], []byte{0x68, 0xBB, 0xCC}) {
		t.Errorf("second NAL = %x, want 68bbcc", nals[1])
	}
}

func TestGroupAccessUnitsKeepsSlicesOfOneFrameTogether(t *testing.T) {
	firstSlice := nal(0x65, 0x88, 0x84)
	secondSlice := nal(0x65, 0x40, 0x12)

	if !StartsAccessUnit(firstSlice) {
		t.Fatal("fixture: first slice should report first_mb_in_slice == 0")
	}
	if StartsAccessUnit(secondSlice) {
		t.Fatal("fixture: second slice should report a non-zero first_mb_in_slice")
	}

	units := GroupAccessUnits([][]byte{firstSlice, secondSlice})
	if len(units) != 1 {
		t.Fatalf("got %d access units, want 1: multi-slice frames must not be split", len(units))
	}
	if !units[0].Keyframe {
		t.Error("IDR slices should mark the access unit as a keyframe")
	}
}

func TestGroupAccessUnitsSplitsOnNewFrame(t *testing.T) {
	frame := func(header byte) []byte { return nal(header, 0x88, 0x84) }

	units := GroupAccessUnits([][]byte{
		nal(NALSPS | 0x60), nal(NALPPS | 0x60),
		frame(0x65),
		frame(0x41),
		frame(0x41),
	})

	if len(units) != 3 {
		t.Fatalf("got %d access units, want 3", len(units))
	}
	if !units[0].Keyframe || units[1].Keyframe || units[2].Keyframe {
		t.Errorf("keyframe flags = %v/%v/%v, want true/false/false", units[0].Keyframe, units[1].Keyframe, units[2].Keyframe)
	}
	for i, u := range units {
		if !bytes.HasPrefix(u.Data, StartCode) {
			t.Errorf("unit %d does not begin with a start code", i)
		}
	}
}

func TestParameterSetsAndCodecString(t *testing.T) {
	sps := nal(0x67, 0x42, 0xE0, 0x28, 0xFF)
	pps := nal(0x68, 0xCE)

	sets := ParameterSets([][]byte{nal(0x41, 1), sps, pps, nal(0x41, 2)})
	want := annexB(sps, pps)
	if !bytes.Equal(sets, want) {
		t.Errorf("ParameterSets = %x, want %x", sets, want)
	}

	if got := CodecString(sets); got != "avc1.42e028" {
		t.Errorf("CodecString = %q, want avc1.42e028", got)
	}
}

func TestParameterSetsRequiresBoth(t *testing.T) {
	if got := ParameterSets([][]byte{nal(0x67, 0x42)}); got != nil {
		t.Errorf("ParameterSets with no PPS = %x, want nil", got)
	}
}

func TestAgainstEncodedFixture(t *testing.T) {
	raw, err := os.ReadFile("../../assets/testpattern.h264")
	if err != nil {
		t.Skip("assets/testpattern.h264 not generated; run ./assets/gen.sh")
	}

	nals := SplitAnnexB(raw)
	units := GroupAccessUnits(nals)

	if len(units) != 300 {
		t.Errorf("got %d access units, want 300 (10s at 30fps)", len(units))
	}

	keyframes := 0
	for _, u := range units {
		if u.Keyframe {
			keyframes++
		}
	}
	if keyframes != 5 {
		t.Errorf("got %d keyframes, want 5 (keyint 60 over 300 frames)", keyframes)
	}

	if got := CodecString(ParameterSets(nals)); got != "avc1.42c028" && got != "avc1.42e028" {
		t.Errorf("CodecString = %q, want a baseline 4.0 profile string", got)
	}
}
