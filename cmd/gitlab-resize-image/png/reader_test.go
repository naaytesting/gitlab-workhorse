package png

import (
	"hash/crc64"
	"image"
	"io"
	"io/ioutil"
	"os"
	"testing"

	_ "image/jpeg" // registers JPEG format for image.Decode
	_ "image/png"  // registers PNG format for image.Decode

	"github.com/stretchr/testify/require"
)

const goodPNG = "../../../testdata/image.png"
const badPNG = "../../../testdata/image_bad_iccp.png"
const jpg = "../../../testdata/image.jpg"

func TestReadImageUnchanged(t *testing.T) {
	testCases := []struct {
		desc      string
		imagePath string
		imageType string
	}{
		{
			desc:      "image is not a PNG",
			imagePath: jpg,
			imageType: "jpeg",
		},
		{
			desc:      "image is PNG without iCCP chunk",
			imagePath: goodPNG,
			imageType: "png",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			requireValidImage(t, NewReader(imageReader(t, tc.imagePath)), tc.imageType)
			requireStreamUnchanged(t, NewReader(imageReader(t, tc.imagePath)), imageReader(t, tc.imagePath))
		})
	}
}

func TestReadPNGWithBadICCPChunkDecodesSuccessfully(t *testing.T) {
	_, fmt, err := image.Decode(NewReader(imageReader(t, badPNG)))
	require.NoError(t, err)
	require.Equal(t, "png", fmt)
}

func imageReader(t *testing.T, path string) io.Reader {
	f, err := os.Open(path)
	require.NoError(t, err)
	return f
}

func requireValidImage(t *testing.T, r io.Reader, expected string) {
	_, fmt, err := image.Decode(r)
	require.NoError(t, err)
	require.Equal(t, expected, fmt)
}

func requireStreamUnchanged(t *testing.T, actual io.Reader, expected io.Reader) {
	actualBytes, err := ioutil.ReadAll(actual)
	require.NoError(t, err)
	expectedBytes, err := ioutil.ReadAll(expected)
	require.NoError(t, err)

	table := crc64.MakeTable(crc64.ISO)
	sumActual := crc64.Checksum(actualBytes, table)
	sumExpected := crc64.Checksum(expectedBytes, table)
	require.Equal(t, sumExpected, sumActual)
}
