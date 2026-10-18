package anthrex

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

// MaxFrame is the largest body either side accepts (proto::codec::MAX_FRAME).
const MaxFrame = 16 * 1024 * 1024

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// EncodeFrame serialises v (a generic value, see Marshal) and prepends the
// 4-byte big-endian length (proto::codec::encode).
func EncodeFrame(v any) ([]byte, error) {
	body, err := Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(body) > MaxFrame {
		return nil, fmt.Errorf("anthrex: frame too large: %d bytes (max %d)", len(body), MaxFrame)
	}
	out := make([]byte, 4, 4+len(body))
	binary.BigEndian.PutUint32(out, uint32(len(body)))
	return append(out, body...), nil
}

// WriteFrame writes one frame.
func WriteFrame(w io.Writer, v any) error {
	f, err := EncodeFrame(v)
	if err != nil {
		return err
	}
	_, err = w.Write(f)
	return err
}

// ReadFrame reads one frame and decodes its body. It returns io.EOF when the
// peer closed the stream cleanly between frames.
func ReadFrame(r io.Reader) (any, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:1]); err != nil {
		return nil, err // io.EOF on a clean close
	}
	if _, err := io.ReadFull(r, hdr[1:]); err != nil {
		return nil, io.ErrUnexpectedEOF
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > MaxFrame {
		return nil, fmt.Errorf("anthrex: frame too large: %d bytes (max %d)", n, MaxFrame)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, io.ErrUnexpectedEOF
	}
	return Unmarshal(body)
}
