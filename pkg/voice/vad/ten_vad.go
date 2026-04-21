package vad

/*
#cgo CFLAGS: -I${SRCDIR}/../../../third_party/ten-vad/include

#cgo darwin CFLAGS: -I${SRCDIR}/../../../third_party/ten-vad/lib/macOS/ten_vad.framework/Versions/A/Headers
#cgo darwin LDFLAGS: -F${SRCDIR}/../../../third_party/ten-vad/lib/macOS -framework ten_vad -Wl,-rpath,${SRCDIR}/../../../third_party/ten-vad/lib/macOS

#cgo linux,amd64 LDFLAGS: -L${SRCDIR}/../../../third_party/ten-vad/lib/Linux/x64 -lten_vad -Wl,-rpath,'$ORIGIN'/../third_party/ten-vad/lib/Linux/x64 -Wl,-rpath,'$ORIGIN'/third_party/ten-vad/lib/Linux/x64

#cgo windows,amd64 LDFLAGS: -L${SRCDIR}/../../../third_party/ten-vad/lib/Windows/x64 -lten_vad

#include "ten_vad.h"
#include <stdlib.h>
#include <stddef.h>
#include <stdint.h>
*/
import "C"
import (
	"fmt"
	"runtime"
	"unsafe"
)

type TenVAD struct {
	handle     C.ten_vad_handle_t
	hopSize    int
	sampleRate int
	threshold  float32
}

func NewTenVAD(hopSize int, threshold float32) (*TenVAD, error) {
	if hopSize <= 0 {
		return nil, fmt.Errorf("invalid hop size: %d (must be positive)", hopSize)
	}
	if threshold < 0.0 || threshold > 1.0 {
		return nil, fmt.Errorf("invalid threshold: %.2f (must be between 0.0 and 1.0)", threshold)
	}

	var handle C.ten_vad_handle_t
	cHopSize := C.size_t(hopSize)
	cThreshold := C.float(threshold)

	ret := C.ten_vad_create(&handle, cHopSize, cThreshold)
	if ret != 0 || handle == nil {
		return nil, fmt.Errorf("failed to create TEN VAD instance (error code: %d)", ret)
	}

	v := &TenVAD{
		handle:     handle,
		hopSize:    hopSize,
		sampleRate: 16000,
		threshold:  threshold,
	}

	runtime.SetFinalizer(v, func(vad *TenVAD) {
		if vad.handle != nil {
			C.ten_vad_destroy(&vad.handle)
			vad.handle = nil
		}
	})

	return v, nil
}

func (v *TenVAD) Process(pcm []int16) (float32, error) {
	if v.handle == nil {
		return 0.0, fmt.Errorf("TEN VAD instance is uninitialized or closed")
	}
	if len(pcm) != v.hopSize {
		return 0.0, fmt.Errorf("input frame length %d does not match hop size %d", len(pcm), v.hopSize)
	}

	cAudioData := (*C.short)(unsafe.Pointer(&pcm[0]))
	cAudioDataLength := C.size_t(v.hopSize)

	var cOutProbability C.float
	var cOutFlag C.int

	result := C.ten_vad_process(v.handle, cAudioData, cAudioDataLength, &cOutProbability, &cOutFlag)
	if result != 0 {
		return 0.0, fmt.Errorf("TEN VAD process failed (error code: %d)", result)
	}

	return float32(cOutProbability), nil
}

func (v *TenVAD) SampleRate() int {
	return v.sampleRate
}

func (v *TenVAD) FrameLength() int {
	return v.hopSize
}

func (v *TenVAD) Close() error {
	if v.handle == nil {
		return fmt.Errorf("TEN VAD instance is already closed")
	}
	C.ten_vad_destroy(&v.handle)
	v.handle = nil
	runtime.SetFinalizer(v, nil)
	return nil
}
