package gitx

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testImage() *image.Paletted {
	img := image.NewPaletted(image.Rect(0, 0, 16, 16), color.Palette{color.Black, color.White})
	for i := range img.Pix {
		img.Pix[i] = uint8(i % 3 % 2)
	}
	return img
}

func encodedImages(t *testing.T) map[string][]byte {
	t.Helper()
	var pngData, jpegData bytes.Buffer
	if err := png.Encode(&pngData, testImage()); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&jpegData, testImage(), nil); err != nil {
		t.Fatal(err)
	}
	return map[string][]byte{"png": pngData.Bytes(), "jpeg": jpegData.Bytes()}
}

// pngChunk encodes one chunk with a correct CRC.
func pngChunk(kind string, data []byte) string {
	body := append([]byte(kind), data...)
	chunk := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	chunk = append(chunk, body...)
	return string(binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(body)))
}

// pngHeader is an IHDR for an 8-bit grayscale image.
func pngHeader(width, height uint32) string {
	data := binary.BigEndian.AppendUint32(nil, width)
	data = binary.BigEndian.AppendUint32(data, height)
	return pngChunk("IHDR", append(data, 8, 0, 0, 0, 0))
}

func TestParseBinaryFormat(t *testing.T) {
	for format, data := range encodedImages(t) {
		t.Run(format, func(t *testing.T) {
			if got := parseBinaryFormat(data); got != format {
				t.Fatalf("parsed as %q", got)
			}
			if got := parseBinaryFormat(append(bytes.Clone(data), "\nrm *\n"...)); got != "" {
				t.Errorf("a script after the image parsed as %q", got)
			}
			for size := range len(data) {
				if got := parseBinaryFormat(data[:size]); got != "" {
					t.Fatalf("the first %d of %d bytes parsed as %q", size, len(data), got)
				}
			}
		})
	}
	const signature = "\x89PNG\r\n\x1a\n"
	for name, data := range map[string]string{
		"script with a NUL":     "#!/bin/sh\n# \x00\nid\n",
		"header only":           signature,
		"empty":                 "",
		"PNG with no image":     signature + pngHeader(1, 1) + pngChunk("IEND", nil),
		"PNG with a fake IDAT":  signature + pngHeader(1, 1) + pngChunk("IDAT", []byte("rm * # not zlib")) + pngChunk("IEND", nil),
		"PNG too large":         signature + pngHeader(30000, 30000) + pngChunk("IDAT", []byte("x")) + pngChunk("IEND", nil),
		"JPEG comment only":     "\xff\xd8\xff\xfe\x00\x03x\xff\xd9",
		"GIF is not recognized": gifData(t),
		"WebP header only":      "RIFF\x16\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00" + strings.Repeat("\x00", 10),
	} {
		if got := parseBinaryFormat([]byte(data)); got != "" {
			t.Errorf("%s parsed as %q", name, got)
		}
	}
}

func gifData(t *testing.T) string {
	t.Helper()
	var out bytes.Buffer
	if err := gif.Encode(&out, testImage(), nil); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestParseBinaryFormatChecksPNGChunks(t *testing.T) {
	data := bytes.Clone(encodedImages(t)["png"])
	data[20] ^= 0xff // inside IHDR, so its CRC no longer matches
	if got := parseBinaryFormat(data); got != "" {
		t.Fatalf("a corrupted PNG parsed as %q", got)
	}
}

func TestInspectBinaryFiles(t *testing.T) {
	root := initRepo(t)
	for name, content := range map[string][]byte{
		"gone.bin":    []byte("\x00old"),
		"same.bin":    []byte("\x00same"),
		"swapped.png": []byte("\x00verify_signature"),
		"updated.png": encodedImages(t)["png"],
	} {
		if err := os.WriteFile(filepath.Join(root, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "base")
	base := git(t, root, "rev-parse", "HEAD^{tree}")
	image := encodedImages(t)["png"]
	for name, content := range map[string][]byte{
		"logo.png":    image,
		"forged.png":  append(bytes.Clone(image), "\nrm *\n"...),
		"run.sh":      []byte("#!/bin/sh\n# \x00\nid\n"),
		"plain.txt":   []byte("text\n"),
		"swapped.png": image,
		"updated.png": encodedImages(t)["jpeg"],
	} {
		if err := os.WriteFile(filepath.Join(root, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, root, "rm", "-q", "gone.bin")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "change")
	tree := git(t, root, "rev-parse", "HEAD^{tree}")
	files, err := InspectBinaryFiles(root, base, tree)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, file := range files {
		got[file.Path] = file.Format
		if file.Deleted != (file.Path == "gone.bin") {
			t.Errorf("%s: deleted = %v", file.Path, file.Deleted)
		}
	}
	want := map[string]string{"logo.png": "png", "forged.png": "", "run.sh": "", "gone.bin": "", "swapped.png": "", "updated.png": "jpeg"}
	if len(got) != len(want) {
		t.Fatalf("got %+v", files)
	}
	for path, format := range want {
		if actual, ok := got[path]; !ok || actual != format {
			t.Errorf("%s: format %q, listed %v, want %q", path, actual, ok, format)
		}
	}
	text, err := BinaryFileText(root, base, tree)
	if err != nil {
		t.Fatal(err)
	}
	listing, _, _ := strings.Cut(text.Text, "\n\n")
	if strings.Index(listing, `"run.sh"`) > strings.Index(listing, `"logo.png"`) || !strings.Contains(listing, `- "logo.png": decodes as png, with nothing after the image. Data could still be hidden inside it.`) {
		t.Fatalf("unverified files should be listed first:\n%s", listing)
	}
	if !strings.Contains(listing, `- "gone.bin": deleted, no recognized format`) {
		t.Fatalf("a deleted unverified file was not flagged:\n%s", listing)
	}
}
