package storage

// A_memorix V1 对账的成对文件重写
// 按行号子集拷贝原始字节，保证向量精度不因往返而改变

import (
	"os"
)

// writeAcceptedRowsLocked 按行号子集重写成对文件，向量行按原始字节拷贝
func (s *VectorStore) writeAcceptedRowsLocked(
	tmpBinPath string,
	tmpIDsPath string,
	rawDiskIDs []int64,
	accepted map[int]struct{},
) (int64, error) {
	itemSize := s.vectorItemSizeLocked()
	input, err := os.Open(s.binPath())
	if err != nil {
		return 0, err
	}
	defer func() { _ = input.Close() }()
	outputVectors, err := os.OpenFile(tmpBinPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o666)
	if err != nil {
		return 0, err
	}
	defer func() { _ = outputVectors.Close() }()
	outputIDs, err := os.OpenFile(tmpIDsPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o666)
	if err != nil {
		return 0, err
	}
	defer func() { _ = outputIDs.Close() }()

	const chunkRows = 10000
	vectorBuffer := make([]byte, chunkRows*int(itemSize))
	var written int64
	rowStart := 0
	for rowStart < len(rawDiskIDs) {
		batch := chunkRows
		if len(rawDiskIDs)-rowStart < batch {
			batch = len(rawDiskIDs) - rowStart
		}
		vectorChunk := vectorBuffer[:batch*int(itemSize)]
		if _, err := readFullAt(input, vectorChunk, int64(rowStart)*itemSize); err != nil {
			return 0, err
		}
		for index := 0; index < batch; index++ {
			row := rowStart + index
			if _, ok := accepted[row]; !ok {
				continue
			}
			if _, err := outputVectors.Write(vectorChunk[index*int(itemSize) : (index+1)*int(itemSize)]); err != nil {
				return 0, err
			}
			encoded := make([]byte, 8)
			for offset := 0; offset < 8; offset++ {
				encoded[offset] = byte(uint64(rawDiskIDs[row]) >> (56 - 8*offset))
			}
			if _, err := outputIDs.Write(encoded); err != nil {
				return 0, err
			}
			written++
		}
		rowStart += batch
	}
	if err := outputVectors.Sync(); err != nil {
		return 0, err
	}
	if err := outputIDs.Sync(); err != nil {
		return 0, err
	}
	return written, nil
}
