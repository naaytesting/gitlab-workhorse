package main

import (
	"encoding/binary"
	"fmt"
	"image"
	"io"
	"os"
	"strconv"

	"github.com/disintegration/imaging"
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

// An io.Reader adapter that skips certain PNG chunks known to cause problems.
type skipReader struct {
	underlying     io.Reader
	state          int
	buffer         [8]byte
	bytesRemaining []byte
}

// Holds the chunk type and its length in bytes
type chunkDef struct {
	typ string
	len uint32
}

func main() {
	if err := _main(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: fatal: %v\n", os.Args[0], err)
		os.Exit(1)
	}
}

func _main() error {
	widthParam := os.Getenv("GL_RESIZE_IMAGE_WIDTH")
	requestedWidth, err := strconv.Atoi(widthParam)
	if err != nil {
		return fmt.Errorf("GL_RESIZE_IMAGE_WIDTH: %w", err)
	}

	src, formatName, err := image.Decode(newSkipReader(os.Stdin))
	if err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	imagingFormat, err := imaging.FormatFromExtension(formatName)
	if err != nil {
		return fmt.Errorf("find imaging format: %w", err)
	}

	image := imaging.Resize(src, requestedWidth, 0, imaging.Lanczos)
	return imaging.Encode(os.Stdout, image, imagingFormat)
}

func newSkipReader(r io.Reader) *skipReader {
	return &skipReader{
		underlying:     r,
		state:          stateReadMagic,
		bytesRemaining: nil,
	}
}

func (r *skipReader) Read(p []byte) (n int, err error) {
	switch r.state {
	case stateDone:
		// There is no more work to do, we let the underlying reader take over.
		log("Done (forward read)")
		return r.underlying.Read(p)

	case stateReadMagic:
		return r.readMagic(p)

	case stateReadNextChunk:
		return r.readNextChunk(p)

	case stateReadCurrentChunk:
		// This means in the previous invocation, we weren't able to read
		// the entire chunk. Keep copying chunk data.
		log("Read remaining chunk bytes")
		return r.copyChunkData(p, r.bytesRemaining), nil
	}

	return 0, fmt.Errorf("unexpected state: %d", r.state)
}

func log(args ...interface{}) {
	if os.Getenv("DEBUG") == "1" {
		fmt.Fprintln(os.Stderr, args...)
	}
}

// Consume PNG magic and proceed to reading the IHDR chunk.
func (r *skipReader) readMagic(dst []byte) (n int, err error) {
	log("Read magic")
	n, err = io.ReadFull(r.underlying, r.buffer[:])
	if err != nil {
		return
	}

	// Immediately move to done when we're not reading a PNG
	if string(r.buffer[:]) != pngMagic {
		log("Not a PNG - read file unchanged")
		r.state = stateDone
	} else {
		r.state = stateReadNextChunk
	}

	return copy(dst, r.buffer[:]), nil
}

// Starts reading a new chunk. We need to look at each chunk between IHDR and PLTE/IDAT
// to see whether we should skip it or forward it.
func (r *skipReader) readNextChunk(dst []byte) (n int, err error) {
	log("Read next chunk")
	chunkDef, err := r.readChunkDef()
	if err != nil {
		return
	}

	switch chunkDef.typ {
	case "iCCP":
		log("!! iCCP chunk found; skipping")
		// Consume chunk and toss out result.
		_, err = r.readChunk(chunkDef.len)
		r.state = stateDone
		return

	case "PLTE", "IDAT":
		// This means there was no iCCP chunk and we can just forward all
		// remaining work to the underlying reader.
		log("Encountered", chunkDef.typ, "(no iCCP chunk found)")
		n := copy(dst, r.buffer[:])
		m, err := r.underlying.Read(dst[n:])
		r.state = stateDone
		return n + m, err

	default:
		// iCCP chunk not found yet; we need to remain in this state and read more chunks.
		log("read next chunk", chunkDef.typ)
		n := copy(dst, r.buffer[:])
		buf, err := r.readChunk(chunkDef.len)
		m := r.copyChunkData(dst[n:], buf)
		return n + m, err
	}
}

// Reads the first 8 bytes from a PNG chunk, which are
// the chunk length (4 byte) and the chunk type (4 byte)
func (r *skipReader) readChunkDef() (*chunkDef, error) {
	log("Read chunk def")
	// Read chunk length and type.
	_, err := io.ReadFull(r.underlying, r.buffer[:])
	if err != nil {
		return nil, err
	}

	chunkLen := binary.BigEndian.Uint32(r.buffer[:4])
	chunkTyp := string(r.buffer[4:])

	log("LEN:", chunkLen, "TYP:", chunkTyp)

	return &chunkDef{chunkTyp, chunkLen}, nil
}

// Reads the entire chunk, including the CRC part, which is not
// included in the length reported by the chunk length bits.
func (r *skipReader) readChunk(length uint32) ([]byte, error) {
	buf := make([]byte, length+crcLen)
	_, err := io.ReadFull(r.underlying, buf)
	if err != nil {
		return nil, err
	}
	return buf, nil
}

func (r *skipReader) copyChunkData(dst []byte, src []byte) int {
	m := copy(dst, src)
	// Copy only fills the destination buffer, which might not be large enough
	// to hold the entire chunk; in that case we need to keep reading the current
	// chunk with the next call to Read.
	if m < len(src) {
		r.state = stateReadCurrentChunk
		r.bytesRemaining = src[m:]
	} else {
		r.state = stateReadNextChunk
		r.bytesRemaining = nil
	}

	return m
}
