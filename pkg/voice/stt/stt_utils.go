package stt

import (
	"bytes"
	"encoding/binary"
)

// createWAVFromPCM creates a minimal WAV file from PCM data
func createWAVFromPCM(pcmData []byte, sampleRate int) ([]byte, error) {
	buf := new(bytes.Buffer)

	numChannels := 1
	bitsPerSample := 16
	byteRate := sampleRate * numChannels * bitsPerSample / 8
	blockAlign := numChannels * bitsPerSample / 8
	dataSize := len(pcmData)
	riffSize := 36 + dataSize

	buf.WriteString("RIFF")
	writeUint32(buf, uint32(riffSize))
	buf.WriteString("WAVE")

	buf.WriteString("fmt ")
	writeUint32(buf, 16)
	writeUint16(buf, 1)
	writeUint16(buf, uint16(numChannels))
	writeUint32(buf, uint32(sampleRate))
	writeUint32(buf, uint32(byteRate))
	writeUint16(buf, uint16(blockAlign))
	writeUint16(buf, uint16(bitsPerSample))

	buf.WriteString("data")
	writeUint32(buf, uint32(dataSize))
	buf.Write(pcmData)

	return buf.Bytes(), nil
}

func writeUint32(buf *bytes.Buffer, v uint32) {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	buf.Write(b)
}

func writeUint16(buf *bytes.Buffer, v uint16) {
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, v)
	buf.Write(b)
}
