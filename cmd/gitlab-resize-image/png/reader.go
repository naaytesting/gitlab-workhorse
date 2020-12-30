package png

import (
	"encoding/binary"
	"fmt"
	"io"
	"io/ioutil"
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

const (
	crcLen         = 4
	pngMagicLen    = 8
	chunkHeaderLen = 8
	pngMagic       = "\x89PNG\r\n\x1a\n"
)

// Reader is an io.Reader decorator that skips certain PNG chunks known to cause problems.
// If the image stream is not a PNG, it will yield all bytes unchanged to the underlying
// reader.
// See also https://gitlab.com/gitlab-org/gitlab/-/issues/287614
type Reader struct {
	underlying     io.Reader
	state          int
	buffer         [4096]byte
	bytesRemaining int
}

func NewReader(r io.Reader) *Reader {
	return &Reader{
		underlying:     r,
		state:          stateReadMagic,
		bytesRemaining: 0,
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
		return r.copyChunkData(p, r.buffer[:], r.bytesRemaining)
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
	magicBytes := r.buffer[:pngMagicLen]
	n, err = io.ReadFull(r.underlying, magicBytes)
	if err != nil {
		return
	}

	// Immediately move to done when we're not reading a PNG
	if string(magicBytes) != pngMagic {
		debug("Not a PNG - read file unchanged")
		r.state = stateDone
	} else {
		r.state = stateReadNextChunk
	}

	return copy(dst, magicBytes), nil
}

// Starts reading a new chunk. We need to look at each chunk between IHDR and PLTE/IDAT
// to see whether we should skip it or forward it.
func (r *Reader) readNextChunk(dst []byte) (int, error) {
	debug("Read next chunk")
	chunkLen, chunkTyp, err := r.readChunkLengthAndType()
	if err != nil {
		return 0, err
	}
	fullChunkLen := int(chunkLen + crcLen)

	switch chunkTyp {
	case "iCCP":
		debug("!! iCCP chunk found; skipping")
		// Consume chunk and toss out result.
		_, err := io.CopyN(ioutil.Discard, r.underlying, int64(fullChunkLen))
		r.state = stateDone
		return 0, err

	case "PLTE", "IDAT", "IEND":
		// This means there was no iCCP chunk and we can just forward all
		// remaining work to the underlying reader.
		debug("Encountered", chunkTyp, "(no iCCP chunk found)")
		n := copy(dst, r.buffer[:chunkHeaderLen])
		m, err := r.underlying.Read(dst[n:])
		r.state = stateDone
		return n + m, err

	default:
		// iCCP chunk not found yet; we need to remain in this state and read more chunks.
		debug("read next chunk", chunkTyp)
		bufferHead := r.buffer[:chunkHeaderLen]
		bufferTail := r.buffer[chunkHeaderLen:]

		// Copy the chunk header bytes we already read.
		n := copy(dst, bufferHead)

		// Copy the remaining bytes.
		m, err := r.copyChunkData(dst[n:], bufferTail, fullChunkLen)
		return n + m, err
	}
}

// Reads the first 8 bytes from a PNG chunk, which are
// the chunk length (4 byte) and the chunk type (4 byte).
func (r *Reader) readChunkLengthAndType() (uint32, string, error) {
	debug("Read chunk def")
	// Read chunk length and type.
	_, err := io.ReadFull(r.underlying, r.buffer[:chunkHeaderLen])
	if err != nil {
		return 0, "", err
	}

	chunkLen := binary.BigEndian.Uint32(r.buffer[:4])
	chunkTyp := string(r.buffer[4:chunkHeaderLen])

	debug("LEN:", chunkLen, "TYP:", chunkTyp)

	return chunkLen, chunkTyp, nil
}

func (r *Reader) copyChunkData(dst []byte, src []byte, remainingBytes int) (int, error) {
	debug("copying chunk data")
	// Read at most the remaining chunk bytes
	// OR the number of bytes we can fit into the destination buffer
	// OR the number of bytes we can fit into the read buffer,
	// whichever is smallest.
	lastByte := min(min(remainingBytes, len(src)), len(dst))
	m, err := io.ReadFull(r.underlying, src[:lastByte])
	if err != nil {
		return m, err
	}

	// Transfer read buffer contents to destination buffer.
	m = copy(dst, src[:m])

	if m < remainingBytes {
		// We weren't able to read the full chunk. Keep trying with the next Read.
		r.bytesRemaining = remainingBytes - m
		r.state = stateReadCurrentChunk
	} else {
		// We read the full chunk so we're ready to read the next.
		r.bytesRemaining = 0
		r.state = stateReadNextChunk
	}
	debug("bytes remaining:", r.bytesRemaining)
	return m, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
