package srun

// XXTEA (Corrected Block TEA), as the SRun front-end obfuscates it.
//
// The two constants below reach the JavaScript as OR expressions
// (0x86014019 | 0x183639A0 and 0x8CE0D9BF | 0x731F2640) so they do not appear
// as recognisable literals. They are the golden-ratio delta and a 32-bit mask.
const (
	delta = 0x9e3779b9
	// The mask is what uint32 arithmetic does here for free. It is named so the
	// correspondence with the JavaScript stays visible.
	_ = 0xffffffff
)

// Xencode encrypts msg under key.
//
// Two details decide whether the output is right, and both are easy to lose in
// translation:
//
//   - Words are packed little-endian, and the message's byte length is appended
//     as one extra word. The key is packed the same way but without that word,
//     then zero-padded to four words because the round function indexes it with
//     two bits.
//   - Python computes the round function in arbitrary precision and masks only
//     when storing. Doing it in uint32 throughout gives the same answer: XOR is
//     bitwise and addition only carries upward, so bits above 32 can never
//     change the bits below. Using Go's int here instead would make the result
//     depend on whether the target is 32- or 64-bit.
func Xencode(msg, key []byte) []byte {
	if len(msg) == 0 {
		return nil
	}

	block := packWords(msg, true)
	keyBlock := packWords(key, false)
	for len(keyBlock) < 4 {
		keyBlock = append(keyBlock, 0)
	}

	last := len(block) - 1
	z := block[last]
	var sum uint32

	for round := 6 + 52/(last+1); round > 0; round-- {
		sum += delta
		e := sum >> 2 & 3

		position := 0
		for ; position < last; position++ {
			y := block[position+1]
			block[position] += mix(y, z, sum, keyBlock[position&3^int(e)])
			z = block[position]
		}
		y := block[0]
		block[last] += mix(y, z, sum, keyBlock[position&3^int(e)])
		z = block[last]
	}
	return unpackWords(block)
}

// mix is the XXTEA round function.
func mix(y, z, sum, keyWord uint32) uint32 {
	return (z>>5 ^ y<<2) + ((y>>3 ^ z<<4) ^ (sum ^ y)) + (keyWord ^ z)
}

// packWords reads data as little-endian uint32s, padding the final short word
// with zero bytes. withLength appends the byte count, which is how the
// decrypting side knows where the padding starts.
func packWords(data []byte, withLength bool) []uint32 {
	words := make([]uint32, 0, len(data)/4+2)
	for index := 0; index < len(data); index += 4 {
		words = append(words,
			uint32(byteAt(data, index))|
				uint32(byteAt(data, index+1))<<8|
				uint32(byteAt(data, index+2))<<16|
				uint32(byteAt(data, index+3))<<24)
	}
	if withLength {
		words = append(words, uint32(len(data)))
	}
	return words
}

// byteAt reads past the end as zero, the way the JavaScript's charCodeAt guard
// does. The final word of an unaligned message is padded rather than dropped.
func byteAt(data []byte, index int) byte {
	if index < len(data) {
		return data[index]
	}
	return 0
}

// unpackWords writes the words back out little-endian. The length word stays in
// the output: the ciphertext carries it, and trimming here would corrupt it.
func unpackWords(words []uint32) []byte {
	out := make([]byte, 0, len(words)*4)
	for _, word := range words {
		out = append(out, byte(word), byte(word>>8), byte(word>>16), byte(word>>24))
	}
	return out
}
