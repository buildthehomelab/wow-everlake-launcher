package casc

import (
	"encoding/binary"
	"math/bits"
)

// hashlittle2 is Bob Jenkins' lookup3 hashlittle2 (public domain), byte-wise: the same results as the aligned C
// version. It returns the primary and secondary hash. The local storage guards its index blocks and the header in
// front of every stored file with it.
func hashlittle2(data []byte, pc, pb uint32) (uint32, uint32) {
	length := len(data)
	a := 0xdeadbeef + uint32(length) + pc
	b, c := a, a+pb
	i := 0
	for length > 12 {
		a += binary.LittleEndian.Uint32(data[i:])
		b += binary.LittleEndian.Uint32(data[i+4:])
		c += binary.LittleEndian.Uint32(data[i+8:])
		a -= c
		a ^= bits.RotateLeft32(c, 4)
		c += b
		b -= a
		b ^= bits.RotateLeft32(a, 6)
		a += c
		c -= b
		c ^= bits.RotateLeft32(b, 8)
		b += a
		a -= c
		a ^= bits.RotateLeft32(c, 16)
		c += b
		b -= a
		b ^= bits.RotateLeft32(a, 19)
		a += c
		c -= b
		c ^= bits.RotateLeft32(b, 4)
		b += a
		length -= 12
		i += 12
	}
	if length == 0 {
		return c, b
	}
	var tail [12]byte
	copy(tail[:], data[i:])
	a += binary.LittleEndian.Uint32(tail[0:])
	b += binary.LittleEndian.Uint32(tail[4:])
	c += binary.LittleEndian.Uint32(tail[8:])
	c ^= b
	c -= bits.RotateLeft32(b, 14)
	a ^= c
	a -= bits.RotateLeft32(c, 11)
	b ^= a
	b -= bits.RotateLeft32(a, 25)
	c ^= b
	c -= bits.RotateLeft32(b, 16)
	a ^= c
	a -= bits.RotateLeft32(c, 4)
	b ^= a
	b -= bits.RotateLeft32(a, 14)
	c ^= b
	c -= bits.RotateLeft32(b, 24)
	return c, b
}

func hashlittle(data []byte, initval uint32) uint32 {
	h, _ := hashlittle2(data, initval, 0)
	return h
}
