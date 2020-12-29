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

func main() {
	if err := _main(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: fatal: %v\n", os.Args[0], err)
		os.Exit(1)
	}
}

const (
	stateReadMagic = iota
	stateReadNextChunk
	stateReadCurrentChunk
	stateDone
)

type chunkDef struct {
	typ string
	len uint32
}

type skipReader struct {
	underlying     io.Reader
	state          int
	buffer         [8]byte
	bytesRemaining []byte
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

func (r *skipReader) readChunkDef() (*chunkDef, error) {
	fmt.Fprintln(os.Stderr, "Read chunk def")
	// Read chunk length and type.
	_, err := io.ReadFull(r.underlying, r.buffer[:])
	if err != nil {
		return nil, err
	}

	chunkLen := binary.BigEndian.Uint32(r.buffer[:4])
	chunkTyp := string(r.buffer[4:])

	fmt.Fprintln(os.Stderr, "LEN:", chunkLen)
	fmt.Fprintln(os.Stderr, "TYP:", chunkTyp)

	return &chunkDef{chunkTyp, chunkLen}, nil
}

func (r *skipReader) readChunk(length uint32) ([]byte, error) {
	buf := make([]byte, length+4)
	_, err := io.ReadFull(r.underlying, buf)
	if err != nil {
		return nil, err
	}
	return buf, nil
}

func (r *skipReader) Read(p []byte) (n int, err error) {
	switch r.state {
	case stateDone:
		fmt.Fprintln(os.Stderr, "STATE: Done (forward read)")
		return r.underlying.Read(p)

	case stateReadMagic:
		fmt.Fprintln(os.Stderr, "STATE: Read magic")
		// consume PNG magic and proceed to reading IHDR chunk
		r.state = stateReadNextChunk
		n, err := io.ReadFull(r.underlying, r.buffer[:])
		copy(p[:], r.buffer[:])
		return n, err

	case stateReadCurrentChunk:
		fmt.Fprintln(os.Stderr, "STATE: Read remaining chunk bytes")
		m := copy(p[:], r.bytesRemaining)

		if m < len(r.bytesRemaining) {
			r.state = stateReadCurrentChunk
			r.bytesRemaining = r.bytesRemaining[m:]
		} else {
			r.state = stateReadNextChunk
			r.bytesRemaining = nil
		}
		return m, nil

	case stateReadNextChunk:
		fmt.Fprintln(os.Stderr, "STATE: Read chunk")
		// in this state, we need to look at each chunk between IHDR and PLTE/IDAT
		// to see whether we should skip it or forward it
		chunkDef, err := r.readChunkDef()
		if err != nil {
			r.state = stateDone
			fmt.Fprintln(os.Stderr, err)
			return 0, err
		}

		fmt.Fprintln(os.Stderr, chunkDef)

		switch chunkDef.typ {
		case "iCCP":
			fmt.Fprintln(os.Stderr, "!! iCCP chunk found; skipping")
			r.state = stateDone
			_, err := r.readChunk(chunkDef.len)
			return 0, err
		case "PLTE", "IDAT":
			fmt.Fprintln(os.Stderr, "Encountered", chunkDef.typ, "No ICCP chunk found")
			// this means there was no iCCP chunk and we can just forward all
			// remaining work to the underlying reader
			r.state = stateDone
			n := copy(p[:], r.buffer[:])
			m, err := r.underlying.Read(p)
			fmt.Fprintln(os.Stderr, "len", n, m)
			return n + m, err
		default:
			fmt.Fprintln(os.Stderr, "read next chunk", chunkDef.typ)
			// remain in state "read chunk"
			n := copy(p[:], r.buffer[:])

			buf, err := r.readChunk(chunkDef.len)
			fmt.Fprintln(os.Stderr, "len", n, len(buf), chunkDef.len)

			m := copy(p[n:], buf)

			if m < len(buf) {
				r.state = stateReadCurrentChunk
				r.bytesRemaining = buf[m:]
			} else {
				r.state = stateReadNextChunk
				r.bytesRemaining = nil
			}

			fmt.Fprintln(os.Stderr, "COPIED:", m)

			return n + m, err
		}
	}

	return 0, fmt.Errorf("unexpected state: %d", r.state)
}
