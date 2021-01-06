package png

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"io/ioutil"
	"os"
)

const (
	crcLen      = 4
	pngMagicLen = 8
	pngMagic    = "\x89PNG\r\n\x1a\n"
)

// Reader is an io.Reader decorator that skips certain PNG chunks known to cause problems.
// If the image stream is not a PNG, it will yield all bytes unchanged to the underlying
// reader.
// See also https://gitlab.com/gitlab-org/gitlab/-/issues/287614
type Reader struct {
	underlying     io.Reader
	chunkHeader    [8]byte
	chunkBody      [4096]byte
	bytesRemaining int
}

func NewReader(r io.Reader) io.Reader {
	magicBytes, err := readMagic(r)
	if err != nil {
		panic(err)
	}

	if string(magicBytes) != pngMagic {
		debug("Not a PNG - read file unchanged")
		return io.MultiReader(bytes.NewReader(magicBytes), r)
	}

	return io.MultiReader(bytes.NewReader(magicBytes), &Reader{underlying: r}, r)
}

func (r *Reader) Read(p []byte) (n int, err error) {
	if r.bytesRemaining > 0 {
		// This means in the previous invocation, we weren't able to read
		// the entire chunk. Keep copying chunk data.
		return r.copyChunkData(p)
	}
	return r.readNextChunk(p)
}

func debug(args ...interface{}) {
	if os.Getenv("DEBUG") == "1" {
		fmt.Fprintln(os.Stderr, args...)
	}
}

// Consume PNG magic and proceed to reading the IHDR chunk.
func readMagic(r io.Reader) ([]byte, error) {
	debug("Read magic")
	var magicBytes []byte = make([]byte, pngMagicLen)
	_, err := io.ReadFull(r, magicBytes)
	if err != nil {
		return nil, err
	}

	return magicBytes, nil
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
		return 0, err

	case "PLTE", "IDAT", "IEND":
		// This means there was no iCCP chunk and we can just forward all
		// remaining work to the underlying reader.
		debug("Encountered", chunkTyp, "(no iCCP chunk found)")
		n := copy(dst, r.chunkHeader[:])
		m, err := r.underlying.Read(dst[n:])
		if err != nil {
			return n + m, err
		}
		return n + m, io.EOF // EOF passes control to the next reader

	default:
		// iCCP chunk not found yet; we need to remain in this state and read more chunks.
		debug("read next chunk", chunkTyp)

		// Copy the chunk header bytes we already read.
		n := copy(dst, r.chunkHeader[:])

		// Copy the remaining bytes.
		r.bytesRemaining = fullChunkLen
		m, err := r.copyChunkData(dst[n:])
		return n + m, err
	}
}

// Reads the first 8 bytes from a PNG chunk, which are
// the chunk length (4 byte) and the chunk type (4 byte).
func (r *Reader) readChunkLengthAndType() (uint32, string, error) {
	debug("Read chunk def")
	// Read chunk length and type.
	_, err := io.ReadFull(r.underlying, r.chunkHeader[:])
	if err != nil {
		return 0, "", err
	}

	chunkLen := binary.BigEndian.Uint32(r.chunkHeader[:4])
	chunkTyp := string(r.chunkHeader[4:])

	debug("LEN:", chunkLen, "TYP:", chunkTyp)

	return chunkLen, chunkTyp, nil
}

func (r *Reader) copyChunkData(dst []byte) (int, error) {
	debug("copying chunk data")
	// Read at most the remaining chunk bytes
	// OR the number of bytes we can fit into the destination buffer
	// OR the number of bytes we can fit into the read buffer,
	// whichever is smallest.
	lastByte := min(min(r.bytesRemaining, len(r.chunkBody)), len(dst))
	m, err := io.ReadFull(r.underlying, r.chunkBody[:lastByte])
	if err != nil {
		return m, err
	}

	// Transfer read buffer contents to destination buffer.
	m = copy(dst, r.chunkBody[:m])

	if m < r.bytesRemaining {
		// We weren't able to read the full chunk. Keep trying with the next Read.
		r.bytesRemaining -= m
	} else {
		// We read the full chunk so we're ready to read the next.
		r.bytesRemaining = 0
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
