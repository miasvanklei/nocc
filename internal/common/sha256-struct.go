package common

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"io"
	"os"
)

//goland:noinspection GoSnakeCaseUsage
type SHA256 struct {
	B0_7, B8_15, B16_23, B24_31 uint64
}

func (h *SHA256) IsEmpty() bool {
	return h.B0_7 == 0 && h.B8_15 == 0 && h.B16_23 == 0 && h.B24_31 == 0
}

func (h *SHA256) XorWith(other *SHA256) {
	h.B0_7 ^= other.B0_7
	h.B8_15 ^= other.B8_15
	h.B16_23 ^= other.B16_23
	h.B24_31 ^= other.B24_31
}

func (h *SHA256) ToShortHexString() string {
	return fmt.Sprintf("%x", h.B0_7^h.B8_15^h.B16_23^h.B24_31)
}

func (h *SHA256) ToLongHexString() string {
	return fmt.Sprintf("%x-%x-%x-%x", h.B0_7, h.B8_15, h.B16_23, h.B24_31)
}

func (h *SHA256) FromLongHexString(hex string) {
	if n, _ := fmt.Sscanf(hex, "%x-%x-%x-%x", &h.B0_7, &h.B8_15, &h.B16_23, &h.B24_31); n != 4 {
		*h = SHA256{}
		// if it couldn't be parsed, it would be IsEmpty()
	}
}

func MakeSHA256Struct(hasher hash.Hash) SHA256 {
	b := hasher.Sum(nil) // len is 32
	return SHA256{
		B0_7:   binary.BigEndian.Uint64(b[0:8]),
		B8_15:  binary.BigEndian.Uint64(b[8:16]),
		B16_23: binary.BigEndian.Uint64(b[16:24]),
		B24_31: binary.BigEndian.Uint64(b[24:32]),
	}
}

func GetFileSHA256(filePath string) (SHA256, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return SHA256{}, err
	}
	defer f.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return SHA256{}, err
	}
	return MakeSHA256Struct(hasher), nil
}

// CalcSHA256OfFile reads the opened file up to end and returns its sha256 and contents.
func CalcSHA256OfFile(file *os.File, fileSize int64, preallocatedBuf []byte) (SHA256, []byte, error) {
	var buffer []byte
	if fileSize > int64(len(preallocatedBuf)) {
		buffer = make([]byte, fileSize)
	} else {
		buffer = preallocatedBuf[:fileSize]
	}
	_, err := io.ReadFull(file, buffer)
	if err != nil {
		return SHA256{}, nil, err
	}

	hasher := sha256.New()
	_, _ = hasher.Write(buffer)
	return MakeSHA256Struct(hasher), buffer, nil
}

// CalcSHA256OfFileName is like CalcSHA256OfFile but for a file name, not descriptor.
func CalcSHA256OfFileName(fileName string, preallocatedBuf []byte) (SHA256, []byte, error) {
	file, err := os.Open(fileName)
	if err != nil {
		return SHA256{}, nil, err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return SHA256{}, nil, err
	}

	return CalcSHA256OfFile(file, stat.Size(), preallocatedBuf)
}
