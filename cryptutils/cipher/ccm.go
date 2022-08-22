package cipher

import (
	"bytes"
	"crypto/cipher"
	"errors"
	_ "unsafe"

	"go.x2ox.com/sorbifolia/cryptutils/internal/mac/cbcmac"
)

type ccm struct {
	c         cipher.Block
	mac       *cbcmac.MAC
	nonceSize int
	tagSize   int
}

// NewCCMWithNonceAndTagSizes returns the given 128-bit, block cipher wrapped in Counter with CBC-MAC Mode, which accepts nonces of the given length.
// the formatting of this function is defined in SP800-38C, Appendix A.
// Each arguments have own valid range:
//
//	nonceSize should be one of the {7, 8, 9, 10, 11, 12, 13}.
//	tagSize should be one of the {4, 6, 8, 10, 12, 14, 16}.
//	Otherwise, it panics.
//
// The maximum payload size is defined as 1<<uint((15-nonceSize)*8)-1.
// If the given payload size exceeds the limit, it returns a error (Seal returns nil instead).
// The payload size is defined as len(plaintext) on Seal, len(ciphertext)-tagSize on Open.
func NewCCMWithNonceAndTagSizes(c cipher.Block, nonceSize, tagSize int) (cipher.AEAD, error) {
	if c.BlockSize() != 16 {
		return nil, errors.New("cipher: CCM mode requires 128-bit block cipher")
	}
	if !(7 <= nonceSize && nonceSize <= 13) {
		return nil, errors.New("cipher: invalid nonce size")
	}
	if !(4 <= tagSize && tagSize <= 16 && tagSize&1 == 0) {
		return nil, errors.New("cipher: invalid tag size")
	}

	return &ccm{
		c:         c,
		mac:       cbcmac.New(c),
		nonceSize: nonceSize,
		tagSize:   tagSize,
	}, nil
}

func (ccm *ccm) NonceSize() int { return ccm.nonceSize }
func (ccm *ccm) Overhead() int  { return ccm.tagSize }

func (ccm *ccm) Seal(dst, nonce, plaintext, data []byte) []byte {
	if len(nonce) != ccm.nonceSize || maxUvarint(15-ccm.nonceSize) < uint64(len(plaintext)) {
		panic("cipher: incorrect nonce length given to CCM or plaintext too large")
	}

	ret, ciphertext := sliceForAppend(dst, len(plaintext)+ccm.mac.Size())

	// Formatting of the Counter Blocks are defined in A.3.
	Ctr := make([]byte, 16)               // Ctr0
	Ctr[0] = byte(15 - ccm.nonceSize - 1) // [q-1]3
	copy(Ctr[1:], nonce)                  // N

	S0 := ciphertext[len(plaintext):] // S0
	ccm.c.Encrypt(S0, Ctr)

	Ctr[15] = 1 // Ctr1

	ctr := cipher.NewCTR(ccm.c, Ctr)

	ctr.XORKeyStream(ciphertext, plaintext)

	T := ccm.getTag(Ctr, data, plaintext)

	xorBytes(S0, S0, T) // T^S0

	return ret[:len(plaintext)+ccm.tagSize]
}

func (ccm *ccm) Open(dst, nonce, ciphertext, data []byte) ([]byte, error) {
	if len(nonce) != ccm.nonceSize ||
		len(ciphertext) <= ccm.tagSize ||
		maxUvarint(15-ccm.nonceSize) < uint64(len(ciphertext)-ccm.tagSize) {
		return nil, errors.New("cipher: incorrect nonce or ciphertext length given to CCM or ciphertext too large")
	}

	ret, plaintext := sliceForAppend(dst, len(ciphertext)-ccm.tagSize)

	// Formatting of the Counter Blocks are defined in A.3.
	Ctr := make([]byte, 16)               // Ctr0
	Ctr[0] = byte(15 - ccm.nonceSize - 1) // [q-1]3
	copy(Ctr[1:], nonce)                  // N

	S0 := make([]byte, 16) // S0
	ccm.c.Encrypt(S0, Ctr)

	Ctr[15] = 1 // Ctr1

	ctr := cipher.NewCTR(ccm.c, Ctr)

	ctr.XORKeyStream(plaintext, ciphertext[:len(plaintext)])

	T := ccm.getTag(Ctr, data, plaintext)

	xorBytes(T, T, S0)

	if !bytes.Equal(T[:ccm.tagSize], ciphertext[len(plaintext):]) {
		return nil, errors.New("cipher: message authentication failed")
	}

	return ret, nil
}

// getTag reuses a Ctr block for making the B0 block because of some parts are the same.
// For more details, see A.2 and A.3.
func (ccm *ccm) getTag(Ctr, data, plaintext []byte) []byte {
	ccm.mac.Reset()

	B := Ctr                                                // B0
	B[0] |= byte(((ccm.tagSize - 2) / 2) << 3)              // [(t-2)/2]3
	putUvarint(B[1+ccm.nonceSize:], uint64(len(plaintext))) // Q

	if len(data) > 0 {
		B[0] |= 1 << 6 // Adata

		_, _ = ccm.mac.Write(B)

		if len(data) < (1<<15 - 1<<7) {
			putUvarint(B[:2], uint64(len(data)))

			_, _ = ccm.mac.Write(B[:2])
		} else if len(data) <= 1<<31-1 {
			B[0] = 0xff
			B[1] = 0xfe
			putUvarint(B[2:6], uint64(len(data)))

			_, _ = ccm.mac.Write(B[:6])
		} else {
			B[0] = 0xff
			B[1] = 0xff
			putUvarint(B[2:10], uint64(len(data)))

			_, _ = ccm.mac.Write(B[:10])
		}
		_, _ = ccm.mac.Write(data)
		ccm.mac.PadZero()
	} else {
		_, _ = ccm.mac.Write(B)
	}

	_, _ = ccm.mac.Write(plaintext)
	ccm.mac.PadZero()

	return ccm.mac.Sum(nil)
}
