package png

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

const (
	// We are reading the PNG magic bytes
	stateReadMagic = iota
	// We are reading a new PNG chunk
	stateReadNextChunk
	// We continue to read an existing chunk
	stateReadCurrentChunk
	// We are done skipping potential chunks and let the
	// underlying reader take over
	stateDone
)

const crcLen = 4
const pngMagic = "\x89PNG\r\n\x1a\n"

// Reader is an io.Reader decorator that skips certain PNG chunks known to cause problems.
// If the image stream is not a PNG, it will yield all bytes unchanged to the underlying
// reader.
// See also https://gitlab.com/gitlab-org/gitlab/-/issues/287614
type Reader struct {
	underlying     io.Reader
	state          int
	buffer         [8]byte
	bytesRemaining []byte
}

func NewReader(r io.Reader) *Reader {
	return &Reader{
		underlying:     r,
		state:          stateReadMagic,
		bytesRemaining: nil,
	}
}

func (r *Reader) Read(p []byte) (n int, err error) {
	switch r.state {
	case stateDone:
		// There is no more work to do, we let the underlying reader take over.
		debug("Done (forward read)")
		return r.underlying.Read(p)

	case stateReadMagic:
		return r.readMagic(p)

	case stateReadNextChunk:
		return r.readNextChunk(p)

	case stateReadCurrentChunk:
		// This means in the previous invocation, we weren't able to read
		// the entire chunk. Keep copying chunk data.
		debug("Read remaining chunk bytes")
		return r.copyChunkData(p, r.bytesRemaining), nil
	}

	return 0, fmt.Errorf("unexpected state: %d", r.state)
}

func debug(args ...interface{}) {
	if os.Getenv("DEBUG") == "1" {
		fmt.Fprintln(os.Stderr, args...)
	}
}

// Consume PNG magic and proceed to reading the IHDR chunk.
func (r *Reader) readMagic(dst []byte) (n int, err error) {
	debug("Read magic")
	n, err = io.ReadFull(r.underlying, r.buffer[:])
	if err != nil {
		return
	}

	// Immediately move to done when we're not reading a PNG
	if string(r.buffer[:]) != pngMagic {
		debug("Not a PNG - read file unchanged")
		r.state = stateDone
	} else {
		r.state = stateReadNextChunk
	}

	return copy(dst, r.buffer[:]), nil
}

// Starts reading a new chunk. We need to look at each chunk between IHDR and PLTE/IDAT
// to see whether we should skip it or forward it.
func (r *Reader) readNextChunk(dst []byte) (n int, err error) {
	debug("Read next chunk")
	chunkLen, chunkTyp, err := r.readChunkDef()
	if err != nil {
		return
	}

	switch chunkTyp {
	case "iCCP":
		debug("!! iCCP chunk found; skipping")
		// Consume chunk and toss out result.
		_, err = r.readChunk(chunkLen)
		r.state = stateDone
		return

	case "PLTE", "IDAT":
		// This means there was no iCCP chunk and we can just forward all
		// remaining work to the underlying reader.
		debug("Encountered", chunkTyp, "(no iCCP chunk found)")
		n = copy(dst, r.buffer[:])
		m, err := r.underlying.Read(dst[n:])
		r.state = stateDone
		return n + m, err

	default:
		// iCCP chunk not found yet; we need to remain in this state and read more chunks.
		debug("read next chunk", chunkTyp)
		n = copy(dst, r.buffer[:])
		buf, err := r.readChunk(chunkLen)
		if err != nil {
			return n, err
		}
		return n + r.copyChunkData(dst[n:], buf), nil
	}
}

// Reads the first 8 bytes from a PNG chunk, which are
// the chunk length (4 byte) and the chunk type (4 byte).
func (r *Reader) readChunkDef() (uint32, string, error) {
	debug("Read chunk def")
	// Read chunk length and type.
	_, err := io.ReadFull(r.underlying, r.buffer[:])
	if err != nil {
		return 0, "", err
	}

	chunkLen := binary.BigEndian.Uint32(r.buffer[:4])
	chunkTyp := string(r.buffer[4:])

	debug("LEN:", chunkLen, "TYP:", chunkTyp)

	return chunkLen, chunkTyp, nil
}

// Reads the entire chunk, including the CRC part, which is not
// included in the length reported by the chunk length bits.
func (r *Reader) readChunk(length uint32) ([]byte, error) {
	buf := make([]byte, length+crcLen)
	_, err := io.ReadFull(r.underlying, buf)
	if err != nil {
		return nil, err
	}
	return buf, nil
}

func (r *Reader) copyChunkData(dst []byte, src []byte) int {
	n := copy(dst, src)
	// Copy only fills the destination buffer, which might not be large enough
	// to hold the entire chunk; in that case we need to keep reading the current
	// chunk with the next call to Read.
	if n < len(src) {
		r.state = stateReadCurrentChunk
		r.bytesRemaining = src[n:]
	} else {
		r.state = stateReadNextChunk
		r.bytesRemaining = nil
	}

	return n
}
