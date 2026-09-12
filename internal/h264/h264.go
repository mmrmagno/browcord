package h264

import "fmt"

const (
	NALSlice = 1
	NALIDR   = 5
	NALSPS   = 7
	NALPPS   = 8
)

var StartCode = []byte{0, 0, 0, 1}

type AccessUnit struct {
	Data     []byte
	Keyframe bool
}

func SplitAnnexB(b []byte) [][]byte {
	var nals [][]byte
	start := -1

	for i := 0; i+2 < len(b); {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			if start >= 0 {
				nals = append(nals, trimTrailingZeros(b[start:i]))
			}
			i += 3
			start = i
			continue
		}
		i++
	}

	if start >= 0 && start < len(b) {
		nals = append(nals, trimTrailingZeros(b[start:]))
	}
	return nals
}

func Type(nal []byte) byte {
	if len(nal) == 0 {
		return 0
	}
	return nal[0] & 0x1F
}

func IsVCL(nal []byte) bool {
	t := Type(nal)
	return t == NALSlice || t == NALIDR
}

func StartsAccessUnit(nal []byte) bool {
	if len(nal) < 2 {
		return true
	}
	firstMB, ok := readUE(unescape(nal[1:], 5))
	if !ok {
		return true
	}
	return firstMB == 0
}

func GroupAccessUnits(nals [][]byte) []AccessUnit {
	var units []AccessUnit
	var current []byte
	var hasVCL, keyframe bool

	flush := func() {
		if len(current) > 0 {
			units = append(units, AccessUnit{Data: current, Keyframe: keyframe})
		}
		current = nil
		hasVCL = false
		keyframe = false
	}

	for _, nal := range nals {
		t := Type(nal)
		if t == NALSPS || t == NALPPS {
			continue
		}
		if IsVCL(nal) {
			if hasVCL && StartsAccessUnit(nal) {
				flush()
			}
			hasVCL = true
			if t == NALIDR {
				keyframe = true
			}
		}
		current = append(current, StartCode...)
		current = append(current, nal...)
	}
	flush()

	return units
}

func ParameterSets(nals [][]byte) []byte {
	var sps, pps []byte
	for _, nal := range nals {
		switch Type(nal) {
		case NALSPS:
			if sps == nil {
				sps = nal
			}
		case NALPPS:
			if pps == nil {
				pps = nal
			}
		}
	}
	if sps == nil || pps == nil {
		return nil
	}

	out := append([]byte{}, StartCode...)
	out = append(out, sps...)
	out = append(out, StartCode...)
	return append(out, pps...)
}

func CodecString(parameterSets []byte) string {
	for _, nal := range SplitAnnexB(parameterSets) {
		if Type(nal) == NALSPS && len(nal) >= 4 {
			return fmt.Sprintf("avc1.%02x%02x%02x", nal[1], nal[2], nal[3])
		}
	}
	return ""
}

func trimTrailingZeros(b []byte) []byte {
	for len(b) > 0 && b[len(b)-1] == 0 {
		b = b[:len(b)-1]
	}
	return b
}

func unescape(b []byte, limit int) []byte {
	out := make([]byte, 0, limit)
	zeros := 0
	for _, c := range b {
		if len(out) == limit {
			break
		}
		if zeros == 2 && c == 3 {
			zeros = 0
			continue
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, c)
	}
	return out
}

func readUE(b []byte) (uint32, bool) {
	pos := 0
	bitAt := func(i int) (byte, bool) {
		if i/8 >= len(b) {
			return 0, false
		}
		return (b[i/8] >> (7 - uint(i%8))) & 1, true
	}

	leadingZeros := 0
	for {
		bit, ok := bitAt(pos)
		if !ok {
			return 0, false
		}
		pos++
		if bit == 1 {
			break
		}
		leadingZeros++
		if leadingZeros > 31 {
			return 0, false
		}
	}

	value := uint32(0)
	for i := 0; i < leadingZeros; i++ {
		bit, ok := bitAt(pos)
		if !ok {
			return 0, false
		}
		pos++
		value = value<<1 | uint32(bit)
	}

	return (1 << uint(leadingZeros)) - 1 + value, true
}
