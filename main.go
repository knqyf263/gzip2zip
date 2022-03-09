package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"log"
	"os"
	"syscall"
)

var (
	localFileHeaderSignature   = []byte{'P', 'K', 3, 4, 10, 0, 8, 0, 8, 0}
	centralFileHeaderSignature = []byte{'P', 'K', 1, 2, 20, 0, 20, 0, 8, 0, 8, 0}
	endOfCentralDirSignature   = []byte{'P', 'K', 5, 6, 0, 0, 0, 0, 1, 0, 1, 0}
)

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage error")
	}

	gzipFile := os.Args[1]

	f, err := os.Open(gzipFile)
	if err != nil {
		log.Fatalf("file open error: %s", err)
	}
	defer f.Close()

	// stdout should be a pipe
	w := os.Stdout

	// parse gzip header
	fileName, gzipOffset := gzipHeader(f)

	// zip file modification date, CRC, and sizes -- initialize to zero for the
	// local header (the actual CRC and sizes follow the compressed data)
	var descriptor [16]byte

	var offset int

	// write zip local header
	locOffset := offset
	offset += localFileHeader(w, fileName, descriptor)

	// write file content
	compressedSize := fileData(w, f, gzipOffset)
	offset += compressedSize

	// write data descriptor
	offset += dataDescriptor(w, f, &descriptor, compressedSize)

	// write zip central directory
	cenOffset := offset
	offset += centralDirectory(w, descriptor, fileName, locOffset)

	//  write end-of-central-directory
	endOffset := offset
	endOfCentralDirectory(w, endOffset-cenOffset, cenOffset)
}

func gzipHeader(f *os.File) (string, int) {
	// gzip header
	var header [10]byte

	n, err := f.Read(header[:])
	if err != nil {
		log.Fatalf("gzip header read error: %s", err)
	}
	offset := n

	// validation
	if n < len(header) {
		log.Fatal("gzip error")
	} else if header[0] != 0x1f || header[1] != 0x8b {
		log.Fatal("not gzip")
	} else if header[2] != 8 {
		log.Fatal("not deflate")
	} else if header[3]&0xe0 != 0 {
		log.Fatal("invalid flag")
	}

	// TODO: extra field (ignore)
	if header[3]&4 > 0 {
		log.Fatal("extra field not implemented yet")
	}

	// file name (save)
	var fileName string
	if header[3]&8 > 0 {
		var name bytes.Buffer
		b := make([]byte, 1)
		for {
			if n, err = f.Read(b); err != nil {
				log.Fatalf("read error: %s", err)
			}
			offset += n

			if b[0] == 0 {
				break
			}
			name.Write(b)
		}
		fileName = name.String()
	} else { // no file name
		fileName = "-"
	}

	// TODO: comment (ignore)
	if header[3]&16 > 0 {
		log.Fatal("comment not implemented yet")
	}

	// TODO: header crc (ignore)
	if header[3]&2 > 0 {
		log.Fatal("header crc not implemented yet")
	}

	return fileName, offset
}

func localFileHeader(w io.Writer, fileName string, descriptor [16]byte) int {
	var header []byte

	// local file header signature
	header = append(header, localFileHeaderSignature...)

	// TODO: last mod file time & last mod file data
	// CRC-32 and sizes(the actual CRC will be in data descriptor)
	header = append(header, descriptor[:]...)

	// file name length
	var nameLen [2]byte
	binary.LittleEndian.PutUint16(nameLen[:], uint16(len(fileName)))
	header = append(header, nameLen[:]...)

	// extra field length
	header = append(header, 0, 0)

	// filename
	header = append(header, []byte(fileName)...)

	n, err := w.Write(header)
	if err != nil {
		log.Fatal(err)
	}

	return n
}

func fileData(w, f *os.File, gzipOffset int) int {
	fi, err := f.Stat()
	if err != nil {
		log.Fatalf("stat error: %s", err)
	}

	// copy raw deflate stream, saving eight-byte gzip trailer
	offset := int64(gzipOffset)
	n, err := syscall.Splice(int(f.Fd()), &offset, int(w.Fd()), nil, int(fi.Size()-8-offset), 0)
	if err != nil {
		log.Fatalf("splice error: %s", err)
	}

	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		log.Fatalf("seek error: %s", err)
	}

	return int(n)
}

func dataDescriptor(w io.Writer, f *os.File, descriptor *[16]byte, csize int) int {
	// parse gzip trailer
	var crc, size [4]byte
	if _, err := f.Read(crc[:]); err != nil {
		log.Fatalf("crc read error: %s", err)
	}
	if _, err := f.Read(size[:]); err != nil {
		log.Fatalf("size read error: %s", err)
	}

	// CRC-32
	copy(descriptor[4:8], crc[:])

	// compressed size
	var compressedSize [4]byte
	binary.LittleEndian.PutUint32(compressedSize[:], uint32(csize))
	copy(descriptor[8:12], compressedSize[:])

	// decompressed size
	copy(descriptor[12:16], size[:])

	// the first 4 bytes are not needed here
	n, err := w.Write(descriptor[4:])
	if err != nil {
		log.Fatalf("write error: %s", err)
	}
	return n
}

func centralDirectory(w io.Writer, descriptor [16]byte, fileName string, locOffset int) int {
	var header []byte

	// central file header signature
	header = append(header, centralFileHeaderSignature...)

	// modification date, CRC, and sizes
	header = append(header, descriptor[:]...)

	// file name length
	var nameLen [2]byte
	binary.LittleEndian.PutUint16(nameLen[:], uint16(len(fileName)))
	header = append(header, nameLen[:]...)

	// extra field, etc.
	var extra [12]byte
	header = append(header, extra[:]...)

	// local directory offset
	var offset [4]byte
	binary.LittleEndian.PutUint32(offset[:], uint32(locOffset))
	header = append(header, offset[:]...)

	// filename
	header = append(header, []byte(fileName)...)

	n, err := w.Write(header)
	if err != nil {
		log.Fatalf("write error: %s", err)
	}

	return n
}

func endOfCentralDirectory(w *os.File, centralSize, centralOffset int) {
	var header []byte

	// end of central directory header signature
	header = append(header, endOfCentralDirSignature...)

	// central directory size
	var size [4]byte
	binary.LittleEndian.PutUint32(size[:], uint32(centralSize))
	header = append(header, size[:]...)

	// central directory offset
	var offset [4]byte
	binary.LittleEndian.PutUint32(offset[:], uint32(centralOffset))
	header = append(header, offset[:]...)

	// comment
	header = append(header, 0, 0)

	if _, err := w.Write(header); err != nil {
		log.Fatalf("write error: %s", err)
	}
}
